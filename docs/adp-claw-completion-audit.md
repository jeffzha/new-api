# ADP Claw 智能工作台完成度审计

## 2026-08-11 生产增量审计（当前权威结论）

> 本节使用当前已提交、已推送的双仓库源码、GitHub Actions 发布清单、生产容器标签、生产数据库投影和真实 ADP 请求重新审计。本节更新了下文 2026-08-10 的历史快照；两者冲突时以本节为准。

### 已验证的生产范围

- new-api 源码与 claw-control 镜像绑定提交 `f6bead9c146b899e808b93eb29030fa4f231d6f4`；ADP fork 镜像绑定提交 `732f008d33d86b894a1bbee10abfbad10b54437b`。
- 发布工作流 `31418748011` 成功；new-api、claw-control、ADP Workbench 和固定 ClamAV 镜像均通过高危/严重已修复漏洞与 Secret 扫描，发布清单绑定精确提交和不可变 digest。
- 生产工作台当前流量在 Green：
  - claw-control `ghcr.io/jeffzha/claw-control@sha256:6c095045918650bde8368f72f9cb933b1fda02082d5dbe2205166fb63daa80f8`；
  - ADP Workbench `ghcr.io/jeffzha/adp-workbench@sha256:5f568fbd5aa8a8d1b38abbd19e1f8ef244c6f75b7da1fab797a7cf7322c47409`；
  - Blue 保留上一个健康不可变版本作为快速回滚目标，没有被同 digest 覆盖。
- 生产预检已验证内部 CA、两张服务证书、私有路由、Cookie/Header 剔除、SSE flush、Blue/Green 配置、Secret owner/mode 和容器健康。
- 真实生产链路在 `https://gateway.nexus-reach.com` 完成：登录 `200`、session-ticket `200`、entry `302`、entry replay `409`、ADP SSO `302`、SSO replay `409`、工作台页面 `200`、账号/应用查询 `200`、真实 `/workbench/chat/message` SSE `200`、终态 `completed`、历史查询 `200`。该 Turn 产生 13 个结构化事件，约 6.331 秒完成，模型精确返回验证词，历史保存 2 条记录。
- 按用户授权保留现有 ADP 凭据、不执行轮换后再次完成公网真实 Turn：SSE 返回 `200`，模型返回本次唯一验证词，持久化终态为 `completed`，共 17 个结构化事件；复核窗口内活动 Green 两个服务均为 `healthy`，无 HTTP 5xx、Traceback 或 panic。
- ADP 生产库当前可见 `active` identity/Agent，3 个 `completed` Turn，3 条 evidence、3 条 usage datum 和 45 条持久化事件；claw-control 生产库的 customer/member/identity/App/period 均为 `active`，App config 为 `verified`，plan 为 `published`，invoice 为 `paid`，outbox 均为 `delivered`。
- 备份链路不再读取可能滞后的 `.env` revision，而是在与切流/回滚互斥的锁内采集活动容器 OCI 标签和迁移状态。生产恢复点 `/opt/new-api/deploy/claw-workbench/backups/20260810T191011Z-final-verified` 已通过精确文件集与 SHA-256 校验；其发布清单绑定当前 Green、上述三个镜像 digest、control migration `0020_app_migration_lineage`、ADP schema fingerprint 和 `ap-guangzhou`，全部备份文件均限制为 owner-only。
- 安全边界重验：匿名签发票据返回 `401`，公网内部票据消费路由返回 `404`，未授权 Sandbox 返回 `403`。部署后无 5xx、Traceback 或 panic；日志中唯一新增 400 是验收过程中故意使用旧 `Prompt` 字段的预检请求，改用当前合同 `Contents` 后同一生产链路成功。

### 当前生产门禁

`chat` 是目前唯一已完成真实供应商验收并开放的执行能力。`files`、`scheduled_tasks`、`integrations`、`sandbox`、`sandbox_code` 和 `sandbox_pty` 仍必须保持 false；这些状态是未完成外部验收时的安全门禁，不是已产品化功能的证据。特别是：

1. AGSX 缺少 region、ToolId/ToolName、`ark_` API key、最小权限 CAM 和配额，因此不得开启生命周期、Shell、六语言 `run_code` 或 PTY。
2. 文件能力缺少私有 COS bucket/region/endpoint 和最小权限凭据，因此不得开启上传、下载、EICAR 或 retention 的真实对象存储路径。
3. 使用者 OAuth 缺少 provider metadata、client secret、已注册 callback 和测试账号；且公开 ADP 合同仍不允许把本地 OAuth token 安全关联到用户 Agent，不得猜测性注入。
4. Tool/Plugin/Connector 只完成目录、受控绑定、安全停用和 pre/post readback；依赖这些集成的 Turn 执行在真实只读非 OAuth Plugin/Tool 验收及供应商限额语义证明前继续 fail-closed。
5. 完整隔离、高可用与性能放行还需要同客户第二用户、另一客户/App/用户、独立 Prometheus 只读 observer 与签名身份、以及异地 DR 目标。

因此，当前精确结论是“固定套餐 Chat 白名单生产链路已验收；完整 P0+P1+P2 仍未达成”。不得用本节的 Chat 成功替代文件、OAuth、Tool/Plugin/Connector、定时任务、AGSX Sandbox、跨租户、负载和 DR 的独立验收。

> 审计日期：2026-08-10
> 需求基线：`docs/adp-claw-workbench-playground-replacement-design.md`
> new-api 工作树：`D:\codex\new-api-seedance-cn`，功能基线 `91f6b455dbd36858a7cff9d65b87e1d5a3fb4861`
> ADP fork 工作树：`D:\codex\adp-chat-client-claw`，功能基线 `186084bfddc42cc369c722cced95842dd83c305f`
> 本报告审计未提交工作树的本地实现，不等同于生产验收或发布批准。

## 1. 结论

本地代码已形成完整的三组件架构骨架：

- new-api 只保留登录身份真值、工作台入口、一次性票据发起和只读状态复核。
- 独立 `claw-control` 拥有 Customer、Membership、客户 App、凭据、固定套餐、账单、审批、审计、成本导入和治理 UI。
- ADP fork 拥有影子账号、Agent、Conversation、持久 Turn/SSE、Workspace、文件、定时任务、OAuth/Skill 和托管沙箱运行面。

在没有真实腾讯 ADP/AGSX、OAuth、COS/ClamAV、MySQL/PostgreSQL、生产域名、负载和灾备环境证据前，当前状态只能判定为：**本地实现基本闭环，生产验收仍受外部输入阻塞**。不得把 mock、单元测试或 fail-closed 占位描述为生产可用。

## 2. 强制架构约束

以下约束已经写入主设计、基线文档、双仓库检查脚本和 CI；它们不是可选建议：

1. new-api 只允许最薄身份/入口桥接，不承载 Claw 客户、App、套餐、Turn、文件或后台任务。
2. 禁止修改 new-api `relay/**`、渠道/模型映射、价格、quota、预扣费/结算/退款、API Key、用户生命周期和 `/v1/**` 模型接口。
3. new-api upstream 既有源码最多修改 10 个文件、100 行；至少 90% Claw 代码必须为新增文件或独立服务。
4. `/playground` 必须受功能开关控制，并永久保留 `/playground/legacy` 回退。
5. 三组件不共享业务数据库、ORM、数据库账号、浏览器 Cookie、API Key、JWT 私钥、HMAC Secret 或 Redis key 空间。
6. 跨组件只允许版本化最小 HTTP DTO、独立 HMAC/短期票据、重放防护、超时和 fail-closed。
7. 突破边界必须先提交包含影响、替代方案、冲突、迁移和回退的已批准 ADR；普通 CI 不接受豁免。
8. BYOK 首次配置例外只适用于尚无 current config 的 Primary App。附加 App 首次配置必须继承 Primary App 当前 `credential_profile_id`；引入另一套 AK/SK 必须走 `app_credential_change` 双人审批，AppId 迁移另行审批。
9. upstream 可持续同步属于发布条件：先形成纯 upstream 合并结果，再重放薄桥接；冲突不得扩散到用户生命周期、relay、渠道、计费、quota 或日志语义，也不得借冲突处理放宽 10/100/90 门禁。
10. 三条内部服务链路统一使用 `X-Workbench-Contract-Version: 1`，版本进入双向 HMAC canonical；缺失、降级或响应版本不一致均 fail-closed。claw-control→new-api identity-status 还要求每请求生成独立 256-bit `X-Workbench-Nonce`，nonce 同时进入请求/响应 canonical 且响应必须精确回显；该只读幂等端点不新增 nonce 业务表。后续升级至少保留当前/前一版本兼容窗口。
11. 事实源、标识镜像、执行快照和运行时 Secret 严格分离。ADP 门禁禁止控制面套餐/付款/发票实体和 App Secret 落库；claw-control 的资源镜像仅能保存 opaque identifier/parent/scope/version/status。

new-api 当前机械门禁结果：既有源码文件 `3/10`、直接修改 `76/100` 行、新增比例 `99.60%`、禁止目录零修改。最终发布仍需在确定提交 SHA 上重跑。

## 3. 本地功能实现矩阵

| 能力 | 本地状态 | 实现边界 |
|---|---|---|
| new-api 入口、身份状态复核、selector、legacy 回退 | 已实现 | 后端测试证明功能关闭时 session-ticket 返回 404；前端 `entry-fallback.test.ts` 证明仅 404 进入原 Playground，其他认证/服务错误保持 fail-closed；真实同域 Cookie/Caddy 尚待验收 |
| Customer、Membership、多客户/多 App selector | 已实现 | `0016` 迁移强制 `(new_api_user_id,membership_slot)` 唯一；浏览器只提交一次性 opaque selection token，不提交可信客户/App ID |
| App draft/verify/enable/suspend/disable/migration | 已实现 | AppContext 必须精确绑定 subject/auth_epoch/profile/config/purpose，敏感解析绕过正缓存并实时复核 new-api 身份；真实 AppMode=4、发布态和腾讯 RequestId 尚待 live 验证 |
| 固定人民币月度套餐、周期、人工付款、不可变账单 | 已实现 | 周期取消使用行锁、expected_version、原因和 auth_epoch 撤销；账单只有在周期取消后才能显式 void，二者都不暗示自动退款；不按 Turn/Token/工具向客户计费，不改 new-api quota |
| 腾讯费用中心只读成本导入与人工修订 | 已实现 | 自动导入只形成待复核成本草稿且不自动归因；复核可补充经所有权校验的客户/App/周期归因；锁定记录只能通过 `0017` 不可变 revision chain 原子替换，原证据保留且 superseded 记录不重复计入毛利 |
| Credential/BYOK、轮换/回滚、双人审批 | 已实现 | Secret 仅服务端引用；生产 Secret Manager/KMS 与实操轮换待验收 |
| Secret canonical fingerprint 与 readiness | 已实现 | legacy 重绑定仅允许容器内 loopback + bootstrap maintenance 调用 |
| 管理 Session、CSRF、可信 actor、审计导出、保留策略 | 已实现 | 管理 actor 来自已验证 Session；请求头不能伪造管理员身份 |
| 影子账号、每用户 Agent、Conversation、历史隔离 | 已实现 | ADP 独立 PostgreSQL；真实 CopyAgent/Conversation 待验证 |
| 持久 Turn、SSE 续流、取消意图、usage evidence | 已实现 | provider 未确认的取消不得标记 confirmed |
| Turn-event 持久化可观测性 | 已实现 | 独立 event-row counter；全部六类写事务仅在 commit 后计数，DB histogram 每事务观测一次并使用毫秒级 bucket |
| Conversation→Workspace→File 所有权 | 已实现 | provider locator/COS locator 不返回浏览器；真实 COS/扫描待验收 |
| 离线定时任务、幂等执行、租约和配额 | 已实现 | 真实 provider 执行、重启和多实例抢占待验收 |
| 用户 OAuth vault | 已实现 | 当前浏览器 Session、PKCE、单次事务 Cookie、scope/generation 防旧回调恢复 |
| Skill 变更 | 已实现 | 仅采用腾讯公开 `SkillList` 完整替换、行锁、读回核验 |
| Tool/Plugin/Connector 上游目录、绑定与对账 | 已实现、默认关闭 | 官方 v20260520 Summary/Detail/ModifyAgent；只接受无鉴权、平台 APIKey、CAM 且 `ToolAccessMode=1` 的 allowlist 交集，完整 pre/post readback。依赖这些集成的 Turn 执行仍 fail-closed，必须完成真实供应商限额与执行验收后另行开放；OAuth、写工具和物理解绑继续阻塞 |
| 腾讯 AGSX 托管沙箱 | 本地实现与独立复核通过、默认关闭 | 仅官方控制面和 E2B 数据面；`run_code` 使用内存隔离 worker；PTY 使用一次性 subprotocol 票据、同源 WebSocket、持续复核和 Blue/Green reaper，真实腾讯验收前两个独立开关均保持 false |
| 蓝绿、备份、DR、E2E/load harness | 已提供运维资产 | 只有离线自测，没有真实切流、恢复、50/100 SSE 报告 |

## 4. 关键安全不变量

### 4.1 身份与会话

- new-api 用户是唯一登录真值；ADP 影子账号不能密码/API key/OAuth 独立登录。
- entry ticket、control session、ADP SSO ticket、browser binding 是四个不同随机凭据，只保存摘要并分别限时、限路径、单次消费。
- 管理操作的 actor 只能从已验证管理 Session 生成；`X-Claw-Actor` 等客户端字段不参与授权或审计身份。
- bootstrap token 不能申请、审批或执行双人治理动作；Secret fingerprint 重绑定只允许容器 loopback maintenance 路径。

### 4.2 Secret 与 BYOK

- 数据库只保存 Secret ref、owner scope、版本、状态和 server-derived canonical fingerprint，不保存明文。
- `platform` 与 `customer:{id}` owner scope 不可混用；运行、验证、迁移和审批都重新校验所有权与 fingerprint。
- Primary App 的首次配置是唯一单管理员 bootstrap；附加 App 不能借首次配置引入新 provider credential。
- `/readyz` 在 active/retiring credential 或 current/pending AppKey 缺失、legacy 或 fingerprint 不一致时返回 503。

### 4.3 OAuth、Skill 与执行合同

- OAuth state 同时绑定当前 DB browser session、一次性事务 Cookie、身份/客户/App/profile/config/provider/connector 完整作用域和单调 generation。
- disconnect 或重新 start 会使旧 callback 永久失效；recent-auth 校验当前 session 的 `sid/auth_time`，不是身份级全局时间。
- Skill mutation 与普通/定时 Turn 使用同一 Agent 行锁和固定锁顺序，消除配置与提交 TOCTOU。
- Tool/Plugin/Connector 只允许公开合同覆盖的只读、非使用者 OAuth 子集；用户 OAuth Token 注入、`ToolAccessMode=2` 写操作和未经真实验证的物理解绑必须保持关闭。PTY 只调用官方 E2B SDK 公共方法并保持独立开关关闭，不能用控制台抓包或猜测内部路径替代真实验收。

### 4.4 沙箱

- 沙箱必须按 customer、new-api user、App、Conversation 四维隔离，实例/令牌/provider locator 不返回浏览器。
- 固定 `NetworkMode=SANDBOX`、`AuthMode=TOKEN`、非持久实例；容量校验必须跨蓝绿实例串行化。
- 代码、命令、文件、输出、运行时长、并发及 provider start 都受服务端硬上限；超限、取消和未知状态必须停止或隔离、持久化、审计且继续占用容量，直到确认终态。
- 只调用腾讯公开 `Start/Describe/Pause/Resume/Stop` 控制面和官方 E2B-compatible SDK；不得硬编码未公开 wire path。

## 5. 本地验证台账

以下结果均为最终工作树的本地证据：

| 检查 | 结果 |
|---|---|
| new-api `go test -p 1 ./...` | 通过 |
| new-api `go vet ./...` | 未通过；命中未修改的既有 provider unreachable-code、`CustomEvent` lock copy 和 IPv6 format 问题，不属于本次 Claw diff |
| new-api default frontend test/build-check | 45 tests 通过；类型检查与生产构建通过 |
| new-api Claw boundary gate | 通过：`3/10` 文件、`76/100` 行、`99.60%` 新增；gate 自测 `8/8` 通过 |
| claw-control `go test -p 1 ./...` | 通过 |
| claw-control `go vet ./...` | 通过 |
| claw-control admin UI | Vitest `14/14`、TypeScript、生产构建均通过 |
| ADP Workbench 全量后端 | `466 passed, 3 skipped`；skip 仅为 Windows 无法验证的 POSIX owner/mode/symlink；新增覆盖 run_code worker、只读 Integration 与 PTY ticket/queue/normal-close 合同 |
| ADP sandbox/acceptance/route-security 独立复核 | `151 passed, 3 skipped`；覆盖生产只读恢复、Provider Start 响应丢失、重放不重复创建、证据隔离、PTY 一次性票据、跨域 pre-handshake 拒绝与强制清理 |
| ADP 客户端 | 16 tests、component/app type-check、app production build 通过 |
| ADP boundary gate | 通过；gate 自测 `21/21` 通过，额外阻止控制面实体、可变 Plan 投影、Secret 落库和完整 AppContext 序列化 |
| deploy security/release scripts | 共 `71` 项：`61` 通过、`10` 个 Windows/POSIX 环境条件 skip、零失败；overlay linter、Caddy 静态合同、YAML 解析和 Python 编译均通过；严格校验 canonical origin、new-api 私网 upstream、发布清单和 Blue/Green 独立不可变镜像引用 |
| fake E2E/load harness | `34/34` 通过；覆盖 SSO/Turn 精确结构化 SSE 续流、multi-context selector/真实 Conversation IDOR、完整 requirement 预检、发布清单和双套独立 Ed25519 签名、EICAR、托管沙箱 Provider 响应丢失及清理、Prometheus 外部证据采集与防手写/篡改门禁 |
| DR 离线 harness | `5/5` 通过 |
| 两仓库 `git diff --check` | 通过；ADP 仅报告 Windows CRLF 提示 |

`go vet` 的既有问题不能被本次功能声明为已修复，也不应为了 Claw 接入修改无关 relay provider；若发布策略要求全仓 vet 为绿，应由独立 upstream-maintenance 变更处理。

发布资产复审发现的本地缺口也已关闭：全新服务器部署会显式启动并等待 ClamAV；顶层 Secret 统一拒绝 symlink/宽权限/错误 owner/超限文件；Blue/Green 不再共用可变应用镜像 tag；DR 示例保护路径与 Compose 实际 volume key 一致；live E2E 在发出网络请求前统一验证完整 requirement/语义合同，对完整身份字段、已知 direct-ADP 路由 401/403、BYOK 双管理员、OAuth/定时任务/费用导入/selector、沙箱 Provider 响应丢失与清理进行 fail-closed 验证。主报告与外部负载证据分别使用互不相同的 OpenSSH Ed25519 签发身份；独立 Observer 只读查询 Prometheus 真实 series，并使用彼此独立的 terminal Turn counter、成功持久化 event-row counter 和数据库事务 histogram，把 50/100 档位、连接、内存、持久化事件和 DB 延迟绑定到同一发布与验收 run。真实 OAuth 浏览器授权、腾讯账单/AGSX、生产 Caddy、平台级 cAdvisor/Prometheus 指标源、跨库、负载与完整 DR 仍属于下节外部验收，不能由本地 harness 代替，也不得向工作台容器开放 Docker socket 或 host PID/network 冒充平台监控。

## 6. 外部验收阻塞项

| 项目 | 需要的真实证据 |
|---|---|
| 腾讯 ADP | 每客户独立 AppMode=4 App、AppId/AppKey/SpaceId/模板 Agent；Describe/CopyAgent/CreateConversation/Chat/History 的脱敏请求与 RequestId |
| 腾讯 AGSX | region、ToolId/ToolName、`ark_` API key、最小权限 CAM、真实配额；生命周期、Shell/文件、六种语言 `run_code`、PTY create/input/resize/kill、同源握手、429、取消、撤权、输出洪泛、进程崩溃和回收报告；任何一项失败则 code/PTY 独立开关继续 false |
| OAuth | provider metadata、client secret、已登记 callback、测试账户；连接、断开、旧 callback、防跨用户/App 复用报告 |
| 文件 | 私有 COS、最小权限凭据、ClamAV；正常文件、EICAR、跨用户 IDOR、过期下载和禁用后访问报告 |
| 数据库 | claw-control 的 SQLite/MySQL/PostgreSQL 三后端；ADP 独立 PostgreSQL；迁移、并发锁、备份恢复报告 |
| 网络 | 生产域名、DNS/TLS、目标 Caddy 版本、真实 Cookie 组合、forward-auth、SSE flush 和断线续流抓包 |
| 性能 | 50/100 并发 SSE 的 P50/P95/P99、错误率、内存、连接、DB/Redis/腾讯限流和成本预算 |
| 运维 | 蓝绿切流/回滚、加密备份、隔离恢复、第二地域仓库、RPO/RTO 和密钥恢复演练 |

## 7. 发布判定

当前不能开启生产 `WORKBENCH_ENABLED` 或 `WORKBENCH_SANDBOX_ENABLED`。允许进入下一阶段的条件是：

1. MySQL/PostgreSQL 与真实腾讯/COS/OAuth/Caddy 验收报告齐全。
2. Linux 预生产复跑 POSIX Secret owner/mode/symlink 与 readiness 测试。
3. 50/100 SSE、蓝绿、备份恢复和灾备演练通过。
4. 报告绑定确定的两仓库 commit SHA、镜像 digest、迁移版本和部署版本。
5. feature flag 关闭时 new-api 原有 API 和 `/playground/legacy` 回归通过。

在这些条件完成前，正确状态是“代码已实现、功能默认关闭、外部验收阻塞”，不是“已生产部署”。
