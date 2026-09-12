# 交接文档：代理商旁路服务（Agency Hub / Distributor）

> 本文档用于新旧会话交接。接手同事直接基于当前代码库与本文件即可继续开发。
> 生成时间：2026-09-12。信息来源：`agency-hub-sidecar-design.md`、`docs/distributor-affiliate-prd.md`、当前工作区代码与 git 状态。

## 0. 一句话现状

正在基于 new-api（`codex/seedance-cn-adapter` 分支）开发独立的「代理商旁路服务」`agency-hub`：代理商设销售折扣 → 生成邀请链接/二维码 → 客户注册自动归属 → 按模型实际消费产生佣金 → 提现管理。**后端主链路已可用并已部署到 `47.97.97.89`，但尚未达到设计文档 §22 的 Phase A–E 最终验收（Go/No-Go）标准，生产财务开关保持关闭。**

## 1. 业务与术语

- 业务命名：页面用「代理商 / 代理商中心」，代码统一 `agency`，表前缀 `agency_hub_`。
- 产品文档：`docs/distributor-affiliate-prd.md`（625 行）。
- 设计规格（权威基线）：`agency-hub-sidecar-design.md`（1388 行，2026-09-09 复核版）。
- 形态：独立部署旁路服务，与 new-api 共用必要数据/缓存，**不进入模型转发链路**（旁路计算、核心写事实）。
- 对外入口：`https://gateway.nexus-reach.com/agency/`。
- 时间口径：存储 Unix 时间戳；页面/CSV/统计按 `Asia/Shanghai`。
- 币种口径：跟随 new-api 当前币种，每笔财务事件保存币种与换算快照。
- 关键词：Distributor(=代理商机构)、paid（真付费额度）、nonpaid（赠额/补单/兑换/订阅，不返佣）、debt（欠费）、charge_id（稳定财务 ID）、MoneySeq（资金序号）。

## 2. 系统架构

```
web/agency-web+(agency 管理前端)  agency-hub(sidecar, Go+Gin)   --outbox/事件-->  消费 SQLite/MySQL/PG
        │                                                    │
        ▼                                                    ▼
   new-api 网关(宿主)   model/service 计费钩子                 agency_hub_* 表（旁路投影、佣金、提现）
        │ 内部受控命令(签名)  /internal/agency/v1/commands
        └── gateway worker 执行 fund/quote 等权威写事实
```

- 语言/框架：Go + Gin + GORM v2；前端 React 19 + TS + Rsbuild（`agency-web/`）。
- 根模块：`github.com/QuantumNous/new-api`；独立子模块 `relaykit/`（禁止反向依赖根模块）。
- 数据库：SQLite / MySQL≥5.7.8 / PostgreSQL≥9.6 三库兼容（见 AGENTS.md 数据库规则）。
- 部署：Docker compose（new-api + agency-hub + Caddy），见 `docker-compose.yml`。线上服务器 `47.97.97.89`，域名 `gateway.nexus-reach.com`，SSH 密钥 `newAPIcn.pem`。
- 责任边界：模型请求不经 agency-hub；网关事务组是唯一资金源；penalty 费用独立账单；人工补单归 `nonpaid`。

## 3. 模块清单（后端）

### 3.1 网关侧（根模块）
| 文件/目录 | 职责 |
| --- | --- |
| `model/agency_funding.go` | 资金账户/lot/分配、预扣、结算、冲正、power贷款、幂等（核心财务） |
| `model/agency_models.go` | 全量 `agency_hub_*` 数据模型与表名 |
| `model/agency_migration.go` | 三数据库迁移 |
| `service/billing_session.go` | BillingSession 预扣/结算/负差额，接入资金投影 |
| `service/quota.go`、`service/midjourney.go` | WSS/普通消费与 MJ 任务结算的 durable 接入 |
| `service/agency_gateway.go` | 网关计费事件/快照管线 |
| `service/agency_command_worker.go` | 消费签名 AgencyCommand（provisioning.start/cancel、funding.reverse）|
| `relay/channel/task/doubao/adaptor_test.go` | hopper 支付参数透传（Drag/Chat 结构同步）|

### 3.2 旁路服务（`pkg/agencyhub/`）
| 文件 | 职责 |
| --- | --- |
| `app.go` | 路由注册、中间件、根目录/健康检查 |
| `auth.go` `sso.go` | 独立账号登录、Root SSO、高风险 proof（action/object 绑定、iat/nbf 时间窗）|
| `agency_service.go` | 机构/操作员/邀请码/注册绑定/provisioning |
| `pricing_service.go` | 整包价格版本、S（销售）/C（结算）系数、发布/预览/历史 |
| `finance_service.go` | 佣金、充值与统计接口（int64→string 序列化改造中）|
| `funding_reversal_service.go` | Root 资金冲正入口（仅排队受控命令）|
| `report_service.go` | 客户/用量/充值/报表/CSV 导出（防注入，进行中）|
| `withdrawal_accounts.go` | 收款账户 AES-GCM+AAD 加密、提现状态机 |
| `reconciliation_service.go` | 对账/异常任务 |
| `provisioning.go`、`agency_command.go` | provisioning 作业与内部命令契约/校验 |
| `idempotency.go` `cursor.go` `decimal.go` `delivery_secret.go` | 幂等/游标/十进制/临时密码交付 |
| `worker.go` | outbox 消费者、租约/fencing、乱序与冲正排序 |
| `audit.go` `config.go` `static.go` | 审计/配置/静态资源 |

### 3.3 前端
- `agency-web/`：React 19 + TS + Vite 的 agency 管理前端（dist 已构建）。
- 主平台 `web/`：暂未新增完整代理商管理页（后端优先，前端后补）。

### 3.4 可执行命令（`cmd/`）
- `cmd/agency-hub`：agency-hub 服务与 `migrate` 子命令。
- 另存 `cmd/reseller-hub`、`cmd/enterprise-policy-hub`、`cmd/hwdrama-proxy` 等（非本次主线）。

## 4. 已完成（已提交与已部署）

- 代理商创建接口返回 `agency_id / invite_code / invite_url / invite_qr_url / temporary_password(一次性) / delivery_id`。
- 公共邀请接口：`GET /agency/api/v1/public/invitations/:code`、`GET .../qr`（PNG 512×512）。
- 客户带 `invite` 注册 → 事务内绑定机构 + 初始化资金账户 + 默认 Token；幂等防重复。
- durable 资金基础：funding account/lot/allocation/ledger/billing_journal/outbox/event_deliveries/source_events。
- 佣金：仅最终消费结算（settle/postconsume）入佣，按 `K = Round(G×P/B)`；赠送/兑换码/订阅/人工补单不计佣；退款/拒付冲正。
- 提现状态机：review→paying→paid / on_hold / payment_unknown，防重复打款，账户快照+密文哈希，Root mark-paid 唯一银行凭证。
- Root SSO、高风险 proof、临时密码一次性交付与 ack 销毁。
- 导出：usage/topups/commissions CSV，UTF-8 BOM、公式注入防护、366 天上限。
- 健康检查：`/healthz /livez /readyz`（schema/capabilities/backlog）。
- 已上线：`https://gateway.nexus-reach.com/agency/`；邀请码示例 `4FQT87RPWQ`（机构 `uzoom`）。
- 规约遵守：未提交前已通关轨的构建/测试验证（见 §7）。

## 5. 正在做（未提交改动，接手第一优先）

当前工作区改动（`git status` 7 个 M）：

| 文件 | 内容 |
| --- | --- |
| `model/agency_funding.go` | 新增 `TryReserveAgencyWalletAndTokenWithSequence`（返回 `moneySeq`），在**同一锁定资金账户事务**里读取资金序号；旧 `*WithSnapshot` 改为其薄封装 |
| `service/billing_session.go` | Settle/Reserve/preConsume 改用 `*WithSequence` 并回填 `relayInfo.AgencyMoneySeq` |
| `service/quota.go` | PreWss/PostWss/postConsume 三处改用 `*WithSequence` 并回填 `AgencyMoneySeq` |
| `service/midjourney.go` | `SettleMidjourneyTaskBilling` 改用 `*WithSequence`（注意当前有缩进/格式化问题，见下）|
| `pkg/agencyhub/finance_service.go` | `commissionSummary` 响应 int64 字段改为字符串（防 JS 精度丢失）|
| `pkg/agencyhub/report_service.go` | `customerUsage/customerTopups/reportSummary` 等 int64 字段改字符串、聚合重构 |
| `relay/channel/task/doubao/adaptor_test.go` | `OmniReferenceTaskType/OutputFormat` 断言随 DTO 结构同步 |

**主线意图**：让网关财务事件路径“序列感知”（`MoneySeq`）——预扣/结算与钱包变更同事务取序号，事件不会与后续钱包变更竞态，为对账/落库提供单调顺序。报表/佣金响应 int64→string 是配合前端精度与设计 §22.2「int64 精度」验收。

**接手续作清单**：
1. `service/midjourney.go` 的 `SettleMidjourneyTaskBilling` 缩进/格式异常（`gofmt` 需重排），并确认 `result` 变量在错误分支的流控。
2. `report_service.go` 聚合重构后，跑 `pkg/agencyhub` 相关测试确认 `reportSummary` 无回归，复查 `companion`/`commission` 两路合并逻辑。
3. 确认 `relayInfo.AgencyMoneySeq` 是否已持久化到 `billing_outbox`/事件负载（若未接，可能仍需补）。

未跟踪文件（勿提交）：`newAPIcn.pem`、`BPlatform.pem`、`problem.jpg`、`uzoom-invite-qr.png`、`server-data.sql(.gz)`、`bin-dev/`。

## 6. 核心数据模型（`agency_hub_*`，前缀常量 `AgencyTablePrefix`）

机构/身份/归属：`agencies`、`operator_accounts`、`sessions`、`sso_ticket_uses`、`verification_uses`、`delivery_secrets`、`user_bindings`、`active_user_bindings`、`provisioning_jobs`。
定价：`price_policy_items`。
财务权威：`funding_accounts`、`funding_lots`、`funding_allocations`、`funding_ledger`、`funding_debts`、`debt_repayments`、`billing_journals`、`billing_operations`、`billing_outbox`、`event_deliveries`、`source_events`、`usage_facts`、`topup_facts`、`funding_reversals`。
投影/佣金/提现：`commission_ledger`、`commission_balances`、`withdrawal_accounts`、`withdrawals`、`daily_stats`。
运维：`audit_logs`、`export_jobs`、`worker_leases`、`commands`、`idempotency_records`。

## 7. 构建与验证命令（接手必读）

```bash
# 统一使用项目内工具链（系统 go 不在 PATH）
export PATH=/tmp/codex-go/go/bin:$PATH

# 根模块构建 + 全量测试
go build ./...
go test ./... -count=1

# relaykit 独立模块（项目强约束，必须单独过）
cd relaykit && GOWORK=off go build ./...

# 前端
cd agency-web && bun install && bun run build

# 差异完整性
git diff --check
```

注意：Go 全量测试可能较慢；按模块先跑 `pkg/agencyhub`、`model`、`service`、`controller`。

## 8. 待开发 / 上线前置（按设计 doc §21-22）

### Phase A–E 缺口（上线 Go/No-Go 前必须完成）
- **完整计费路径覆盖**：确保所有模型/任务路径（OpenAI、Claude、Gemini、Responses、Realtime、MJ、视频、表达式、图片）都进入 durable billing；未完成路径默认不开通生产，禁止“零佣金”掩盖缺账。
- **三数据库验证**：SQLite/MySQL/PostgreSQL 迁移 + 核心财务并发真实测试（当前只验证了 SQLite）。
- **性能/容量**：持续 60RPS/峰值 120、单用户/单机构热点压测；背压100万事件追平时间与存储预算。
- **故障恢复**：crash 注入（事务前后/扣款与 Token 之间/journal 与 outbox 之间）、低 ID 晚提交、租约过期旧 worker、乱序/重复/冲正先到、归档后重复投递；Redis 故障回源；主库故障安全失败。
- **安全/越权**：SSO 验票、proof action/object 绑定、跨机构数据隔离、临时密码无明文落盘、CSV 注入、时区边界、int64 精度（正在做）。
- **对账/归档**：Root reconciliation 入口、daily_stats 归档索引、备份/恢复可复算精确余额。
- **运营开关**：佣金实时结算、提现、导出等生产开关当前**保持关闭**；全部验证通过前不打开。

### 已知风险点（设计 §24 修订记录）
- 计费接入、并发/故障测试、容量验证是上线前置，尚未实现或实测。
- 上线前必须只读核实生产数据库是否存在代理商数据；禁止直接清理。

## 9. 关键接口速查（详见设计 doc §13）

### 公开（无需登录）
- `GET /agency/api/v1/public/invitations/{code}`
- `GET /agency/api/v1/public/invitations/{code}/qr`

### 认证
- `POST /agency/api/v1/auth/nonce`、`POST /agency/api/v1/auth/login`
- `GET /agency/sso/start`、`POST /agency/sso/callback`
- `POST /auth/logout`、`POST /auth/change-password`、`POST /auth/verify`

### 代理商自身
- `GET /auth/me`；`GET/POST /agencies`；`GET /pricing`、`POST /pricing/sales/preview|publish`、`GET /pricing/history`
- `GET /models?q=`；`GET /customers(/...)`、`/customers/{user_id}/usage|topups`
- `GET /reports/summary`；`GET /commissions/summary|ledger`；`GET /audit`
- `POST /exports`、`GET /exports/{id}(/download)`
- `GET/POST /withdrawal-accounts`、`PATCH /withdrawal-accounts/{id}`、`POST /withdrawal-accounts/{id}/disable`
- `GET/POST /withdrawals`、`POST /withdrawals/{id}/cancel`

### Root 管理
- `GET/POST /root/agencies`、`PATCH /root/agencies/{id}`、`disable|enable|reset-password|enter`、`POST /leave-agency`
- `POST /root/users/{user_id}/bind`、`GET/POST /root/provisioning/{job_id}(/cancel)`、`POST /root/users/{user_id}/transfer`
- `POST /root/deliveries/{delivery_id}/ack`
- `GET/POST /root/agencies/{id}/pricing(preview|publish)`
- `POST /root/withdrawal-accounts/{id}/reveal`、`POST /root/withdrawals/{id}/transition|mark-paid`
- `POST /root/funding/reversals`、`GET /root/audit|sync/status|reconciliation/issues`、`POST /root/reconciliation/runs`、`POST /root/reconciliation/issues/{id}/resolve`
- 内部命令：`POST/GET /internal/agency/v1/commands`（仅内网，mTLS+签名）

## 10. 设计约定速记（改动前必读）
- **AGENTS.md 规则**：JSON 统一走 `common.*`；三库兼容；`relaykit` 独立可构建；计费安全不变量（饱和/防负扣、`common/quota_math.go`）；billing 改动先读 `pkg/billingexpr/expr.md`。
- 计费：预扣≠最终；只认 `settle/postconsume`；`charge_id` 稳定且幂等；佣金公式 `K = Round(G×P/B)`；饱和事件经 `QuotaClamp`/`attachQuotaSaturation` 审计。
- 资金：付费优先；赠额/补单/兑换/订阅=nonpaid 不计佣；欠费会先还 debt 再发佣金；退款冲正不重不漏。
- 提现：状态机白名单 + expected_version CAS；账户快照防篡改；payment_unknown 不可直接回 paying。
- 命令：payload 必须是 JSON object、拒绝重复 key、签名 command 二次校验现态；旁路 DB 账号无 `users.quota` 写权限。
- 前端数值：int64 金额字段以字符串返回（正在统一）。
- 上线纪律：未到 Go/No-Go 前保持「不提交、不推送」由用户决定；生产请求/支付/提现操作不在本轮执行。

## 11. 下一步建议
1. 先收尾 §5 未提交改动（gofmt 修复 midjourney、reportSummary 测试、事件负载是否含 MoneySeq）。
2. 跑全量测试与 relaykit/agency-web 构建复验。
3. 按 Phase C→D→E 补齐计费路径覆盖与三库/故障/容量验证（§8）。
4. 全部通过后由用户决定是否提交、推送并打开生产财务开关。

## 13. mechanism-test ok


## 14. 本机试运行进展（2026-09-12 第二轮）
- 三数据库迁移门控已全部通过（SQLite 默认 + PostgreSQL 15432 + MySQL 13306 隔离实例），见 TestMigrateAgencyExternalDatabaseCompatibility。
- 新增三库核心财务并发测试 TestAgencyFundingConcurrentReserveIsConservedAcrossDialects（含 mysql/postgres 行锁 money_seq 单调唯一、lot 守恒、paid 不重复分配；SQLite 顺序执行），三库均 PASS。
- 修复真实 MySQL 兼容 bug：model/agency_funding.go 4 处裸 key 保留字改 agencyKeyColumn()（惰性 initCol），MySQL durable token reserve/release 不再语法错误。
- 确认 relayInfo.AgencyMoneySeq 已落入 billing event payload 与 billing_outbox（service/agency_gateway.go），无需再补。
- 回归：pkg/agencyhub、model、service、controller 全绿；root + relaykit build、gofmt、git diff --check 通过。
- 尚余：性能 60RPS/峰值120 与背压容量、故障注入（crash/租约/乱序重投）、§22 完整越权与对账验收、Go/No-Go 终审。
- 新增三库单热点预扣性能基准 BenchmarkAgencyFundingReservePerDialect（ReserveAgencyFundingWithSequence，单账户顺序预扣=FOR UPDATE 行锁串行化路径=单热点吞吐上限）：SQLite(内存) ~1959/s·510us/op、MySQL(13306) ~228/s·4.39ms/op、PostgreSQL(15432) ~83/s·12.0ms/op。单账户顺序吞吐即并发热点上限，MySQL/PostgreSQL 均高于 60RPS 持续目标（PostgreSQL 距 120 峰值较近）；多热点账户并行后总体可线性分散。本机基准仅作基线，§22.4 尚需生产环境背压/存储预算实测。
- 基准终版（重跑一致）：SQLite ~1882/s·531us/op、MySQL ~230/s·4.34ms/op、PostgreSQL ~75/s·13.3ms/op；每地址分配行放大 k=alloc-rows/op≈1.0（MySQL 含预热行 1.001、PG 1.006）。单账户顺序短扣即并发热点上限，MySQL/PostgreSQL 均高于 60RPS，PostgreSQL 距 120 峰值较近；背压 100 万追平时长、日增量行宽/在线保留预算仍需生产环境按 k×事件宽度实测。

## 15. 并发基准与 MySQL 死锁修复（2026-09-12 第三轮）
- 新增并发生成基准 BenchmarkAgencyFundingReserveConcurrentPerDialect（sqlite 顺序执行防 SQLITE_LOCKED；mysql/pg 用 RunParallel，场景 single-user-hot / single-org-hot / multi-org-mixed），三条腿全过，k=alloc-rows/op≈1.0：SQLite ~4.6-4.8k/s；MySQL ~1356 / 2847 / 2972 reserves/s；PostgreSQL ~323 / 1118 / 1262 reserves/s。
- 修复真实生产级 MySQL 死锁（Error 1213，多机构腿首轮 reserve 1-11 全挂，SHOW ENGINE INNODB STATUS 死锁图确认）：根因 = agency_hub_funding_allocations.charge_id 是非唯一索引，而生产 charge_id 来自 common.NewRequestId()（时间戳前缀+8 随机字符）全部排到索引尾部 supremum；原 reserveAgencyFundingTx 先做存在性检查（非唯一索引 FOR UPDATE 相等扫描持全局 gap 锁）再锁账户行，并发插入申请 insert-intention → 跨用户 100% 相互死锁（PostgreSQL 无此问题）。
- 修复 = 重排锁序：账户行 AgencyLockForUpdate 前置（同用户串行化），存在性检查改普通读；删除原重复账户锁；agencyFundingAllocatedTx 改普通读（5 个调用点均持用户/账户级锁）。另加第二道防线 model/agency_lock_retry.go：isTransientLockError（MySQL 1213/1205、PG 40P01/55P03、SQLite busy/locked）、retryAgencyTransaction（4 次、2/4/8ms 退避）、runAgencyFundingTransaction，接入 SetAgencyQuotaAbsolute / TryReserveUserQuotaAndAgencyWithToken / tryReserveAgencyWalletAndToken / ReserveAgencyFundingWithSequence 四个入口，外加新契约测试 model/agency_lock_retry_test.go。
- 修复后重跑验证：MySQL 并发基准三条腿全部通过（不再有任何 1213），最低腿 1356/s、多机构 2972/s，单用户/单机构/多机构均远超 60 RPS 持续与 120 RPS 峰值目标（PG 多机构最冷 323/s 也 >120）；三库守恒门控 TestAgencyFundingConcurrentReserveIsConservedAcrossDialects（sqlite/mysql/postgres）与 TestMigrateAgencyExternalDatabaseCompatibility（mysql/postgres）全部 PASS。
- 完整回归复验：gofmt -l 干净、git diff --check 通过、pkg/agencyhub + model + service + controller 全绿、根模块 go build ./... 与 relaykit GOWORK=off go build ./... 均通过。
- 尚余（需生产环境或人工，不可编造数字）：§22.4 生产级背压/存储预算（100 万积压追平时长、日增量字节/在线保留预算）、§22.2 SSO 端到端（真实 IdP 错 aud/过期/state、临时密码 Root 恢复）、备份/加密密钥恢复与回滚演练、§22.5 Go/No-Go 终审（提交/推送、生产财务开关由用户决定）。

## 16. §22.2 SSO/高危验证契约测试补齐（2026-09-12 第四轮）
- 新增 pkg/agencyhub/sso_contract_test.go，显式覆盖 §22.2「SSO单用/错aud/过期/state错/源会话撤销或降权/Root高风险proof不可换body-action重放」：
  - TestSSOTicketVerifyRejectsWrongAudienceIssuerExpiryAndKey：错 aud、错 issuer、过期、伪造密钥签名全部被 VerifySSOTicket 拒绝（纯单元，无 DB）。
  - TestSSOCallbackRejectsStateMismatchAndWrongAudience：SSO 回调对 state_hash 与 cookie 不符、上报 state 与 cookie 不符、错 aud、过期票据逐一拒绝（invalid_state / invalid_ticket）。
  - TestSSOCallbackTicketIsSingleUse：同一票据第二次回调被拒（ticket_replayed，JTI 唯一约束），不可再建第二个 Root 会话。
  - TestSourceSessionRevocationInvalidatesRootSession / TestSourceSessionDowngradeInvalidatesRootSession：源管理员会话撤销或 version 升级（降权/刷新）后，旁路 Root 会话立即 401 source_session_invalid 并被吊销。
  - TestRootVerificationProofIsSingleUseAndBodyBound：高危 proof 首次通过、同 JTI 重放拒绝、body_hash 绑定（换 body 拒绝）。
  - TestRootVerificationProofScopeAndSessionMustMatch：proof 的 action/object 与请求作用域不符、SourceSessionVersion 过期均拒绝。
- Root PAT 代替 Session 无旁路侧可接受路径：CreateRootSession 只接受显式会话参数，宿主必须先用真实浏览器 Session 校验；该项由宿主网关集成承担，本机无法端到端复验。
- pkg/agencyhub 全套测试（含新增）PASS；gofmt 干净。

## 17. §22.4 存储预算实测量（2026-09-12 第四轮，live MySQL 真实 schema）
- 方法：对 MySQL 13306（agency_test，innodb_page_size=16384）4 张核心每事件表 TRUNCATE 后受控插入 20000 行真实字段，按 .ibd 文件字节增量/20000 得含索引行宽（16KB 页粒度、追加场景）：funding_allocations ~673 B、billing_journals ~1039 B、billing_outbox ~1565 B、commission_ledger ~727 B。
- 每事件合计 ~4003 B/event ≈ 4.0 KB，k=alloc-rows/op≈1.0 已由并发基准证实（一行分配即一行事件行）。
- 预算（60 RPS 持续）：5,184,000 事件/天 × 4.0 KB ≈ 19.3 GiB/天（约 20 GB）。在线保留：30 天 ≈ 580 GiB、90 天 ≈ 1.7 TiB；若计 archive/待背压保留需再乘事件宽度倍数。峰值 120 RPS 为 2 倍速率但须按背压排队时间窗口计算积压体量。
- 100 万积压追平：受消费者侧投影/出账速率约束（非 reserve 插入腿），本机未做消费者吞吐基准；生产实测前不得写死追平时长。反向结论：存储是本节最大成本项（约 20 GB/天 @60RPS），应在设计 §17 归档/清理与在线保留策略落地后开生产。
- 本项仍属"实测量+预算"而非生产验收；§22.4 生产环境 100 万积压追平时长与日增量仍待上线后按真实事件宽度复核。

## 18. §22.4 消费者排水速率基准与发现（2026-09-12 第四轮）
- 新增 pkg/agencyhub/consumer_bench_test.go：BenchmarkAgencyConsumerProjectionPerDialect 走真实管道（claim 租约 → CanonicalHash 校验 → processBillingEventWithLease → source_event/佣金/日报/出账→done），各方言种子 5000 事件全量排空测事件/s。
- 实测单批吞吐（含 GORM 全链路、每事件事务）：MySQL ~10460/s（~488ms/5000）、PostgreSQL ~6804/s（~750ms/5000）、SQLite(内存) ~1454/s（~3.4s/5000）。原始数据库吞吐远高于 60/120 RPS 目标，排水不是 DB 瓶颈。
- 关键发现（基准暴露的运营上限）：StartBackground 每 2s tick 仅调用 RunConsumerOnce(ctx,100) 一次 → 持续排水上限定在 100/2s = 50/s，低于设计稳态 60 RPS（峰值 120 时缺口更明显）。按设计负载 60/s 持续，积压将以 ~10/s 增长（日增 ~0.9M 事件），不可持续。
- 结论：1M 积压"追平时长"不能按原生吞吐算（那样 MySQL ~96s），而受调度节拍约束：按当前 50/s 上限需 ~5.6 小时追平；只有提高节拍（例：每 tick 多次排水、batch 扩到 200+，或并行消费者）才能匹配 60 稳态。修复属生产行为参数与设计复核，需在上线措置中明确，不应在未评审前静默改动 worker.go。
- 建议（待用户/设计评审）：把 RunConsumerOnce 的调用改为"每 tick 至空速排水（batch 翻倍或循环排水直到耗尽/达到上限）"，实测后按 §15 复核即可闭合 §22.4 追平项。

## 19. 本机部署试运行 + §18 收尾 + 计费覆盖审计（2026-09-12 第五轮）

**§18 建议已落地**：worker.go 新增 `drainConsumerBudget`（每 tick 至空速排水、batch=500、预算上限 20 批），`TestDrainConsumerBudgetSustainsAbove60RPS` 契约覆盖；本轮重跑 `BenchmarkAgencyConsumerProjectionPerDialect`（SQLite+MySQL+PG 全三库，-run '^$'）实测排水：SQLite ~43238/s、MySQL ~9917/s、PostgreSQL ~7687/s，均远超 60RPS 稳态 / 120 峰值，1M 积压追平不再受 50/s 节拍瓶颈约束（§18 的旧上限结论作废）。

**本机部署试运行（真实 Postgres）**：
- 用当前未提交代码重编译 `bin-dev/new-api`（网关）+ `bin-dev/agency-hub`（sidecar，linux/amd64 CGO_ENABLED=0）；重启 `new-api-dev-run`（bind mount 生效），`/api/status` 200，AutoMigrate 正常新增 `users.billing_mode/funding_version`。
- `agency-hub migrate` 在共享 dev Postgres（postgres:5432/new-api）全量建表 41 张 `agency_hub_*`；sidecar 容器 `agency-hub-dev-run`（端口 3201）启动后 `/agency/readyz` 200、schema ready、无缺表；生产财务开关保持 OFF（capabilities 显示 commission_worker/exports/withdrawals=false）。
- 网关↔sidecar 互通验证：网关 agency command worker 在迁移前报 42P01，迁移后持续运行 0 错误（轮询 `agency_hub_commands` 正常）。
- HTTP 冒烟：`/agency/api/v1/public/invitations/*` 返回 404/JSON（路由正常）、`/commissions/ledger` 未登录 401、`/api/status` 200。

**三库 gate 用当前代码重跑全部 PASS**：`TestMigrateAgencyExternalDatabaseCompatibility`（MySQL 13306 + PG 15432）+ `TestAgencyFundingConcurrentReserveIsConservedAcrossDialects`（sqlite/mysql/postgres 并发守恒、money_seq 单调）。
`pkg/agencyhub` 全绿、`go build ./...`、`relaykit GOWORK=off go build`、`agency-web bun run build`、gofmt/diff-check 均通过。

**计费路径覆盖审计结论（§8 首项）**：网关所有标准 relay（OpenAI/Claude/Gemini/Responses/图片/音频/Embedding）统一经 `controller/relay.go` → `service.SettleBilling` → `BillingSession`（含 durable 钩子）；任务平台（视频/异步图）经 `relay/relay_task.go`（冻结 Quote + 预扣）→ `model/task_billing_atomic.go` `AdjustAgencyChargeTx`；Midjourney 经 `service/midjourney.go` `*WithSequence`；Realtime 按段 `RecordAgencyRealtimeSegment`；违规费独立账单。无未接入路径，无需“零佣金掩盖缺账”。

**int64 精度收尾**：`commissionSummary` + `reportSummary`/`customerUsage`/`customerTopups` 已是字符串；本轮将 `commissionLedger`（原直接返回原始 model 行的 Go 风格大写键 + int64 数字，与 agency-web ledger 表契约不符且超 2^53 丢精度）改为 snake_case 字符串 DTO `commissionLedgerView`，并新增 `TestCommissionLedgerReturnsSnakeCaseStringMoney`（含 ±9007199254740993 精度断言）。`listCustomers`/`withdrawalView` 核对已符合前端契约。

**尚余（非本机可闭合）**：生产环境背压/存储预算实测算（§22.4，本机仅基线）、三库线上并发压测、Go/No-Go 终审、提交/推送/打开生产财务开关（均由用户决定）。线上前置：上线前只读核实生产 DB 是否存在代理商数据，禁止直接清理。

## 20. 最终验证收尾（2026-09-12 第六轮）
- 修复 `TestAdjustAgencyChargeSettlesDurableDeltaAccurately` 末段断言：孤儿行判定需把 `nonpaid_consumed`/`debt_consumed` 一并纳入（纯赠额分配行合法地 `consumed=0`），改用“完全归零”口径。
- 三库 gate 用当前工作树重跑全部 PASS：`TestMigrateAgencyExternalDatabaseCompatibility`（真实 MySQL 8.2 `127.0.0.1:13306` + PostgreSQL 15 `15432`）+ `TestAgencyFundingConcurrentReserveIsConservedAcrossDialects`（sqlite/mysql/postgres 并发守恒、money_seq 单调）。根模块 `go build ./...` 与 `go test ./...`（0 FAIL）、`relaykit GOWORK=off go build ./...`、gofmt、`git diff --check` 全部通过。
- 本机部署健康：`new-api-dev-run` `/api/status` 200、`agency-hub-dev-run` `/agency/readyz` 200（capabilities 显示 agency_durable_v1=true，生产财务开关保持关闭）。
- 运维备忘（避免重复踩坑）：本机 13306 的 MySQL 是 **homebrew 原生实例**（datadir `/tmp/amysql5`，`--no-defaults`，`bind-address=127.0.0.1`），**root 密码为空**；`docker run -p 13306:3306` 的容器会经 Colima SSH 转发绑定 `*:13306`，但 `127.0.0.1:13306` 仍优先落到原生实例。跑三库 gate 的 DSN：MySQL `root:@tcp(127.0.0.1:13306)/agency_test?parseTime=true&charset=utf8mb4,utf8&loc=Local`；PG `host=127.0.0.1 port=15432 user=agency password=agency dbname=agency_test sslmode=disable`。缺模块缓存时的拉取用 `GOPROXY=https://goproxy.cn,direct`（`proxy.golang.org` 与 `docker.io` 在本机被墙）。
- 尚余（均需用户决策/外部环境，非本机可闭合）：生产背压/存储预算实测（§22.4）、三库线上并发压测、Go/No-Go 终审、提交/推送与打开生产财务开关；上线前只读核实生产 DB 是否存在代理商数据。
  Go/No-Go 验收审计：`docs/AGENCY_GO_NOGO_AUDIT.md`（Phase A–E / §22 证据映射与建议）。

## 21. 主平台↔agency-hub 浏览器 SSO 一键进入（2026-09-12 第七轮）
- 问题：主平台 :3000 登录后打开 `:3201/agency` 仍提示输入代理商账号密码。SSO 后端链路（平台 `POST /api/agency/sso-ticket`、hub `/agency/sso/start`+`/agency/sso/callback`）早已存在，但**前端从未接线**：hub 首页只有账号密码登录，平台侧也没有可供浏览器同源签票的桥接页。
- 实现（严格对齐设计 §5.2 “前端主动 POST 签票、票据不进 URL、签票必须平台同源”）：
  - 平台新增同源桥接页 `GET /api/agency/sso`（`controller/agency_sso_page.go` + `router/api-router.go`），被 hub 主页以隐藏 iframe 加载；页面内 JS 主动 `POST /api/agency/sso-ticket`（携带平台浏览器会话），成功后经 `window.postMessage` 把一次性票据回传 hub 页（票据不落 URL）。`AGENCY_SSO_ALLOWED_ORIGIN`（逗号分隔白名单）为空即禁用；非白名单 origin → 403。
  - hub 主页 `pkg/agencyhub/static.go` 重写：注入 `CONFIG`（`AGENCY_HUB_PLATFORM_BASE_URL`、base_path）；页面加载先探测 `/auth/me`，未登录则自动 `sso/start` → 建 iframe → 收 postMessage → `sso/callback` → 刷新进入；失败回退显示“通过主平台账号一键进入”按钮 + 原账号密码表单。`message` 事件校验 `event.origin` 必须等于平台 origin，防跨站伪装。
  - 新增 `AGENCY_HUB_PLATFORM_BASE_URL`（默认空=关闭 SSO）与 `AGENCY_HUB_COOKIE_SECURE`（默认 true；本地 http 开发需设 false，镜像平台 `SESSION_COOKIE_SECURE`）。hub 全部 `SetCookie` 的 Secure 位统一走 `a.config.CookieSecure`。
  - dev 密钥：openssl 生成 Ed25519 对 → `bin-dev/keys/sso.priv.pem`（平台签票，`AGENCY_SSO_PRIVATE_KEY_FILE`）+ `sso.pub.pem`（hub 验票，旧公钥备份为 `sso.pub.pem.bak`）。新增 `dev-recreate.sh` 以相同 port/网络/挂载/环境重建两个 dev-run 容器并注入 SSO env（平台还加 `AGENCY_SSO_KEY_ID`、`AGENCY_SSO_ALLOWED_ORIGIN`）。
- 验证：
  - 单测新增：`TestAgencySSOPageOriginAllowlistAndPayload`（未配置 503 / 缺 state_hash 400 / 非白名单 403 / 白名单 200 且含签票 fetch+postMessage 接线）、`TestIndexEmbedsPlatformBaseURLAndSSOWiring`（index 注入平台地址与 `sso/start`、`sso/callback` 接线）。`pkg/agencyhub` 全绿、`go build ./...` 通过、go.mod 无改动。
  - 实时验证（真实 dev 容器）：平台 SSO 页白名单 200/非白名单 403；hub `sso/start`+伪造票 → `invalid_ticket`（证明 `sso.pub.pem` 已加载）；再用 `sso.priv.pem` 真实签名 + 临时 root 用户/会话（`users`+`user_sessions`）跑通全链路：callback 成功 → `/agency/api/v1/auth/me` 200 `actor_type=root`。测试后已删除临时用户/会话/票据记录与脚本（残留 0）。期间确认“源会话必须真实存在”是 `validateRootSourceSession` 的正常防伪约束（此前 401 即为此正确行为）。
  - 注意：重建平台容器后进程内 `SessionSecret` 变化，用户需在 :3000 重新登录一次；随后打开 :3201/agency 即自动进入（Root 代管模式）。
- 尚余（均需用户决策，同前）：生产 DB 只读核实、Go/No-Go 终审、提交/推送、按“佣金→提现→导出”开生产财务开关。

### 21.1 修复合订（2026-09-12）：SSO 桥接页被 `X-Frame-Options: SAMEORIGIN` 拦截
- 现象：主平台登录后打开 `:3201/agency` 仍停在登录页（不自动进入）。
- 根因：平台 SSO 桥接页 `AgencySSOPage` 误设 `X-Frame-Options: SAMEORIGIN`，而设计要求它被 hub 的**跨源**隐藏 iframe 嵌入 → 浏览器拒绝渲染 iframe，`postMessage` 票据永远到不了 hub 页，自动登录静默失败并回退登录表单。
- 修复：改用 `Content-Security-Policy: frame-ancestors <AGENCY_SSO_ALLOWED_ORIGIN 白名单>`（精确列出可嵌入的 hub origin），移除 XFO；`respondAgencySSOPageError` 同步处理。hub 首页同时把 SSO 失败原因显示出来（如“主平台 SSO：a browser session is required”= 需先在 :3000 登录）。
- 回归：`TestAgencySSOPageOriginAllowlistAndPayload` 改为断言 CSP 头存在且 XFO 为空；`pkg/agencyhub` 全绿；`git diff --check` 通过。重建两二进制并重启两个 dev-run 容器，实测响应头 `CSP: frame-ancestors http://127.0.0.1:3201 http://localhost:3201`、无 XFO，平台 :3000 200、hub ready。

### 21.2 修复合订（2026-09-12）：桥接页签票需携带平台 Bearer 令牌
- 现象：上一轮修掉 XFO 后，打开 `:3201/agency` 仍停在登录页，表单上方显示“主平台 SSO：无权进行此操作，access token 无效”。
- 根因：new-api 仪表盘鉴权不走 Cookie，而是 `Authorization: Bearer <token>` 头（`middleware/auth.go` 的 `classifyDashboardCredential` 只读该头）。桥接页 JS 的 `fetch('/api/agency/sso-ticket')` 没带头 → `RootAuth` 判定无凭证 → `service.ErrAuthTokenInvalid`。
- 修复（`controller/agency_sso_page.go`）：桥接页 JS 先读 `window.localStorage.getItem('new_api_access_token')`（前端请求拦截器在 `web/src/lib/http-client.ts:150` 已把 in-memory token 镜像到该 key，按 origin 隔离，:3000 iframe 可读），取不到则 postMessage `{ok:false,error:'主平台未登录，请先打开主平台登录，再重试进入代理商中心'}`；取到则以 `Authorization: Bearer <token>` 携带同源 POST 签票。
- 回归：`TestAgencySSOPageOriginAllowlistAndPayload` 增断言 JS 含 `new_api_access_token` 与 `'Authorization':'Bearer '+token`；`go test ./controller -run TestAgencySSOPage` 通过。`./dev-backend-rebuild.sh` 重建 `new-api` 并重启 `new-api-dev-run`（200），`curl` 桥接页确认 JS 已带令牌逻辑（hub 无需重编）。
- 注意：重启平台后内存 `SessionSecret` 变化，用户需先在 :3000 重新登录刷新 `new_api_access_token`，再开 :3201/agency。
