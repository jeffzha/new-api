# 代理商正式发布前置修复与演练记录

日期：2026-09-13，维护后复核于 2026-09-14 完成（Asia/Shanghai）。目标：修复会话限流串扰、补齐生产 SSO 密钥配置、验证原 PostgreSQL 的增量升级。

## 邀请注册开通（2026-09-14 最新状态）

用户将本轮范围明确收敛为：先开放邀请注册，佣金处理与提现继续关闭并排查。北京时间 **2026-09-14 00:33** 已完成：

- Blue、Green 和 Agency Hub 的 `AGENCY_ONBOARDING_ENABLED` 均为 `true`，三个应用容器已逐个重建并通过健康检查；镜像保持 `f94e09465002` 对应版本。
- Hub 的 `AGENCY_HUB_COMMISSION_PROCESSING_ENABLED=false`、`AGENCY_HUB_WITHDRAWALS_ENABLED=false` 保持不变；两套主站 `AGENCY_COMPONENT_BILLING_ENABLED=false` 保持不变。
- 合并 Compose 前后做完整结构比较，唯一配置变化为三个服务的 onboarding 值。更新前确认没有待执行的旧用户 provisioning job。
- 公网 `/agency/readyz` 返回 `onboarding=true`、`commission_worker=false`、`withdrawals=false`；指定邀请预览接口返回 `success=true`、`can_register=true`。
- 邀请注册控制器专项回归通过：`go test ./controller -run '^(TestAgencyInviteRegistration|TestAgencyOnboardingPause)' -count=1`，测试文件及缓存均在 E 盘。没有为验证在生产创建测试账号，也没有进行真实充值或付款。
- PostgreSQL 容器 ID、数据卷和 Caddy 文件指纹保持不变；没有重建数据库、重跑 Agency 迁移、清理数据或改变流量权重。

开通前新增完整数据库备份和原配置备份：

```text
/opt/new-api/deploy/backups/agency-onboarding-20260914-nb8vnkjs
newapi.dump: 10,205,833 bytes
SHA256: b4e18eefd34e5e4fceb46322b011efbc5b5430410770d3f6b73bb31dd4f699b3
```

备份目录私有，包含配置中的认证信息，不应公开。`pg_restore --list` 已验证；结果保存在该目录的 `onboarding-verification.json`。

开通检查时，定时当前状态对账已产生 **8 条未解决记录**（1 条 funding_account、3 条 billing_operation、4 条 billing_outbox），另有 4 条 pending delivery。邀请注册开通不代表这些异常已修复，也不代表佣金或提现可放行；未修改账面余额、重写历史事件或关闭异常。后文“三开关关闭”描述的是此前维护完成时状态，应以本节最新值为准。

## 正式执行结果（2026-09-13 维护完成时）

用户已明确授权短时维护。三项修复已于北京时间 **2026-09-13 23:49** 正式生效；后文保留的准备、演练记录不代表当前仍待部署。

| 项目 | 正式执行结果 | 验收边界 |
| --- | --- | --- |
| 登录限流 | 新版本已运行；刷新、退出、代理身份验证与登录密码尝试分开计数 | 内存及 Miniredis 的真实路由回归通过；生产同源无凭证刷新返回预期 401、退出返回 200、非可信 Origin 返回 403；未向生产连续提交错误密码或清空原限流记录 |
| SSO | Blue、Green 已重建，既有签名密钥及公钥只读挂载实际生效；容器 UID 10001 可读，密钥未重新生成 | SSO 起始 Cookie、主站桥接页和 Origin 限制通过；无效票据被 Hub 以 `invalid_ticket` 拒绝，不再是密钥未配置错误；真实 Root 浏览器签票及登录仍需用户确认 |
| 数据库 | 停止旧写入器后完成新备份、核心前置列增量升级、`agency-hub migrate` 和主站正常迁移 | PostgreSQL 原容器与原数据卷保留；Hub readiness 为 true，缺表、缺列、缺索引列表均为空；没有新建空库、清库或覆盖余额 |

### 发布版本及流量

Blue、Green、Agency Hub 三个运行容器使用相同镜像及 image ID，健康状态均为 `healthy`，维护后复核时重启计数均为 0：

```text
image: new-api-seedance:v1.0.0-rc.22.gateway.20260913T152836Z.gf94e09465002
image ID: sha256:1649b94e16c12aa94052be456acdc96aea17becfa773a9c001704e5bedaf880f
source commit: f94e09465002
```

限流修复提交为 `ce49e4eda4879a0f9383a9d142fcfe1d4decfb6c`。构建时发现 Agency 前端 lockfile v2 与原 Bun 1.3.11 不兼容，首次构建在替换生产容器之前失败。后续提交 `f94e09465002` 将三个 Dockerfile 的 **Agency 前端构建阶段** 固定为官方 Bun 1.4.2，保留 `--frozen-lockfile` 和主站前端原有 Bun 版本，没有修改锁文件；新镜像随后完整构建成功。

Bun 1.4.2 固定摘要：`sha256:9114c058aeae42162ee16dd5084b95fe9473970bb6bcb5b232ab1630f0546895`。

公网仍为 **Blue 100% / Green 0%**，但两个槽位均已更新，无需再执行切流命令。Caddy 配置未改动，前后 SHA256 均为：

```text
c5b47272678546287236e5c682039d67c76cd4ba9331d82e8b8dd96922035c99
```

网关发布清单 `releases/gateway-blue.json` 和 `releases/gateway-green.json` 已同步为实际运行版本。主站原 `SESSION_SECRET` 保持不变；未修改管理员密码。

### 本次维护备份（最新恢复参考点）

本次实际停写后的备份不同于后文较早的演练备份，应优先以此目录追溯本次变更：

```text
/opt/new-api/deploy/backups/agency-maintenance-20260913-Dx3y5N
```

其中包括原 Compose 三文件、Caddyfile、私有 `.env`、密钥归档、私有容器快照、`newapi.dump`、`restore-list.txt`、校验和、迁移日志与生产验证 JSON。`pg_restore --list` 和校验和验证通过。数据库文件为 **10,172,658 bytes**，SHA256：

```text
9fdb9962dfb3adf2edecdbba8432ef65f5567a7b38eaa3fcdf5cfab28d266ecb
```

数据库异机副本已下载至：

```text
E:/new-api-test-cache/agency-release-prep/backup-maintenance-20260913-Dx3y5N/newapi.dump
```

下载后 SHA256 与服务器一致，目录仅当前 Windows 用户和 SYSTEM 可访问。备份含业务数据和认证材料，不要提交到 Git 或公开发送。

原 PostgreSQL 容器 `new-api-seedance-postgres-1` 创建时间仍为 `2026-09-01T03:59:29.458644817Z`，数据库仍为 `newapi`，挂载卷仍为 `new-api-seedance_postgres_data`。没有执行 `down -v` 或 `--remove-orphans`。

### 实际执行顺序与验证

1. 在生产旧服务在线期间完成镜像构建；用确切发布镜像对隔离恢复库启动主站 master，验证正常核心迁移及版本接口。
2. 获取蓝绿发布锁，核对配置、数据卷和 Caddy 指纹，准备三个服务的同版本配置。
3. 停止旧 Hub 和两套旧主站写入器，制作本次停写备份。
4. 执行 `postgres-core-prerequisites.sql`，仅增量添加缺失核心列；然后以 `/agency-hub` 为入口执行 `migrate`。
5. 启动新 Blue master，完成其余核心迁移并确认内部 HTTP 恢复，再启动新 Green 和 Hub。
6. 核对镜像、健康状态、实际挂载、公开页面和静态资源、认证拒绝路径，以及原数据库和 Caddy 未变。

记录时间均为 UTC：维护开始 `2026-09-13T15:45:54Z`；Blue 内部 HTTP 恢复 `2026-09-13T15:49:02Z`；三个服务恢复完成 `2026-09-13T15:49:09Z`。未单独测量公网精确停机时间，不把内部探针恢复时间等同于所有客户端恢复时间。

执行期间，Compose 一次性迁移命令消费了 SSH 脚本标准输入，导致迁移成功后后续启动步骤未继续执行；确认迁移完成后，通过恢复脚本启动服务。相关 `compose run/up` 已改用 `< /dev/null`，避免相同输入问题；**不要为此重新执行完整维护脚本**。

以下公网检查已通过：

- `GET /api/status`：200，返回准确发布版本。
- `GET /agency/readyz`：200，`ready=true`，schema 缺失列表全部为空。
- 主站 `/`、代理商 `/agency/` 及各自抽查的 JavaScript 静态资源：200，资源类型正确。
- 同源无凭证刷新：预期 401；无凭证退出：200；非可信 Origin 刷新：403 `AUTH_ORIGIN_FORBIDDEN`。
- `/agency/sso/start`：200，设置 state/nonce Cookie；携对应 state 提交无效票据：401 `invalid_ticket`。
- 可信 Origin 的主站 SSO 桥接页：200、`no-store`、正确 `frame-ancestors`；非可信 Origin：403；未登录签票请求：401。

证据保存于最新备份目录的 `production-verification.json`；其中明确记录 `authenticated_root_sso_tested=false`。维护后日志检查未发现 panic、启动迁移失败、缺表缺列、密钥加载或数据库连接错误。

### 当前未开放的功能与真实登录验收

本次先开放邀请注册，不等同于代理业务所有能力已开放：

- 两套主站：`AGENCY_ONBOARDING_ENABLED=true`、`AGENCY_COMPONENT_BILLING_ENABLED=false`。
- Hub：邀请开通已允许，佣金消费者和提现仍关闭，`AGENCY_HUB_AUTO_MIGRATE=false`。
- 生产原本已有 1 名 `agency-durable-v1` 用户；关闭开通开关不会取消其持久化计费模式，不得据此声称“所有代理计费已关闭”。
- readiness 检查时有 4 条待处理 delivery，佣金消费者关闭，未手动重放或处理。
- 日志包含现有的历史日结对账不支持提示：历史截止时点需要不可变快照，不能用当前状态检查冒充历史对账完成。此项未在本次修复，不纳入通过范围。
- 未执行付费模型调用、新代理开户、佣金发放或提现验收。

真实 Root 登录由用户在浏览器确认：

1. 在同一浏览器打开 `https://gateway.nexus-reach.com/`，使用现有正式环境超级管理员（Root）账号登录。
2. 打开 `https://gateway.nexus-reach.com/agency/`，点击 **“使用平台登录状态继续”**。
3. 确认进入超级管理员页面。下方账号密码表单用于独立代理商操作员，不能用它验证主站 Root 密码。

必须是启用中的 Root（role 100），普通管理员（role 10）或 API Token 不满足此入口条件。若浏览器原本已登录代理商操作员，先退出代理商中心再使用平台登录按钮；主站旧会话无效时重新登录原账号即可，不需要重置密码。正式开放新代理归属、佣金处理或提现前，还需另外授权并完成相应小规模业务验收。

## 准备阶段完成边界（维护前历史记录）

本节及后续演练细节描述维护授权前状态；“尚未生效”不适用于上文已完成的正式发布。

| 项目 | 已完成 | 尚未生效的部分 |
| --- | --- | --- |
| 登录限流 | 源码修复；内存和 Redis 路由回归通过 | 需提交并发布新主站版本；没有替换当前正式主站镜像 |
| SSO | 校验原 Ed25519 密钥对匹配；正式 Compose 基础服务补充两份只读挂载，Green 继承后也具备挂载；应用 UID 10001 读取测试通过 | 现有主站容器未重建，旧运行容器不会自动获得新增挂载 |
| 数据库 | 完整备份和异机副本；隔离恢复、连续两次迁移、原数据指纹与新 Hub readiness 验证通过 | 正式数据库未执行本次 DDL，仍需维护窗口停旧 Hub 写入器并协调新 Hub 上线 |

未切换公网流量，未开启新客户代理归属、佣金消费者或提现；未修改主站管理员密码。没有把准备和演练描述为生产功能已上线。

## 限流行为

- 登录、注册、2FA 等原 CT 密码尝试额度保持不变。
- `POST /api/user/auth/refresh` 和 `POST /api/user/auth/logout` 改用各自独立的 IP 桶，默认各 120 次/60 秒。
- 新配置为 `AUTH_SESSION_RATE_LIMIT` 和 `AUTH_SESSION_RATE_LIMIT_DURATION`；非正数回退默认值，时间窗口不超过现有内存桶保留期。
- 代理商 SSO 签票在 Root 鉴权后按用户限流，不再消耗 IP 的登录额度。
- 代理商 verify 与 command-proof 都验证 Root 密码，因此共用一个用户级验证额度，避免交替调用或换 IP 增加猜测次数。
- 不清空 Redis 现有 CT 记录；原限流状态按 TTL 到期。此次修改不重置任何密码或登录会话。

回归使用真实路由、隔离 SQLite、有效 JWT 和刷新 Cookie；覆盖内存/Miniredis、Origin 拒绝、刷新轮换、退出撤销、各桶隔离，以及不同 Root 与不同 IP 的边界。

## 正式配置修改

服务器：`47.97.97.89`；Compose 项目：`new-api-seedance`。

`/opt/new-api/deploy/compose.yml` 的 `new-api` 服务增加：

```yaml
volumes:
  - /opt/new-api/deploy/agency-secrets/agency_sso_private.pem:/run/secrets/agency_sso_private_key:ro
  - /opt/new-api/deploy/agency-secrets/agency_sso_public.pem:/run/secrets/agency_sso_public_key:ro
```

这是对原 `volumes` 列表的追加，不是替换原日志、数据、Workbench 密钥挂载。完整合并链为基础配置、Blue 覆盖文件和 Green 覆盖文件；已经验证两服务的有效配置均包含上述挂载。既有 SSO 环境变量不变。

原 SSO 私钥、临时密码交付加密密钥、提现加密密钥原为 `root:root 0600`，已调整为 `root:10001 0640`，以便实际运行用户读取。密钥内容未修改，未重新生成或轮换。公钥不变。

读取探针使用原主站镜像、UID/GID `10001:10001`、`--network none`、只读根文件系统和只读 bind mount；仅检查文件可读，没有请求生产 SSO 接口或创建登录会话。

## 备份与恢复演练

服务器备份目录：

```text
/opt/new-api/deploy/backups/agency-prereq-20260913-LxEPR3
```

包含原 Compose 三文件、Caddyfile、私有 `.env`、代理商密钥归档、私有容器配置快照、PostgreSQL custom-format dump 和 SHA256。目录由受限 `umask 077` 创建；其中包含秘密，不要上传到 Git、工单或公开聊天。

数据库备份为 10,157,303 bytes，SHA256：

```text
d409b7a371e5a4c5dccb7591bac0045a4f0154f999a4a586cfe87eda637ea88b
```

数据库另有本机 E 盘副本：`E:/new-api-test-cache/agency-release-prep/backup-20260913/newapi.dump`。该目录已移除继承权限，仅当前 Windows 用户和 SYSTEM 可访问；下载后的 SHA256 与服务器一致。

隔离容器 `agency-prereq-rehearsal-20260913` 使用当前生产 PostgreSQL 镜像的精确 image ID，`--network none`，无宿主端口，限制 512 MiB 和 1 CPU。只恢复到独立的 `agency_restore` 数据库和独立卷，未挂载生产数据卷。测试后容器已停止，保留恢复数据便于复核，没有删除生产或演练数据。

备份恢复出的快照包含 138 个用户、13 个渠道、24 个充值记录、1 个代理商；这是备份时点，不代表生产实时计数。

执行顺序：

1. `pg_restore --exit-on-error --no-owner --no-privileges` 恢复到隔离数据库。
2. 对原有 97 张表，保存原始列集合的行数和有序数据指纹。
3. 执行 PostgreSQL 专用的 nullable 核心列增量 SQL，再执行本次源码构建的 Linux `agency-hub migrate`。
4. 完整重复第 3 步，验证幂等。
5. 再次使用原列集合计算指纹：97 张表全部保持一致。新列回填不混入原字段比较。
6. 在同一隔离容器中以 UID 10001 启动新 Hub，保持开通/佣金/提现关闭，验证 `/agency/readyz`。

结果：两次迁移成功；Agency 表为 44 张；`missing_tables`、`missing_columns`、`missing_indexes` 均为空；`ready=true`。不把未开启的佣金处理及真实 SSO 登录计入这项 readiness 验收。

本次 Linux Hub 二进制 SHA256：`79f4521a92adf8e3ac31ce269daf509b62359ae08c2e34f032ce37cf183b2690`。这是演练构建，不是已部署的新正式镜像。

具体证据在服务器备份目录的 `rehearsal-result.json`、`rehearsal-ready.json`、两次迁移日志和前后指纹文件中。

## 正式执行前确认清单（历史记录，已按上文执行）

1. 安排维护窗口。主站新增挂载需重建容器；不能假定配置写入后已生效。新限流逻辑还必须随新镜像发布。
2. 正式执行前再做新备份，避免把早先演练快照当成最新回退点。
3. 暂停旧 Hub、独立 worker 和对账任务；新旧 Hub 写入器不能跨 schema 回填混跑。
4. 用演练过的核心字段 SQL和新版本 `agency-hub migrate` 执行增量升级。核心 SQL不替代主站其他认证、业务表的正常版本迁移。
5. 以新 Hub、原有持久密钥和正确挂载启动并验证 readiness；与所有主站槽位及后台执行节点兼容后才能开放新代理用户。
6. 有 durable 用户后，不得简单切回不支持该计费语义的旧镜像。禁止 `down -v`、`--remove-orphans`、重建数据库或直接覆盖真实余额。

准备阶段收尾检查：Blue、Green、旧 Hub、PostgreSQL 均保持健康，容器创建时间未变；Caddy SHA256 与备份前相同，Blue 100% / Green 0% 未改动。此处指正式维护前；维护后的容器替换结果以上文为准。
