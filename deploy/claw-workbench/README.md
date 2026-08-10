# ADP Claw 工作台独立部署 Overlay

> Security note: `.env` is data, never shell code. Keep it owned by the current
> deployment user with mode `0600`; strict parsing rejects unknown keys,
> expansion, command substitution, quoting and duplicate assignments. Preflight
> also validates every active/historical locator/OAuth key and cross-purpose key
> independence, requires a verified Workspace host suffix when files are
> enabled, and runs the standalone overlay security linter used by CI.

## Authoritative release manifest

Run the collector on the deployment host only after the target color is healthy
and `switch-active.sh` has completed:

```sh
sh ./scripts/collect-release-manifest.sh
```

The wrapper accepts no caller-supplied revision, digest, migration, color, or
region. It writes `state/release-manifest.json` atomically with mode `0600`.
The JSON is assembled from these authoritative sources:

- `new_api_revision` and `claw_control_revision`: the clean Git `HEAD` of this
  repository; both components are built from the same worktree.
- `adp_revision`: the clean Git `HEAD` of the worktree resolved from
  `ADP_SOURCE_DIR` in the strict `.env` file.
- The three image digests: immutable `repository@sha256:...` references on the
  running new-api, active claw-control, and active ADP containers, checked
  against the local image object. Both the image and container must carry
  `org.opencontainers.image.revision=<matching 40-hex Git revision>`. CI must
  apply this OCI label to all three images; tag-only images are rejected.
- `active_color`: `state/active-color`, cross-checked against both active
  upstreams in `state/Caddyfile.active` and the exact Compose service labels.
- `control_migration`: the newest row in `claw_schema_migrations`, queried in
  the control PostgreSQL container without placing its password on argv.
- `adp_migration`: `schema-sha256:<digest>` over a stable projection of the live
  ADP `public` schema. The ADP fork currently uses SQLAlchemy `create_all` and
  has no migration ledger, so the live schema fingerprint is its authoritative
  schema head. Collection also requires the core workbench tables to exist.
- `caddy_version`: the version returned by the running `workbench-switch`
  Caddy binary.
- `provider_region`: the single consistent Tencent region in
  `WORKBENCH_AGSX_REGION` and, when set, `WORKBENCH_FILE_COS_REGION`, checked
  against the active ADP container's actual environment.
- `config_sha256`: a framed SHA-256 over `.env`, `compose.yml`,
  `new-api.env.example`, active color/Caddy state, and every regular file below
  `caddy/`, `config/`, and `observability/`. Secret directories are never read,
  and configuration contents are never copied to output.

Collection fails closed for dirty worktrees, missing/stopped/unhealthy or
ambiguous containers, mutable image tags, missing/mismatched OCI revisions,
digest mismatches, placeholder or conflicting values, Caddy/color disagreement,
missing schema state, symlinked configuration, or any Git/Docker/PostgreSQL
command failure. Command stderr is deliberately not echoed because provider or
database diagnostics may contain credentials. The manifest contains only
revisions, digests, version identifiers, the region, active color, and a UTC
`collected_at` timestamp; it never contains `.env` values or Secret data.

该目录只新增独立的部署层，不修改现有生产 Compose、Caddy、new-api 数据库或 upstream 核心代码。所有服务均放在 `claw-workbench` profile 下，未显式运行脚本时默认关闭；所有容器都不发布宿主机端口。

## 拓扑与边界

```text
Internet -> existing Caddy
             |-- existing new-api Blue/Green
             `-- Caddyfile.public.snippet
                    |-- claw-control-active:8080 -> workbench-switch -> control Blue/Green
                    `-- adp-chat-client:8000    -> workbench-switch -> ADP Blue/Green

claw-control -> private TLS identity proxy -> existing new-api identity-status
new-api      -> private TLS workbench-switch -> claw-control browser/session API
ADP          -> private TLS workbench-switch -> claw-control internal API
ADP          -> private backend -> ClamAV -> private COS

control PostgreSQL != ADP PostgreSQL != new-api database
control PostgreSQL 保存 session/ticket hash、outbox 和持久控制状态；Redis 只承载可由 PostgreSQL/outbox 重建的事件通知与短期缓存，不能成为身份、授权或计费真值。Turn 与业务持久状态分别留在两套 PostgreSQL。
```

`workbench_backend` 是 Docker internal 网络；数据库只加入该网络。ADP 和 claw-control 额外加入仅用于外连腾讯服务的 `workbench_egress`。`workbench-switch` 和 `new-api-identity-proxy` 是现有 edge 网络与 backend 网络之间的唯一桥接点，且没有 `ports`。`workbench-switch` 同时在 backend 与现有 edge 网络声明证书 SAN 对应的 `workbench-control.internal` 别名，因此 new-api Blue/Green 可在 edge 网络通过 `https://workbench-control.internal:8443` 验证内部 CA 后访问 control，不能使用明文 `http://claw-control-active:8080`。

## 文件说明

- `compose.yml`：独立 PostgreSQL、Redis、control Blue/Green、ADP Blue/Green、ClamAV、活动色切换器和 new-api identity 私有 TLS 代理；control 等待 PostgreSQL/Redis 健康后启动，ADP 等待扫描器健康后启动，两个颜色均运行带数据库认领保护的 worker。
- `docker/Dockerfile.claw-control`：通过国内镜像分别构建 Go control 与 admin SPA，并只把二进制和 `dist` 静态产物复制进只读运行镜像；`/workbench/admin` 由 control 从 `/app/admin-ui` 提供。
- `caddy/Caddyfile.public.snippet`：需要人工导入现有 HTTPS site、并放在 new-api catch-all 前；明确拒绝 `/api/internal/workbench/*`，把 entry/config/plan 以及 `GET /api/workbench/selections`、`POST /api/workbench/selections/choose` 精确转发到 control，并为这些路由保留 `claw_control_session`。管理 API 会删除 `Authorization`/`X-Claw-Actor`，只允许短期 admin session + CSRF 流程，不允许公网 bootstrap token。`/workbench/admin` 静态路由必须位于通用 ADP `/workbench/*` 路由之前并直接代理 control，不使用 `forward_auth`；管理数据 API 仍由 control 的短期 admin session + CSRF 保护。SSO 路由只向 ADP 保留一次性 `claw_sso_binding` Cookie，普通 ADP 路由剥离全部 `claw_*` Cookie。
- `new-api.env.example`：必须同时配置到 new-api Blue/Green 的正式契约变量，并挂载内部 CA 验证 control TLS。
- `scripts/preflight.sh`：检查配置、密钥长度/复用、证书 SAN、edge TLS 别名、new-api HTTPS/CA 契约、admin 静态路由顺序与无 `forward_auth`、admin 产物构建路径、control 两色渲染配置一致性，并用选定 Caddy 镜像验证 public snippet、identity proxy 和活动切换配置。
- `scripts/deploy-color.sh`、`switch-active.sh`：先部署不接流量颜色，再经 Caddy 原子 reload 切色。
- `scripts/rollback.sh`：在 new-api 功能开关关闭后，禁用工作台网关但保留数据。
- `scripts/backup.sh`、`restore.sh`：备份/恢复两套 PostgreSQL；Redis RDB 仅作故障取证，恢复时主动失效所有旧会话和一次性票据。
- `SECURE_FILES.md`：文件上传、恶意内容扫描、隔离区容量、私有 COS、失败补偿与验收用例的独立安全说明。
- `observability/METRICS.md`：内部 Prometheus 端点、低基数指标公式、抓取示例与告警规则；示例不自动部署 Prometheus，9090/9100 也不会发布到宿主机。

## 首次准备

1. 在服务器安装 Docker Engine、Buildx/BuildKit、Docker Compose >=2.17 和 OpenSSL；创建或确认现有 edge 网络，例如 `new-api-production`。
2. 复制 `.env.example` 为 `.env`，把 `NEW_API_INTERNAL_UPSTREAM` 的占位值替换成 edge 网络内可解析的 new-api 活动 upstream，并把允许的精确 Docker/private-network host 写入 `NEW_API_INTERNAL_ALLOWED_HOSTS`；localhost、任意未列入 host 和公网地址都会被拒绝。`COMPOSE_PROJECT_NAME` 固定为 `claw-workbench`，以保证 DR 保护卷路径可验证。生产必须由 CI 构建并扫描 claw-control/ADP 两色镜像，分别填入四个不同的 registry digest，保持 `CLAW_BUILD_LOCAL_IMAGES=false`；目标服务器只 pull，不现场 build。未接流量颜色保留上一 digest。`CLAMAV_IMAGE` 同样必须固定到已扫描的 digest。
3. 在 `secrets/` 创建下列文件。`secrets/` 及 `provider/oauth/billing/sandbox` 子目录都必须是非 symlink 目录，由 root 或 UID 10001 持有并设为 `0700`。所有文件必须是非 symlink 普通文件；除 `.crt` 证书外，供应用读取的顶层 Secret 统一执行 `chown 10001:10001`、`chmod 0600`。两份 TLS private key 可由 root 或 UID 10001 持有且必须 `0600`；`.crt` 证书可用 `0644`，但不可被 group/world 写入。preflight 会逐项验证类型、owner、mode 和 64 KiB 上限：

   - `control_db_password`、`adp_db_password`、`redis_password`
   - `claw_admin_token`
   - `new_api_control_hmac`：new-api -> claw-control v2
   - `adp_control_hmac`：ADP -> claw-control v2
   - `new_api_identity_hmac`：claw-control -> new-api identity-status v1
   - `adp_session_secret`
   - `adp_usage_evidence_key`：ADP Turn 用量原始证据的独立 32 字节加密密钥（标准 base64）
   - `adp_connector_token_key`、`adp_oauth_state_key`：OAuth Token 与一次性 PKCE state 各自独立的 32 字节标准 base64 密钥
   - `adp_connector_token_previous_keys.json`、`adp_oauth_state_previous_keys.json`：对应历史 key-id 到 32 字节 base64 密钥的 JSON；初始均写 `{}`
   - `files/cos_secret_id`、`files/cos_secret_key`：仅在启用文件能力时需要；限私有 COS 桶最小权限，不复用客户 ADP App 凭据
   - `internal_ca.crt`
   - `workbench_control.crt/key`，SAN 必须含 `workbench-control.internal`
   - `new_api_identity.crt/key`，SAN 必须含 `new-api-identity.internal`

4. 创建 `secrets/provider/`。每个文件名必须是以 `WORKBENCH_PROVIDER_` 开头的合法大写环境变量名，内容是单个 provider Secret。claw-control 的 credential profile 只保存 `env://VARIABLE_NAME`，例如 `env://WORKBENCH_PROVIDER_CUSTOMER_ZHANGYUE_ADP_APP_KEY`；ADP 浏览器和容器环境不直接持有整套客户 Secret。claw-control 容器以 UID/GID `10001:10001` 运行，因此这些 bind-mounted 文件必须在宿主机执行 `chown 10001:10001` 并设为 `0400` 或 `0600`；preflight 会拒绝错误 owner 及任何 group/world 权限，不能通过改成 `0644` 绕过。

   管理 API 要求提交与服务端完全一致的 canonical fingerprint。不要把 Secret 复制到
   浏览器或在线哈希工具；在服务器目录内只传文件名运行下列脚本，它只输出 fingerprint：

   ```sh
   python3 scripts/provider_fingerprint.py credential WORKBENCH_PROVIDER_CUSTOMER_SECRET_ID WORKBENCH_PROVIDER_CUSTOMER_SECRET_KEY
   python3 scripts/provider_fingerprint.py app-key WORKBENCH_PROVIDER_CUSTOMER_ADP_APP_KEY
   ```
5. 分别复制 `config/integration-allowlist.json.example` 和 `config/oauth-providers.json.example` 为去掉 `.example` 的生产文件。Provider JSON 只能保存 HTTPS endpoint、client id、允许 scope/host 和不透明 `client_secret_ref`；真正 client secret 放在 `secrets/oauth/<ref>`，必须是 UID/GID `10001:10001` 拥有的直接子普通文件且权限 `0400` 或 `0600`。禁止在 JSON、`.env`、浏览器或日志中写 client secret。
6. 将 `new-api.env.example` 中的配置以生产 Secret 注入方式同时加入 Blue/Green。两个 HMAC 值分别与同名 secret 文件保持一致，但不能复用 new-api JWT、Session 或 API Key。将 `internal_ca.crt` 作为只读 Docker secret/bind mount 同时挂载到两个 new-api 容器的 `/run/secrets/internal_ca`，文件应由容器运行用户可读且不可写；`SSL_CERT_FILE` 必须指向该容器内路径。两个 new-api 容器还必须加入 `WORKBENCH_EDGE_NETWORK`，使用 `https://workbench-control.internal:8443` 并保持 `WORKBENCH_ALLOW_INSECURE_CONTROL_HTTP=false`，不能为了省略证书挂载退回明文内部 HTTP。

claw-control→new-api identity-status v1 的请求必须携带每请求新生成的 256-bit `X-Workbench-Nonce`，该 nonce 同时进入请求 HMAC、由 new-api 原样回显并进入响应 HMAC。这个只读幂等端点不使用 new-api 业务表保存 nonce；旧响应因无法匹配下一请求的随机 nonce 而被拒绝。该 canonical 变更要求 new-api Blue/Green 与 claw-control Blue/Green 同批部署并在切流前运行双向契约测试；混合旧/新实现会 fail-closed，不得临时关闭验签或由 Caddy 补写 nonce。

`WORKBENCH_STREAM_REAUTH_SECONDS` 默认 30 秒，决定 Chat 与文件解析长连接在上游静默期间重新核验 new-api 用户、客户、App、套餐和 `auth_epoch` 的最长间隔；不得大于业务可接受的撤销生效时间。

`adp-blue` 与 `adp-green` 分别固定使用唯一的 `WORKBENCH_INSTANCE_ID`。控制面的撤销/缓存失效事件同时写入 Redis Pub/Sub 与 Stream：Pub/Sub 用于低延迟，Stream cursor 用于实例重启后补漏。扩容到多个同色副本时，必须为每个并行实例设置不同且跨重启稳定的 ID，不能只复用颜色名；所有实例的 `WORKBENCH_CONTROL_EVENT_CHANNEL` 必须与 control 的 `CLAW_REDIS_EVENT_CHANNEL` 相同。

`CLAW_PROVIDER_VERIFICATION_TIMEOUT` 默认 15 秒，限制保存 App/credential profile 时 control 对腾讯 provider 的在线验证时长。该值必须覆盖正常网络抖动但保持有界；超时必须使本次配置变更失败，不能绕过验证或在生产启用 `CLAW_ALLOW_UNTRUSTED_PROVIDER_VERIFICATION`。

迁移 `0016_customer_membership_scope` 会在启动时把旧的 active `primary`/空成员槽位改为
`customer:<customer_id>`，把 disabled 旧槽位改为 `historical:<id>`，随后创建
`(new_api_user_id,membership_slot)` 复合唯一索引。若历史数据存在其他重复作用域，迁移会
fail-closed；必须先备份 control DB、人工核对并修复重复归属，不能删除唯一索引或跳过迁移。

迁移 `0017_usage_audit_revisions` 新增不可变成本修订链。原锁定记录仅变为
`superseded`，替代记录与新证据单独保存；毛利只汇总当前 `locked` 记录，既避免重复计入，
也不会覆盖历史证据。

迁移 `0015_provider_secret_fingerprints` 会把旧版本中由管理员任意填写的 fingerprint
明确标记为 legacy（version 0），不会静默信任或猜测。新颜色此时 `/healthz` 仍可用，
但 `/readyz` 会返回 503。先确认 `secrets/provider/` 已在 Blue/Green 安装完全相同、
版本正确的 Secret 并备份 control DB，再对未接流量的新颜色执行：

```sh
sh ./scripts/reenroll-provider-fingerprints.sh green RE-ENROLL-CURRENT-RUNTIME-SECRETS
```

该脚本只通过目标容器 loopback 使用 emergency bootstrap secret，Secret 不离开容器；
同一路径在公网 Caddy 边缘固定返回 404，普通 admin session 与来路
`X-Claw-Actor` 均不能调用或伪造此次维护身份；
服务端只为 version 0 的 active/retiring/staged credential 和 current/pending App config
按当前挂载内容生成 canonical SHA-256，并逐行审计。version 1 的任何 mismatch 都不会被
覆盖，必须走凭据轮换或新 App config。脚本成功后会再次检查 `/readyz`；失败时不得切色。

文件能力默认保持 `WORKBENCH_FILES_ENABLED=false`。只有完成 `SECURE_FILES.md` 中的 ClamAV、隔离区、私有 COS、EICAR、扫描器不可用和补偿删除验证后，才能在 Blue/Green 同时改为 `true`；任何扫描超时、未知结果或扫描器不可用都必须拒绝上传，不能降级为未扫描直传。

离线定时任务另由 `WORKBENCH_SCHEDULED_TASKS_ENABLED` 控制，默认关闭。

AppId 迁移 Agent 重建 worker 在受管生产拓扑中不是可选能力：
`WORKBENCH_APP_MIGRATION_WORKER_ENABLED=true` 必须同时进入 ADP Blue/Green。
两色都轮询签名 control `claim/report` API，但 PostgreSQL 行锁、一次性哈希 lease
和 attempt ID 保证每个 member 只有一个消费者。生产 preflight 会拒绝关闭该
开关，并验证 poll 为 1–300 秒、lease 为 10–120 秒。provider-unknown 任务只能
通过管理员 expected-version retry 进入 Describe-only 恢复，不会再次 CopyAgent。
启用前还必须给客户套餐加入 `scheduled_tasks` capability；仅打开进程开关不会
绕过套餐和完整 identity/customer/App/config 作用域。两色使用同一 ADP
PostgreSQL，由行租约、续租、幂等 run key 与版本化离线 delegation 协调，不能
改成各实例内存 Cron。`.env.example` 中的 reauth、worker interval、lease、batch、
最小执行间隔、每用户任务数、每日运行数、prompt/附件、delegation 生命周期和
misfire grace 均为硬上限，preflight 会按 ADP 启动配置的同一范围校验。启用前按
ADP fork `server/WORKBENCH_SCHEDULED_TASKS.md` 验证撤权、误触发、Blue/Green
抢占、崩溃恢复、上游已提交但终态未知且不得重提等场景。

OAuth/Skill 集成另由 `WORKBENCH_INTEGRATIONS_ENABLED` 控制，默认关闭。启用前必须：

1. 在腾讯 ADP App 打开 Claw 动态配置并发布；每个工作台用户已经拥有独立 Agent。
2. 填好两个只读 JSON 配置，确保 catalog 只包含该 App/config version 和套餐 capability 共同允许的资源。`config/*.reference.json` 提供完整脱敏结构，包含 App/config version、Skill、只读 Plugin/Tool、OAuth Connector、authorization/token/revoke URL、host/scope allowlist 和 `client_secret_ref`；只复制需要的条目，不得直接使用参考域名或占位 ID。
3. 为 OAuth Provider 在官方控制台登记精确回调地址 `https://<域名>/workbench/integrations/oauth/callback/<provider>`，并确认 Caddy 的通用 `/workbench/*` 路由可到达 ADP。
4. 生成两套互不复用的 JWE key ring；历史密钥只放 previous-key 文件，完成重加密或删除旧 Token/state 后才能移除。
5. 把 OAuth client secret 作为 `secrets/oauth/` 下的只读版本化文件同时安装到 Blue/Green，先逐色通过 preflight 和内网回归，再打开开关。

公开合同已覆盖 `SkillList`、`PluginList`、`ToolList` 以及 Plugin Summary/Detail 与 Agent 读回。当前可在独立开关下开放 allowlist 内无鉴权、平台 APIKey、CAM 且 `ToolAccessMode=1` 的只读资源；写入前后都校验完整集合，停用只设置 `IsDisabled=true`。使用者 OAuth、本地 Token 注入、写/删除工具、鉴权未完成和物理清空仍固定阻塞。完整边界见 ADP fork `server/WORKBENCH_INTEGRATIONS.md`。

腾讯 AGSX 托管沙箱另由 `WORKBENCH_SANDBOX_ENABLED` 和套餐/App 交集中的独立
`sandbox` capability 共同控制，默认关闭；它不复用通用 `tools` capability，因此
开放受控沙箱不会使普通 Chat/定时 Turn 触发尚未开放的通用工具执行。首版仅允许
`SANDBOX` 网络与 `TOKEN` 认证，提供有硬上限的非交互 Shell、文件读写和实例生命
周期。代码执行在一次性 Linux worker 内调用官方 `run_code` 并由进程级内存/CPU/
输出限制隔离；互动 PTY 通过同源 WebSocket、一次性 subprotocol 票据和服务端 E2B
PTY SDK 代理实现。二者各有默认 false 的独立开关，只有目标腾讯地域真实验收通过后
才允许打开。每个实例严格绑定
`customer + new-api user + App + conversation`，Blue/Green 使用各自稳定唯一的
`WORKBENCH_INSTANCE_ID`、同一个 PostgreSQL 和同一份 ClientToken HMAC 密钥完成租约
接管与幂等创建，不保存或向浏览器返回 provider token、直连 URL 或 CAM 凭据。

启用前在 `secrets/sandbox/` 安装四个只读文件：`agsx-api-key`（`ark_...`）、
`cam-secret-id`、`cam-secret-key`、`client-token-hmac-key`（独立随机值，至少 32
字节）。它们必须由容器 UID `10001` 所有，权限为 `0400` 或 `0600`，并在两色
完全一致；不得写入 `.env`、数据库、日志或 API。然后设置真实 region、严格匹配的
`<region>.tencentags.com`，并至少设置 ToolId 或 ToolName；若两者都设置则返回实例
必须同时匹配。控制端点保持
`ags.tencentcloudapi.com`。`preflight.sh` 会验证域名、模式、硬上限、目录穿越、
符号链接、文件权限、长度、控制字节与密钥格式。只有完成真实 CAM 最小权限、创建/
复用/暂停/恢复/停止、Shell/文件、六种语言代码执行、PTY input/resize/Ctrl-C/exit、
静默撤权、断连取消、输出洪泛、跨客户 IDOR、两色接管和额度耗尽
测试后才能打开开关。完整合同见 ADP fork `server/WORKBENCH_MANAGED_SANDBOX.md`。

### 运维证据存储

在首次部署前执行 `openssl rand -base64 32 > secrets/evidence_master_key`，将文件权限设为仅管理员可读，并确保它与所有现有 secret 独立。`scripts/preflight.sh` 会校验它可解码为恰好 32 字节、不可被组/其他用户读取，并检查 Blue/Green 共用同一个 `evidence_data` 持久卷。不要把该值写入 `.env`、日志、数据库或备份清单。

`CLAW_EVIDENCE_MAX_BYTES` 默认 10 MiB，必须在 1 到 100 MiB 之间；`CLAW_EVIDENCE_ROOT` 在容器内固定使用 `/var/lib/claw-control/evidence`。证据文件在写入共享卷前由 claw-control 完成扩展名、MIME、内容和大小边界校验，并通过内部 `workbench-clamav:3310` 的 `INSTREAM` 扫描；仅明确的 `OK` 可以继续，命中、超时、连接失败、扫描上限或未知结果全部 fail-closed 且不落盘。MIME/签名检查不能替代恶意文件扫描，未接通并验证 ClamAV 时开放证据上传属于上线阻断项。扫描通过后才使用独立主密钥进行 AES-256-GCM 加密。管理员上传需要 session 与 CSRF，授权下载需要 admin session，所有响应均禁止缓存。

腾讯费用中心自动导入默认关闭。启用前在 `secrets/billing/` 创建
`tencent_secret_id` 与 `tencent_secret_key`，将 owner 设置为容器 UID
`10001` 且权限设为 `0400` 或 `0600`；在 `.env` 中只设置非密钥项
`CLAW_TENCENT_BILLING_IMPORT_ENABLED=true` 与真实
`CLAW_TENCENT_BILLING_PAYER_UIN`。SecretId/SecretKey 由 control entrypoint
读取为进程环境后立即交给固定 `billing.tencentcloudapi.com` 的 TC3 客户端，
不写入 `.env`、数据库、日志或 API 响应。Blue/Green 共用同一数据库、证据卷
和只读凭据目录，数据库的任务 lease 与账号级 provider coordinator lease 保证
同一 ImportRun 不会重复处理，并保证两个颜色不会把账号级 5 QPS 放大为 10 QPS。

导入只会生成 account-scoped、`unverified` 的用量核查草稿和加密原始证据；
不会自动按 Customer/App 分摊，也不会修改发票、余额或 new-api quota。上线前先
在腾讯费用中心确认 ADP 实际 `BusinessCode`，并为凭据授予只读账单查询的
最小权限。`DescribeBillDetail` 单月超过 20 万条时，应按腾讯官方建议改用账单
COS 存储并继续走人工证据流程，不得提高本实现的 20 万条硬上限绕过保护。

数据库中的 `claw_evidence_objects` 与 `evidence_data` 必须作为同一个恢复点备份；独立主密钥通过密钥管理系统另行备份。当前通用 `backup.sh` 只导出数据库和 Redis 取证快照，因此在把证据功能用于生产前，必须为 `evidence_data` 配置同一恢复点的卷快照（或停写后的加密归档），并完成“数据库 + 证据卷 + 密钥恢复”的演练。缺少任何一项都不得把备份标记为可恢复。

### Turn 用量原始证据

ADP 另用 `secrets/adp_usage_evidence_key` 加密每个 Turn 的原始 `response.completed` JSON；它与 claw-control 的 `evidence_master_key` 完全独立。`entrypoint-adp.sh` 将其只注入为 `WORKBENCH_USAGE_EVIDENCE_KEY`，数据库同时保存非敏感 `WORKBENCH_USAGE_EVIDENCE_KEY_ID`、明文 SHA-256 和逐 JSON path 的非累加用量投影。密钥缺失、格式不是标准 base64 的 32 字节、落库失败或没有完成事件都会阻止 Turn 标为完成。该密钥不进入通用备份；必须在独立密钥管理系统中按 key id 归档并演练旧证据解密。不要把它复用为 Session、HMAC、provider 或人工证据主密钥，也不要在日志或 `.env` 中保存。

部署并启用后，new-api 超级管理员从同源路径 `/workbench-admin` 进入管理控制面；该入口先向 new-api 申请一次性 admin ticket，再跳转 `/api/workbench/entry` 建立短期管理会话。不要直接向浏览器分发 `CLAW_ADMIN_TOKEN`，该 token 只保留给私网紧急 bootstrap。

示例生成命令（只在安全终端执行，输出直接写入受限文件，不要复制到 Git/聊天/日志）：

```sh
umask 077
openssl rand -base64 48 > secrets/new_api_control_hmac
openssl rand -base64 48 > secrets/adp_control_hmac
openssl rand -base64 48 > secrets/new_api_identity_hmac
openssl rand -base64 48 > secrets/adp_session_secret
openssl rand -base64 48 > secrets/claw_admin_token
openssl rand -base64 32 > secrets/evidence_master_key
openssl rand -base64 32 > secrets/adp_usage_evidence_key
openssl rand -base64 32 > secrets/adp_connector_token_key
openssl rand -base64 32 > secrets/adp_oauth_state_key
openssl rand -base64 48 > secrets/sandbox/client-token-hmac-key
printf '{}\n' > secrets/adp_connector_token_previous_keys.json
printf '{}\n' > secrets/adp_oauth_state_previous_keys.json
```

内部 CA 和证书应由现有 PKI 签发；不要在生产服务器临时创建长期自签根证书。证书只用于服务 TLS，HMAC 仍承担应用层请求/响应完整性和服务身份验证。

## 发布顺序

```sh
cd deploy/claw-workbench
sh ./scripts/preflight.sh
sh ./scripts/deploy-color.sh blue
```

此时没有公网入口。完成 identity、ticket replay、跨客户 IDOR、App/plan gate、SSE、文件和现有模型/素材路由回归后：

1. 把 `caddy/Caddyfile.public.snippet` 导入现有生产 HTTPS site，位置必须在现有 catch-all 前；先执行生产 Caddy 的 `validate`，再 graceful reload。
2. 真实 Cookie 组合回归：确认多 membership 入口跳转 `/playground/select`，两个 selection API 只接收 `claw_control_session`，浏览器只提交 opaque `selection_token`，成功后再取得 SSO 跳转；确认 `/workbench/auth/sso` 转发给 ADP 的 Cookie 头只含 `claw_sso_binding`，不含 control/admin/CSRF Cookie；确认其他 ADP 路由剥离全部 `claw_*` Cookie但保留 ADP `token`；确认 SSO 成功和失败响应都会以 `Path=/workbench/auth/sso; Max-Age=0` 清理 binding。另需确认 `/api/workbench/entry?ticket=...` 和整个工作台 surface 被 `log_skip` 排除、SSE 首事件立即 flush。
3. 给 new-api Blue/Green 配同一组工作台环境变量并重启，保持 `WORKBENCH_ENABLED=false`。
4. 两边分别验证 `/playground/legacy`，最后同时把 `WORKBENCH_ENABLED=true`；`/playground` 才开始签发工作台票据。

下一颜色发布：

```sh
sh ./scripts/deploy-color.sh green
# 对 green 走内网 E2E 后才执行：
sh ./scripts/switch-active.sh green
```

切色器的 Caddy 配置在 `state/` 内生成并由 `caddy validate` 后 reload；不会修改现有生产 Caddy 文件。control Blue/Green 使用相同 worker 配置：周期结算和 outbox 通过数据库行锁、事务与幂等写入串行认领，维护清理只删除超过保留期的数据。因此切色不会改变 worker 所有权，也不依赖“仅 Blue 执行”的脆弱约定；发布前仍必须完成双实例抢占、实例崩溃和 Redis 短暂不可用故障测试。

## 功能开关回滚

回滚顺序不能颠倒：

1. 同时把 new-api Blue/Green 的 `WORKBENCH_ENABLED=false` 并确认 `/playground` 回退原 Playground、`/playground/legacy` 可访问。
2. 执行 `sh ./scripts/rollback.sh --confirm-new-api-disabled`，切换器对 control/ADP 全部返回 404；数据库和私有容器保留，便于排查。
3. 若需要完全停机，再执行 `sh ./scripts/compose.sh down`。不要附加 `-v`，否则会删除数据库卷。
4. 恢复时部署并验证目标色，执行 `switch-active.sh blue|green`，最后才重新打开 new-api 功能开关。

## 备份与恢复

```sh
sh ./scripts/backup.sh
sh ./scripts/restore.sh ./backups/20260809T120000Z RESTORE
```

备份使用 PostgreSQL custom format、SHA-256 清单和最小镜像/revision 元数据，不包含任何 Secret。备份目录本身未加密，必须立即复制到加密、异机、受访问控制并有保留策略的存储。至少每月做一次隔离环境恢复演练。

恢复会先停止全部写入者和活动切换器；任何失败都会保持入口停止。恢复两套数据库后会主动撤销 control PostgreSQL 中尚未过期的 session/ticket hash，并对 Redis 执行 `FLUSHALL`，不会复活旧 control session、SSO ticket 或权限缓存。`redis-forensics.rdb` 仅用于事故分析，因为业务真值、outbox 和 Turn 状态应由 PostgreSQL 重建。

## 上线门槛和已知约束

- `Caddyfile.public.snippet` 依赖 Caddy 2.8+ 的 `log_skip`；Cookie 正则删除、`forward_auth`、查询票据不落 access log 和 SSE `flush_interval -1` 必须在服务器实际 Caddy 版本上用长连接和多 Cookie 组合验证。
- edge、switch、identity proxy 不记录请求头或 query；App 日志同样不得记录 HMAC header、Cookie、ticket、AppKey、AK/SK。排障使用脱敏的 `request_id`/`turn_id`。
- `/api/internal/workbench/**` 只允许 backend 网络直连；公网 matcher 明确返回 404。`/api/admin/workbench/**` 虽经 edge 转发，但 edge 会删除 bootstrap Bearer/actor，只允许 new-api admin entry 换取的短期 cookie 和 CSRF 流程。
- `/workbench/admin` 只公开 SPA shell/静态资源，所以不能套用依赖既有 admin cookie 的 `forward_auth`；它必须先于通用 `/workbench/*` ADP 路由命中 control。真正的管理数据仍只从 `/api/admin/workbench/**` 读取，并由 control 校验 admin session 和 CSRF。
- new-api Blue/Green 必须同时挂载相同内部 CA、加入同一 edge 网络并使用完全相同的 workbench control HTTPS 配置；任何 `http://` URL 或 `WORKBENCH_ALLOW_INSECURE_CONTROL_HTTP=true` 都是上线阻断项。
- control Blue/Green 共用独立 control DB；ADP Blue/Green 共用独立 ADP DB。不得把任一 DSN 指向 new-api DB。
- Redis 密码只能放在 `secrets/redis_password`，由 control/ADP 入口脚本从 Docker secret 读取；不得写进 `.env`、Compose 环境值或镜像层。Redis 不可用时 control 不通过依赖健康检查，outbox 保留在 PostgreSQL 等待重试。
- `WORKBENCH_ENABLE_HELPER_ASR=false` 是生产固定约束：不开放 ADP helper ASR，避免绕过客户 App、套餐、审计与文件安全边界；需要语音能力时必须先走独立威胁建模、计费和路由评审。
- `APP_CONFIGS=[]` 只保留 bootstrap 兼容；客户 App/Space/Agent/Secret 由 claw-control 动态解析并覆盖客户端字段。
- 所有客户 AppKey/ADP AK/SK 只进 `secrets/provider/` 和 control 进程；不能进 Git、Compose、镜像层、浏览器响应、审计 diff 或日志。ADP 只额外挂载与客户 App 凭据隔离的最小权限 COS 存储凭据。
- OAuth catalog/provider 元数据只进 `config/*.json`，OAuth client secret 只进 `secrets/oauth/`；OAuth Token/state 只以独立 JWE key ring 加密落 ADP PostgreSQL。任何 key、Token、client secret、provider 直连地址都不得进入浏览器或普通日志。
- 工作台上传文件只能经过隔离、ClamAV 扫描和私有 COS 管道；浏览器、历史记录和审计只保存不透明文件 ID，不返回 COS locator 或长期下载 URL。详细约束见 [`SECURE_FILES.md`](./SECURE_FILES.md)。
- 现阶段 fixed monthly `offline_manual` 不逐 Turn 扣款。没有获批 ADR 和稳定幂等固定扣款接口时，不启用 `wallet_monthly`。
- 生产开放仍以设计文档第 19、20 节测试全部通过为准；overlay 本身不等于业务功能已经验收。

## 仍需部署者提供

- 现有生产 edge Docker network 名称、new-api Blue/Green 内网服务名和端口。
- canonical HTTPS origin，以及生产 Caddy 主配置的 import 位置和实际版本。
- 由内部 PKI 签发的 CA、两个内部服务证书与私钥。
- 三个互不相同的 HMAC Secret、admin/session Secret、三套数据库/Redis密码。
- ADP fork 的不可变 commit/tag 或已扫描的镜像 digest。
- 每客户腾讯 AppId/AppKey/SpaceId/模板 Agent、AK/SK 的 `env://` 引用名；私有 COS、扫描器和 OAuth 配置。
- 初始 customer/member/固定套餐金额、周期、限制和人工付款证据。
