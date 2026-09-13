# 代理商旁路服务：网关联动与财务契约审查

- 审查日期：2026-09-12，Asia/Shanghai。
- 需求基线：`pkg/doc/05-agency-hub-sidecar-design.md`，重点覆盖 §2、§6～12、§22.1 财务验收场景。
- 审查对象：当前 Windows 工作区中的代码及既有未提交改动；不是对生产运行版本的认证。
- 方法：读取设计文档、项目规范及 `pkg/billingexpr/expr.md`，追踪真实网关调用链，运行隔离测试，并增加明确断言设计契约的可复现验收探针。
- 操作范围：未修改生产实现、未部署、未访问生产模型、未执行生产充值/退款/提现或其他财务操作。

## 1. 审查结论

**网关已经接入部分代理商逻辑，但没有完成设计文档要求的财务闭环，当前不能判定为可正式开通代理商计佣与提现。**

已有真实集成包括邀请客户注册、durable 用户标记、部分报价倍率覆盖、钱包与 Token 联合预扣、支付到账资金投影、最终事件及佣金消费者。缺口不限于尚未进行生产验证：本机新增的 5 个设计验收探针全部失败，直接复现了预扣缺少持久 journal、禁用代理商阻断客户、非计佣扣款资金次序错误、Realtime 成本/佣金错误、无限额 Token 累计越界。

本报告区分“已实测失败”“代码审查确认缺失”和“尚待集成或并发验证”。既有测试通过，只能证明其实际断言覆盖的行为，不能替代整个需求验收。

## 2. 新增验收探针及实际失败结果

文件：`service/agency_design_audit_test.go`。使用独立临时 SQLite 数据库，fixture 恢复全局数据库引用及 Redis 开关，不使用真实密钥或真实账户。

文件带 `//go:build agency_audit`。这组用例专门保留本次审查发现的未满足契约，默认测试不运行；它们断言目标行为，不把当前错误行为改成通过条件。

复现命令，在仓库根目录运行：

```powershell
go test -tags agency_audit ./service -run '^TestAgencyDesign' -count=1 -v
```

2026-09-12 23:17 左右的实际运行结果：5 个测试失败，进程退出码 1。

| 测试与定义位置 | 设计要求 | 实际结果 |
| --- | --- | --- |
| `TestAgencyDesignReservePersistsAcceptedJournal`，`service/agency_design_audit_test.go:56` | §8.4：预扣与报价接受同时持久化 journal，先于上游请求 | 用户额度从 200 扣到 100，预扣函数返回成功，但 charge 的 journal 查询返回 `record not found` |
| `TestAgencyDesignDisabledAgencyKeepsCustomerCharging`，`:70` | §3.2、§9.1：禁用代理商不影响客户使用最后销售价，只停止新请求计佣 | 提供最新 state revision 且 `CommissionEligible=false` 的快照，预扣仍报 `agency pricing snapshot is stale` |
| `TestAgencyDesignNoncommissionableDebitUsesPaidFirst`，`:80` | §2.4、§8.6：订阅购买等真实钱包扣款也按付费优先 | paid=100、nonpaid=100，扣 50 后实际为 paid=100、nonpaid=50；要求是 paid=50、nonpaid=100 |
| `TestAgencyDesignRealtimeCostUsesOriginalBasis`，`:89` | §2.1：Q=1000、S=0.9、C=0.75 时 B=900、T=750、G=150 | 实际 T=675、G=225；把已应用 S 的 B 再作为成本基准 |
| `TestAgencyDesignUnlimitedTokenPreservesInt32Bounds`，`:101` | §8.6、§22.1：无限额只绕过额度可用性，不绕过存储边界 | 函数返回成功，`used_quota=2147483657`、`remain_quota=-2147483657`，超出 int32 范围 |

这些失败不是生产数据异常推断，而是使用确定输入、调用真实业务函数后得到的结果。

## 3. 十类主要财务缺口

### G01：预扣、最终结算与退款尚未成为单一持久财务事务

**要求：** §8.3～8.4、§22.3 要求 reserve/finalize/cancel 同步提交用户钱包、Token、资金 lot、journal、operation、outbox/delivery；预扣为 0 也应建立 journal，进程崩溃后依靠持久状态恢复。

**证据：**

- `service/billing_session.go:28` 仍使用原 `BillingSession`；全仓未发现设计要求的 `AgencyBillingSession` 实现。
- `service/billing_session.go:48` 的 `delta == 0` 分支直接设置 settled 后返回。外层后续确实会调用事件函数，但该调用不属于钱包事务，也没有把预扣状态变更为最终状态。
- `model/agency_funding.go:601` 的 `tryReserveAgencyWalletAndToken` 更新 User、Token、账户和 allocation，没有写入 reserved journal、报价快照、对应 operation/outbox。
- `service/billing.go:90` 先调用 `relayInfo.Billing.Settle(actualQuota)`，`:94` 再调用 `RecordAgencyBillingEvent`。
- `service/agency_gateway.go:463` 中 `RecordAgencyBillingEvent` 另开事务，仅覆盖 journal、operation、outbox、delivery。
- `service/billing_session.go:143` 后的 Refund 保留异步执行方式，资金释放和补偿事件分开执行。

**影响：** 预扣成功后进程退出，缺少设计指定的 accepted quote/journal 恢复依据；钱包已提交但事件事务失败时，现有 outbox 局部回滚测试不能证明整笔财务原子性。

**验证：** `TestAgencyDesignReservePersistsAcceptedJournal` 已实测失败。既有 `TestRecordAgencyBillingEventRollsBackJournalWhenOutboxConflicts` 通过，但它只证明后半部分 journal/outbox 事务回滚。

### G02：视频与旧任务在提交阶段过早标记财务最终成功

**要求：** §8.1、§8.5、§22.1 要求上游提交前保留 public task/submit attempt；成功提交不入佣金，必须等待任务成功且账单最终确认；未知提交不能猜测失败或盲目重发。

**证据：**

- `controller/relay.go:578` 调用 `relay.RelayTaskSubmit`，成功后在 `:604` 立即调用 `service.SettleBilling`。
- `service/billing.go:94` 写入业务状态 `success`；`service/agency_gateway.go:447` 固定生成 `BillingStatus: "finalized"`、`FinancialFinal: true` 的事件。
- 任务到 `controller/relay.go:609` 才初始化，`:637` 才 `task.Insert()`；插入失败仅记录日志。
- `model/agency_models.go:730` 定义 `AgencyTaskSubmissionAttempt`，业务目录搜索只有模型定义及迁移引用，没有实际提交尝试的创建/更新调用。
- model/service/controller/relay 的业务代码未找到 `reconcile_required` 状态实现。
- `service/midjourney.go:127` 同样在提交后扣款阶段调用 `RecordAgencyBillingEvent(..., "success")`。

**影响：** 不能保证“对外确认任务时已有持久任务 ID”；任务最终失败、用量迟到、账单金额不变以及提交超时，都没有达到文档的生命周期契约。此项属于调用链审查确认，未调用付费上游制造生产任务。

### G03：禁用、改价、转移与在途报价的排序契约不完整

**要求：** §3.2、§7.4～7.5、§9.1 要求禁用不阻断已有客户；首次接受报价后，重试、补扣与长任务沿用已接受版本。

**证据：**

- `service/agency_gateway.go:320` 附近可以生成禁用机构的 `CommissionEligible=false` 快照，这是正确的部分实现。
- `model/agency_funding.go:727` 却强制机构 `Status == "active"`，即使快照已是最新禁用版本仍拒绝。
- `model/agency_funding.go:625` 在每次 reserve/追加扣额时验证当前绑定、机构与政策版本；`service/billing_session.go:64` 的最终补扣也调用该方法。
- 没有 G01 所要求的已接受报价 journal，因此不能区分“首次接受新报价”与“旧报价的后续结算”。
- 实际预扣锁顺序从 `agency_funding.go:608` 的用户开始，再到资金、归属和机构；转移路径按机构→归属锁，与文档统一顺序不一致。

**影响：** 禁用会使客户无法正常预扣，已实测；改价/转移后在途补扣可能被视为 stale，属于明确代码路径风险。跨数据库死锁风险尚需真实并发测试验证。

### G04：结算系数 C 仍使用独立统一舍入公式，完整 billing basis 未保存

**要求：** §2.1、§8.3 要求同一原引擎 basis 分别计算 B/T，保留各路径截断、min1、round 和中间量化；不能以量化整数 Q 代替完整 basis。

**证据：**

- `controller/agency_pricing.go:65`、`:72` 只运行标准倍率 1 与销售倍率 S，没有用 C 运行同一引擎。
- `service/text_quota.go:444` 起将实际用量重算为整数标准 quota。
- `service/agency_gateway.go:378` 附近通过 `agencycontract.Calculate(standard, policy, 0, true)` 求成本。
- `pkg/agencycontract/contract.go:247`、`:264` 对整数 quota × BPS 统一 round，不能表达所有原路径的舍入合同。
- `AgencyBillingBasis` 未发现业务赋值；`service/agency_gateway.go:428` 仅在缺省时组织简化 map，缺 normalized usage、表达式源码/版本/实际依赖参数与时间、工具组件、视频规格和真实 rounding policy。
- Realtime 的 `service/agency_gateway.go:68` 直接 `standard := quota`，把客户已付费用当作未应用 S 的标准费用。

**验证：** Realtime 黄金值探针实测 T=675、G=225，正确目标为 750、150。既有 `TestRecordAgencyBillingEventUsesActualStandardQuota` 通过，仅覆盖整数标准 quota 的局部场景，不能证明全路径成本一致。

### G05：Realtime 与渠道重试不能保证冻结销售系数只应用一次

**要求：** §7.3～7.5、§8.5、§22.1 要求路由分组只决定渠道；重试和 Realtime 计费不能改变接受的 S，分段钱包与事实同事务。

**证据：**

- `relay/channel/openai/relay_realtime.go:343` 先调用 `PreWssConsumeQuotaWithResult`，`:349` 再写 segment journal，存在两个事务的中断窗口。
- `service/quota.go:131`、`:142` 的 Realtime 预扣重新读取分组折扣，没有使用冻结的 `AgencyPricing.SalesBPS`。
- `controller/agency_pricing.go:59` defer 清理请求倍率 override；`controller/relay.go:334` 后续渠道选择又调用 `helper.HandleGroupRatio`。
- `relay/helper/price.go:77` 仅识别临时 context override，没有读取已经附在 RelayInfo 上的 AgencyPricing。

**影响：** 路由切换可能覆盖 S；Realtime 部分入口可能仍按路由组收费。既有分段 hash 测试没有真实钱包扣费/路由变更断言，不能覆盖这些行为。

### G06：资金 lot、收费组件、欠费及累计边界未形成完整权威账

**要求：** §2.4、§8.4、§8.6 要求 paid-first，预扣 reserved 与 consumed 分开，费用组件确定性分配，欠费可追溯、后续入账先还债，所有累计字段校验存储边界。

**证据：**

- `model/agency_funding.go:1687` 后的 reserve 直接增加 `PaidConsumed`，未按设计先移到 `PaidReserved`。
- `:1705`、`:1734`、`:1753`、`:1767` 分配统一使用 `ComponentID: "default"`，没有完整收费组件按最大余数分配矩阵。
- `:1774` 仅更新 account，没有对应 reserve funding ledger/outbox。
- `ApplyAgencyQuotaDeltaTx` 在 `:211` 先扣 nonpaid，订阅购买/管理扣款因此违背 paid-first。
- 正向赠额分支 `:203` 不先抵 debt；若负向变化产生债务，只增加汇总数，没有完整 debt 来源明细。
- reserve 的欠费分支也没有创建对应 `AgencyFundingDebt`；`RecordAgencyTopup` 后续还款却依赖债务明细匹配，否则返回 `agency funding debt repayment mismatch`。
- 正常生产补扣使用的 reserve 在 `:643` 先以剩余额度检查拒绝，未实现最终用量超出钱包时记录 debt 的合同。
- 网关 model/service/controller/relay 中没有 `ReconcileBlocked` 准入检查/设置；hub 另有不被网关调用的资金方法，不能代替网关权威入口。
- `model/agency_funding.go:662` 直接在 SQL 中累计 Token remain/used，不验证更新后的 int32 上下界。

**验证：** paid-first 与无限额 Token 累计边界两个探针均实测失败。其他项为代码审查确认的结构缺口；欠费组合故障仍需进一步确定性集成用例。

### G07：退款有局部累计金额实现，尚未实现完整资金与组件冲正合同

**要求：** §9.3 要求每个费用组件按累计 R 求恢复 paid/nonpaid/debt 与冲回 K/M，绑定永久退款操作，资金恢复与冲正事件同事务，拒付后还债再取消可恢复真实还款来源。

**已实现部分：** `service/agency_gateway.go:660` 后以 journal 累计退款 quota 计算累计冲回 micros，已有多次小额/最终全退测试通过。

**缺口证据：**

- 退款函数输入缺稳定业务退款 operation key，`service/agency_gateway.go:683` 每次生成新事件 ID；不能由该函数独立识别同一外部退款操作重放。
- 计算基于原总费用，缺每个收费组件的累计退款分摊。
- `model/agency_funding.go:1806` 按 allocation 倒序释放，适合预扣少扣释放余量，却不能代替 finalized 售后退款所需的累计按来源比例恢复。
- `service/task_billing.go:231` 钱包变化完成后再写退款事件，不是同一事务。
- `model/agency_funding.go:1912` 附近若原拒付债已被后续充值偿还，OutstandingQuota 不足会直接报错；未看到完整沿 repayment 恢复新资金来源的分支。

**限制：** 现有测试证明部分金额累计与拒付操作可用，未证明整个 §9.3，不能据此宣称退款闭环完成。

### G08：财务事实缺少真实用量和充值实付数据

**要求：** §8.3、§11.3、§12 要求事实保留白名单用量、视频规格、充值实付/到账分列，关闭消费日志后仍可完整报表。

**证据：**

- `pkg/agencycontract/contract.go:173` 的 BillingEvent 缺输入/输出/cache、视频规格、完整资金组件字段。
- `pkg/agencyhub/finance_service.go:382` 生成普通 UsageFact 没有赋值 InputTokens/OutputTokens/CacheReadTokens/CacheWriteTokens，这些字段默认 0。
- Realtime 有另外的 usage fact 写入，不能代表 Chat/Responses/音频/图像/视频均已覆盖。
- `model/topup.go:124` 各真实支付接入统一传 `paid=creditedQuota, bonus=0`，接口不包含实际支付金额/币种或独立赠送额度。
- `model/agency_funding.go:1097` 创建 TopupFact 没有写实际支付金额及实际支付币种。
- `pkg/agencyhub/finance_service.go:263` 对非 reversal 的资金事件也调用 recordUsageFact，包含 topup/funding_adjusted；需按事件类型排除对模型调用统计的污染。

**影响：** “接口有列表”与“列表能显示完整真实数据”不是同一验收结论。现有字段定义和合成 fact 测试不能证明生产事件确实填充数据。

### G09：事件幂等、消费者核验及负佣金恢复仍有缺口

**要求：** §9.2、§9.4、§12、§16 要求从原 journal 核验冻结结果、业务操作幂等、完整 event_count 与永久回执，负余额可通过后续正常佣金逐步补足。

**证据：**

- `service/agency_gateway.go:447` 每次调用 Record 重新生成 OccurredAtMS，并参与完整 payload hash；`:474` 比较 hash 时未排除此动态字段。同业务参数稍后重放可能被误判冲突，此项为代码审查风险，未引入依赖 sleep 的时间测试。
- `pkg/agencyhub/finance_service.go:196` 的消费者核对 payload/source 重复，没有从原 journal 核验 revision/B/T/P/K。
- `:242`～`:255` 只比较前序 outbox/source 数量，没有核对每个 operation 的 event_count/event_index 集合。
- 由于 reserve 本身没有 outbox，后序数量检查不能证明所有钱包动作都已产生事实。
- `finance_service.go:336` 拒绝正向佣金入账后仍为负的余额。例如 available=-100、新赚50，目标应为-50，代码会返回错误，阻止正常赚取佣金偿还负余额。

**已验证部分：** 既有幂等事件、payload 冲突、毒事件、租约 fencing、低 ID 晚提交和冲正先到测试提供局部有效覆盖；本报告没有把它们等同完整资金生命周期验收。

### G10：邀请注册有真实联动，注册重试与完整 provisioning 屏障未完成

**要求：** §6 要求零初额真实用户、默认 Token 同事务、注册幂等、旧用户跨实例流式/Realtime/异步任务/Batch 全部排空，最终绑定前复核 Root 授权。

**已实现证据：**

- `controller/user.go:350` 在同事务中执行 InsertWithTxAgency、BindUserByInvite、默认 Token 创建。
- `controller/user_agency_invite_test.go` 对有效邀请、无效/禁用邀请回滚、旧 aff 冲突和重复账号有测试，当前已运行通过。
- `pkg/agencyhub/agency_service.go:697` 后转移实现按机构 ID 锁排序，结束旧 binding、创建新 binding 并切换 active 指针，保留历史归属。

**缺口证据：**

- `controller/user.go` 无 `Registration-Idempotency-Key` 处理或永久结果；提交后丢响应的注册重试不能返回原成功结果。
- `pkg/agencyhub/provisioning.go:45` 的 blocker 只查询通用 Task，未覆盖跨实例活动流式、Realtime、旧 Midjourney、Batch 排空。
- `provisioning.go:210` 最终提交核验用户版本和 fencing，但未看到源 Root Session 在最终绑定事务重验，job 只保留 RootActorID。
- `agency_service.go:572` 后正确将旧余额视为 openingNonpaid，但创建 opening lot 时仅设置 BonusInitial，没有设置 BonusAvailable，依赖 aggregate fallback，缺完整 lot 守恒。

**影响：** 不能把“Task 等待测试通过”解释为所有网关在途工作已排空。转移本身有实现，但 G03 的旧报价补扣冲突仍影响在途请求。

## 4. §22.1 十三项财务验收逐项结果

| 必测场景 | 已有实现/测试证据 | 本次判定与缺口 |
| --- | --- | --- |
| 原用户+未开通；受管 S=1 每路径一致 | legacy 回归和部分报价测试存在 | 未见覆盖所有路径的 S=1 黄金用例；T 的统一 round 不符合所有原路径，未通过全项验收 |
| 输入/输出/cache/tool、表达式/固定价、图像/视频 | S 倍率已有接入，整数 Q 事件有局部测试 | 完整 basis、usage、component 缺失；Realtime 成本失败 |
| Q=0、0用量、非法数量/NaN/Inf/溢出 | 原项目安全边界测试存在，契约零值测试通过 | 不能外推所有代理商路径；累计 Token 边界实测失败 |
| 多在途补扣超原字段范围 | 部分单次 quota 检查 | 未形成 reconcile_blocked/待结算证据闭环；不能通过 |
| 同机构两用户、同用户两 Key 并发 | SQLite 分支通过 | SQLite 该用例顺序执行；MySQL/PG 未配置且跳过，不是真实三数据库并发通过 |
| 预扣100最终100/60/150、预扣0 | 部分资金调整测试通过 | reserve journal 缺失，delta0 后事件另事务，原子生命周期不通过 |
| 赠额/签到/人工补单/真支付/订阅购买/违规费 | 多种入口已有 durable 集成 | paid-first 实测失败，lot/debt 明细尚不完整 |
| 各支付商 Amount/Money、回调重放 | 支付入口传已算 creditedQuota；资金操作重复/冲突测试通过 | bonus 固定0、实付字段缺失；未证明每个支付商真实契约 |
| Model mapping、重试、Token 路由组 | origin model 精确匹配、SHA-256 键测试通过 | 重试/Realtime 可能覆盖冻结 S；完整路径未通过 |
| 订阅优先/钱包回退 | 原订阅路径与部分测试保留 | 受管完整原子事实与真实用量未证明，不能只据旧订阅测试判通过 |
| Token 无限额/禁用/删除后在途结算 | 联合 User/Token 预扣和部分旧任务测试 | 无限额累计越界失败；受管补扣读取已删除 Token 会失败，缺专用历史结算合同 |
| Realtime 重复累计帧/多个成功段/失败尾段 | 分段 hash 与重复事件单元测试通过 | 实际成本失败；钱包和事件非同事务，路由倍率与全链重复扣费未证明 |
| 异步提交成功/超时/双轮询/账单金额不变 | 原任务和账单 reconcile 代码存在 | 提交过早 finalized、submit attempt 未使用、未知状态未实现，不通过 |

黄金 B=900、T=750、P=600→K=100 在纯契约测试中通过；不代表网关实际会为所有协议生成这组正确 B/T/P，也不代表 paid 恢复和财务事务完整。

## 5. 实际运行的既有测试与边界

### 5.1 纯契约测试

```powershell
go test ./pkg/agencycontract -count=1
```

结果：通过。`pkg/agencycontract/contract_test.go` 共 6 个测试：黄金佣金、C=0 不继承、非法模型/负数、按实际收费算 paid 比例、零值/非法比例、模型名大小写/UTF-8 精确键。最后一项是键算法测试，并未连接三个真实数据库。

### 5.2 定向服务测试

```powershell
go test ./service -run '^Test(RecordAgency|AgencyTask|AgencyMidjourney|AgencyCommand|PreConsumeBillingRejectsDurable)' -count=1 -json
```

2026-09-12 23:20 的结果：以下 9 个测试全部通过，service 包通过。

1. `TestPreConsumeBillingRejectsDurableUserWithoutQuote`
2. `TestAgencyCommandWorkerExecutesProvisioningCancelWithFreshRootSession`
3. `TestRecordAgencyRealtimeSegmentIsHashIdempotent`
4. `TestRecordAgencyBillingEventRejectsConflictingFinalize`
5. `TestRecordAgencyBillingEventUsesActualStandardQuota`
6. `TestRecordAgencyBillingEventRollsBackJournalWhenOutboxConflicts`
7. `TestAgencyTaskRefundProratesAndFinalRefundUsesRemainder`
8. `TestAgencyTaskRefundUsesCumulativeCommissionRounding`
9. `TestAgencyMidjourneyRefundFallsBackToChargeOperation`

这些用例覆盖事件局部行为、局部金额算法或命令执行，不等同于外部视频/实际所有协议/完整故障恢复验证。

### 5.3 模型、注册与组合测试

执行过：

```powershell
go test ./service ./model ./controller ./pkg/agencycontract -run 'Agency|Midjourney|SubscriptionWithBalance' -count=1 -v
```

- model 与 controller 包通过；controller 有有效/禁用/无效邀请、aff 冲突、OAuth invite 拒绝、durable 缺 schema 拒绝等实际断言。
- 该过滤器下 agencycontract 输出 `[no tests to run]`，因此已另行执行 5.1，不能把过滤器空跑算成契约覆盖。
- service 组合失败：`TestSettleMidjourneyTaskBillingRequiresPersistedTask` 创建用户触发 `users.aff_code` UNIQUE 约束。
- 随后单独运行同一 Midjourney 测试通过，显示存在测试状态隔离问题，不能抹去组合失败。

```powershell
go test ./service -run '^TestSettleMidjourneyTaskBillingRequiresPersistedTask$' -count=1 -v
```

可解释该失败的代码证据：`service/quota_saturation_test.go:128` 用软删除清理先前测试用户，保留空 aff_code 的唯一索引值；后续 `service/task_billing_test.go:127` 也创建空 aff_code 用户。`truncate` 是事后清理，不能清掉进入测试前的残留。另 `task_billing_test.go:120` 把实际复数表 `agency_hub_event_deliveries` 写成不存在的 `agency_hub_event_delivery`，运行日志出现 `no such table`。

### 5.4 数据库兼容与并发

```powershell
go test ./model -run '^TestAgencyFundingConcurrentReserveIsConservedAcrossDialects$|^TestMigrateAgencyExternalDatabaseCompatibility$' -count=1 -v
```

2026-09-12 23:20 的实际结果：

- `TestAgencyFundingConcurrentReserveIsConservedAcrossDialects/sqlite` 通过。
- 输出明确包含 `dialect mysql not configured; skipped` 和 `dialect postgres not configured; skipped`。
- 这两种 dialect 使用 `t.Logf` 后 continue，未计入标准 Go 测试 SKIP 计数，审查不能只读取汇总 PASS。
- `model/agency_funding_concurrency_test.go:65` 的注释和分支明确 SQLite 顺序执行，不能宣称本机已验证真实并发资金竞争。
- `TestMigrateAgencyExternalDatabaseCompatibility` 显式 SKIP，提示设置 `AGENCY_HUB_RUN_EXTERNAL_DB_TESTS=1` 并使用可丢弃测试数据库。

没有基于历史 macOS/Colima 报告或历史 DSN 声称当前 Windows 环境三数据库测试通过。

## 6. 交付与后续验收边界

本次新增物只有验收探针和本文，保留了所有原工作区改动。探针文件已 gofmt；未为得到全绿结果而修改业务实现或放宽断言。

修复顺序应优先恢复文档的真实财务边界：accepted journal 与统一资金事务、任务提交/最终账单状态机、冻结报价与禁用行为、原引擎 B/T 和完整 basis、全来源资金/债务/退款，然后补足真实事件用量及其他统计字段。应在隔离环境重跑 5 个失败探针、全部 §22.1 场景、故障注入和真实三数据库并发，之后再讨论生产启用。

上述顺序是基于本次审查结果的整改建议，不代表这些整改已经实施。本地存在明确功能失败，当前不能将剩余工作描述成“仅需要线上验证”。
