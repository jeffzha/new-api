# Agency Hub — Phase A–E / §22 Go/No-Go 验收审计

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
