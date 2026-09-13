# Agency Hub — Phase A–E / §22 Go/No-Go 验收审计

> **2026-09-13 当前复核结论：未完成 / No-Go。** [剩余工作清单](agency-hub-remaining-work.md) 保留 11 组、44 个稳定编号条目，现为 **42 项未关闭、2 项已关闭**（W01.02、W06.02）。19:00 后优先补齐通用 Task 首次终态财务结算、真实内部 mTLS 命令传输、命令业务与永久回执原子提交、支付退款完整证据幂等，并新增部署覆盖文件和指南。此前完成的 allocation 级债务归属、模型退款优先抵扣其他债务及客户有效销售价格继续有效。组件支付拒付交叉规则、独立 Midjourney 生命周期、长 provisioning 最终鉴权、历史日结、最小数据库权限及生产部署/恢复仍有缺口；本地验证通过不扩大为整份规格或生产通过。没有部署生产、迁移生产库或切换生产开关。

当前实施与验收边界如下；结果来自本轮实际执行的验证，历史证据继续保留：

最终后端证据：`E:/new-api-test-cache/runs/20260913-195806-8f6bdad2`（49 个含测试包、1,470 个顶层测试通过、0 失败、5 个顶层跳过；财务审计、全仓构建和目标 vet 退出码均为 0）。隔离三库最终批次、备份恢复及 Linux 构建校验值见 [验证记录](agency-hub-verification-20260913.md)。本批未修改前端，其已通过证据仍在 `runs/20260913-184416-bf8d5eaa` 及客户价格专项。两次历史 PostgreSQL JSON 初始失败现场保留，不作为当前未修复项。

| 目标 | 当前状态与证据 | 保留边界 |
| --- | --- | --- |
| 资金来源与其他债务抵扣 | W01.02 已关闭。allocation 级债务身份、paid/nonpaid 还款来源、先偿债再恢复可用额及损坏来源事务回滚，已在 `E:/new-api-test-cache/external-db/20260913-180708-2350c6b7` 隔离 SQLite/MySQL 5.7.43/PostgreSQL 16.4 完整运行通过 | 历史 pooled debt 仅在来源唯一可证明时采用，歧义安全拒绝；不再以“债务仍按来源聚合”为当前实现阻塞 |
| 客户有效销售价格 | W06.02 已关闭。宿主 `/api/pricing` 从本地政策返回客户有效销售倍率，不依赖 Hub HTTP；后端合同、真实价格组件 8 项、纯价格 4 项、身份缓存 2 项，以及 typecheck/相关 lint 通过；显示证据 `E:/new-api-test-cache/customer-pricing-display-results.json` | 不泄露结算成本/佣金政策；关闭的是有效价格展示，生产 SSO 与全部页面验收仍属其他条目 |
| 异步首次结算、退款与供应商账单 | 通用 Task 提交前冻结价格与事件版本；实际 RelayTask 受理时只保留预扣，首次明确成功/失败与钱包/Token/journal/outbox 同事务。成功待账单保留永久回执，后续用冻结依据结算；已 finalized v2 下调使用累计退款。真实控制器、适配器、本地 HTTP 上游和 worker 链已有通过证据 | W01.03 仅部分完成；独立 Midjourney、缺少冻结依据的历史任务恢复、所有供应商回调变体仍未全部验收；未知状态保持待对账，不能猜测退款或佣金 |
| 内部命令与资金操作 | 独立 TLS 1.3 双向认证监听、精确 SAN 身份、Hub 真实客户端、当前 Root/源会话核验及业务/回执同事务已实现；资金退款重放比较完整不可变证据；三库原子性与真实 mTLS 执行/结果查询已有测试 | W08.01/02 仍部分完成：浏览器 provisioning 入口未全部迁移到命令客户端，长任务最终提交鉴权和执行归属仍缺，生产证书与网络尚未部署验证 |
| 组件支付拒付 | 原 allocation 债务身份已补齐；v2 组件支付拒付检测到未支持的归属路径会完整回滚 | W01.01 仍未关闭：缺组件撤回细目、与模型退款交叉水位、精确佣金补偿及全部顺序验收；`AGENCY_COMPONENT_BILLING_ENABLED` 默认关闭 |
| 对账 | 新增原 finalize operation、不可变组件结果、journal/资金矩阵/allocation/lot 身份及累计模型退款水位证据。手动/普通定时扫描标明 `current_state_per_page`；历史 cutoff/daily 明确失败，防止以当前余额认证过去日结 | W03.01 仍未实现历史状态重建；W03.02/04 仅部分完成，debt/repayment/资金账本全链与支付拒付交叉核对仍缺；新增代码不由较早全量 PASS 自动覆盖 |
| 发布与生产 | 新增 [内部命令部署指南](../deploy/agency-hub/COMMAND-TRANSPORT.md)、专用内网 Compose 覆盖文件和环境示例；真实 Compose 合并保留原网络/环境/数据卷且不发布 3443；最终 Linux amd64 两个程序编译通过 | 最小权限、证书轮换与实际网络、最终镜像运行、生产 SSO/恢复与容量尚未完整验收；隔离数据库备份恢复不等于生产备份，无生产部署结果 |

以下保留各历史批次原始结论及其更正，便于追溯；其中的 ✅、测试数量、环境和“全部通过”只属于该历史记录，不能覆盖上表的当前状态。

> **2026-09-13 较早批次分项财务记录（历史）：** 已接入 v2 收费分项的资金矩阵、累计退款、精确 ID 佣金投影及真实网关→Outbox→Hub→报表测试；默认自动生产 v2 仍关闭。当时记录“组件支付拒付、退款抵扣其他未清债及全部旧异步退款调用方迁移尚未完成”；其中其他债务抵扣已由本页当前状态更新，组件支付拒付和全部异步生命周期仍未完成。逐批证据见 [验证记录](agency-hub-verification-20260913.md)。

> **2026-09-13 较早批次页面记录（历史）：** 已补齐管理、导出、客户归属、异常付款恢复和受控对账复核页面；对账修复具有权威证据、Root 二次验证及事务审计。当批实际执行了六条浏览器流程，以及隔离数据库的迁移、并发和备份恢复专项。功能边界和逐批证据见 [验证记录](agency-hub-verification-20260913.md)。下文历史的“全部通过”不代表当前完整业务验收。

> **2026-09-12 复核更正：本文以下内容作为历史记录保留，其中“Phase A～D 完成”“本机可闭合全部通过”“只剩窄缺口”不再作为当前结论。** 本次重新核查真实页面、网关财务调用与生产部署后，已复现多项需求验收失败；本机外部 MySQL/PostgreSQL 测试也未运行。当前结论为 **未完成 / No-Go**，以 [需求逐项复核与实际验收记录](agency-hub-requirements-audit-20260912.md) 及其中原始测试证据为准。旧环境的测试/基准不能推定当前生产能力或全部需求通过。

> 生成：2026-09-12 第六轮。把设计基线 `agency-hub-sidecar-design.md` §22 与 Phase A–E 的每项验收逐一映射到当前代码/测试/实测证据。
> 状态口径：`✅ 完成并验证` / `🟡 本机已验证、生产待实测` / `⏳ 需用户决策或外部环境` / `⚠️ 缺口未闭环`。
> 关联文档：`docs/HANDOVER.md`（历轮记录）。

## Phase A：代码边界、契约与基础设施 — ✅

| 验收 | 证据 |
| --- | --- |
| 所有增减 quota/Token/充值/订阅/Realtime/MJ/视频路径接入调用图 | `docs/HANDOVER.md` §19 计费路径覆盖审计：标准 relay 统一经 `controller/relay.go`→`service.SettleBilling`→`BillingSession`；任务平台经 `relay/relay_task.go`→`model/task_billing_atomic.go`；MJ 走 `service/midjourney.go` `*WithSequence`；Realtime 按段 `RecordAgencyRealtimeSegment` |
| 契约/迁移/权限/财务幂等/跨库测试骨架 | `pkg/agencycontract/contract_test.go`、`model/agency_migration_test.go`、`model/agency_funding_test.go`（幂等/冲正）、三库 gate（本轮重跑 PASS） |
| 无受管用户时原功能行为完全一致 | legacy 路径测试：`TestAdminQuotaAddLeavesLegacyUserOnQuotaOnlyPath`、`TestLegacyAgencyWalletAndTokenReservationIsAtomic` |
| 非支持计费路径不可悄悄走旧扣费 | provisioning 用户被拒：`TestCreditTopUpQuotaRejectsProvisioningUser`、`TestPurchaseSubscriptionWithBalanceRejectsProvisioningUser`、`TestSettleTaskBillingReconciliationRejectsProvisioningUser` |

## Phase B：完整资金基础与身份 — ✅

| 验收 | 证据 |
| --- | --- |
| durable 模式、资金 lot/预扣/Token 事务、充值/赠额/扣减/退款/outbox | `model/agency_funding.go` + `agency_funding_test.go`（topup 幂等重放、冲正建债、混合 paid/bonus 分配与 outbox、debt 偿还）、`model/agency_quota_entry_test.go`（充值/订阅/补签/管理扣减投影） |
| 余额可恢复且守恒、并发不重复分配 paid | `TestAgencyFundingConcurrentReserveIsConservedAcrossDialects`（sqlite/mysql/postgres 全 PASS，本轮重跑） |
| delta0/trust/重试/删除 Token/中断事务合同测试 | `TestAdjustAgencyChargeSettlesDurableDeltaAccurately`（预扣100→最终100/60/150、多退少补与 debt）、`TestReleaseAgencyWalletAndTokenIsAtomic`、`TestLegacyAgencyWalletAndTokenReservationIsAtomic`（Token 记账失败整体回滚） |
| 独立账号/SSO/机构/整包价格/Root 高风险验证 | `pkg/agencyhub/sso_contract_test.go`（错 aud/过期/单用/state 错/源会话撤销降权/proof 单用 body 绑定）、`agency_command_test.go`（签名命令幂等重放） |

## Phase C：注册、价格和所有模型路径 — ✅（价格快照细节见合同测试）

| 验收 | 证据 |
| --- | --- |
| 原子邀请注册/零额度/归属/资金/默认 Token 同事务、无 aff 叠加 | `pkg/agencyhub/agencyhub_test.go` 注册/绑定/ack 幂等；`docs/HANDOVER.md` §4 |
| 旧用户 provisioning/转移/禁用、防 stale 提交 | `TestProvisioningFencingTokenPreventsStaleCommit`、root transfer/disable 用例 |
| 版本检查/快照、客户端有效价格 | `pkg/agencycontract/contract_test.go`：`TestCommissionGoldenCase`（B=900/T=750/P=600→K=100）、`TestExplicitZeroSettlementDoesNotInherit`、`TestInvalidModelNameAndNegativeChargeRejected`、`TestModelKeyIsCaseAndUTF8ExactAcrossDatabases`（§22.2 大小写/UTF8 跨库一致） |
| 文本/协议/音频/图片/表达式/视频/旧任务/Realtime 逐路径接入，未知状态 reconcile_required | 计费覆盖审计（见 Phase A）；Realtime：`service/agency_realtime_test.go`；任务结算：`TestSettleTaskBillingReconciliationRejectsProvisioningUser`。⚠️ reconcile_required 的显式集成测试未见专项文件，属窄缺口 |

## Phase D：佣金、报表、退款对账 — ✅

| 验收 | 证据 |
| --- | --- |
| 事件幂等、佣金、多币种、部分退款/拒付、充值记录、日聚合 | `TestCommissionGoldenCase`、`TestCommissionForPaidUsesFinalChargedQuota`（含半退语义）、`TestReverseAgencyTopupCreatesDebt...`、`TestChargebackDebtIsCleared...`、`TestReverseAgencyTopupMixedPaidBonusAllocationsAndOutbox`、报表聚合 `TestReportSummaryMergesUsageAndCommissionsDeterministically` |
| 消费者停机/重复/乱序/低ID晚提交/毒事件都不重不漏 | `pkg/agencyhub/agencyhub_test.go`：`TestConsumerProcessesLateLowIDDeliveryAfterHigherIDDone`、`TestConsumerReversalBeforeOriginalSettles`、`TestConsumerPoisonsMalformedAndUnknownSchemaEvents`、`TestConsumerPoisonsOutboxPayloadHashMismatch`、`TestConsumerLeaseFencePreventsStaleWorkerFinancialCommit` |
| 转移/退款历史归属正确 | `TestReverseAgencyTopupReversesDebtRepaymentFromTheSameFundingLot`、root transfer 用例 |

## Phase E：提现、导出、容量和生产启用 — 🟡

| 验收 | 证据 |
| --- | --- |
| 收款账户加密/版本/防篡改 | `TestWithdrawalPayingGateRequiresImmutableAccountSnapshot`（账户快照哈希）、AES-GCM+AAD |
| 提现状态机：并发不超余额、冲正冻结 on_hold、unknown 不可重打、CAS | `TestWithdrawalReversalFreezesOutstandingRequests`、`TestWithdrawalUnknownPaymentCannotRestartOrCancelButCanBeMarkedPaid`、`withdrawal_reversal_test.go` |
| CSV 注入/换行/路径穿越/时区边界/int64 精度 | `TestExportWorkerEscapesSpreadsheetFormulaText`、`TestParseExportFilterRejectsUnknownAndInvalidValues`、`TestExportPathRejectsTraversal`、`TestDownloadExportRejectsTamperedFile`、`TestReportSummaryEndDateBoundaryIsLocalDayInclusive`、`TestCommissionLedgerReturnsSnakeCaseStringMoney`（±9007199254740993） |
| 单用户/单机构热点、排水速率基准 | `BenchmarkAgencyFundingReservePerDialect`、`BenchmarkAgencyFundingReserveConcurrentPerDialect`、`BenchmarkAgencyConsumerProjectionPerDialect`（本轮文档 §19：SQLite~43k/s、MySQL~9.9k/s、PG~7.7k/s） |
| 蓝绿兼容/回滚、备份恢复防重打 | ⚠️ 未见专项自动化测试（设计 §22.3/22.4 要求），属上线短板项 |

## §22 详细门槛

- **§22.1 计价与财务确定性** ✅：黄金用例、S 仅一次、C=0 成本 0、非法量拒绝、并发守恒、delta0/多退少补、赠额/人工补单/违规费、topup 重放、订阅/回退、Realtime 增量、异步任务持久 ID——均有对应测试（见各 Phase）。
- **§22.2 发布/身份/越权** ✅（生产端到端除外）：缓存不变量、Root 政策/价格继承、SSO 合同、注册回滚、跨机构隔离（report/export 过滤与注入测试）、临时密码、CSV/int64 精度。🟡 真实 IdP 端到端（错 aud/过期/state）+ 生产撤权需线上验证。
- **§22.3 故障恢复与提款** ✅（主干）+ ⚠️（窄缺口）：原子性/租约栅栏/低ID晚提交/冲正先到/毒事件/Redis 故障回源均有测试；缺「归档后重复投递」「journal 与 outbox 之间 crash 注入」「低层任务平台 crash 注入」专项。
- **§22.4 性能与数据库** 🟡：三库迁移+核心财务并发本轮实跑 PASS（MySQL 8.2:13306、PG 15:15432、SQLite）；存储行宽实测（funding_allocations~673B、journals~1039B、outbox~1565B、commission_ledger~727B，~20GB/天@60RPS，handover §17）；100 万积压追平时长与真实事件宽度需生产实测。
- **§22.5 Go/No-Go** ⏳：见下。

## 结论与建议

- 本机可闭合的验收项**全部通过且本轮已重跑证据**：`go build ./...`、`go test ./...`（0 FAIL）、`relaykit GOWORK=off go build ./...`、gofmt、`git diff --check`、三库 gate、本机部署（`/api/status` 200、`/agency/readyz` 200，生产财务开关保持关闭）。
- **建议方向：可以进入上线准备，但尚未 Go**。上线前必须由用户拍板并完成：① §22.3/§22.4 剩余窄缺口（归档重投、crash 注入、蓝绿回滚）的落实与测试；② 生产环境 100 万积压追平/存储预算/并发实测；③ 提交并推送当前改动；④ 上线前只读核实生产库是否有代理商数据（禁止清理）；⑤ 按顺序打开生产财务开关（佣金→提现→导出）。
- 未验证项不得以“预计支持”充数；本轮不执行真实付费/支付/提款/生产操作（§22.5）。
