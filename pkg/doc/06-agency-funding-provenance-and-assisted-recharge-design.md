# 代理客户资金来源、代充二维码与用量归因设计

| 项目 | 内容 |
| --- | --- |
| 文档状态 | 核心实现稿；核心闭环已落地，扩展验收项见第 13、14 节 |
| 编制日期 | 2026-09-14 |
| 源码基线 | `f94e09465002`；首次核验 2026-09-14 10:53:20；用户 161 补充核验 11:40:50（北京时间） |
| 适用范围 | `new-api` 主站、`pkg/agencyhub` 旁路服务、`agency-web` 报表 |
| 上位设计 | [`05-agency-hub-sidecar-design.md`](./05-agency-hub-sidecar-design.md) |
| 目标读者 | 产品负责人、财务/运营、后端、前端、测试和发布人员 |

### 0.1 本轮核心实现状态（2026-09-14）

本轮已落地并通过 SQLite 回归测试的核心闭环如下：

- 事实投影与佣金结算解耦。`AGENCY_HUB_FACT_PROJECTION_ENABLED=true` 时，用量和充值事实继续同步；`AGENCY_HUB_COMMISSION_PROCESSING_ENABLED=false` 时仅将佣金任务置为 `deferred`，不会阻断用量/充值报表。
- 兑换码、管理员调额、用户自助支付、管理员代充支付均写入来源批次、操作人/受益人和充值事实；新扣费按 `redemption → admin_grant → paid` 分配，并保留同一 charge 的多来源 allocation。
- Root 用户管理抽屉提供代充入口，金额使用十进制字符串校验，支付回调校验原订单金额并保持幂等；代充结果同时提供收银台 URL 和二维码。
- Agency 用量接口返回 `paid_quota`、`nonpaid_quota`、`debt_quota`、`funding_breakdown` 以及事实/佣金同步状态；充值接口返回统一的四类产品来源和代充发起人。

本节只标记已经提交到源码的核心范围。三数据库迁移、生产历史事件重放、真实支付商户验收、全站钱包 v2 迁移和 CSV/收银台状态机等文档中列出的扩展验收，仍需按第 13、14 节单独完成，不得因为本轮核心测试通过而直接宣称全部设计项已经上线。

## 1. 结论

**可以确定：系统并没有“只有扫码充值消费才展示调用用量”的业务规则。** 源码中，不可计佣金的消费也会生成用量；现有回归用例 `TestBillingEventRecordsUsageFactForNonCommissionableUsage` 覆盖了这个契约（本轮阅读源码，未重新执行该测试）。

**用户补充确认应检查 ID 161 后，已找到与问题吻合的完整证据：161 有管理员加额、兑换码入账和一次成功调用；主站扣费记录与余额变动一致，代理用量投影未执行。** 其调用事件仍停在 pending，Hub 的 `AGENCY_HUB_COMMISSION_PROCESSING_ENABLED=false` 关闭了包含用量投影的 consumer。由此可以确认：本例没有显示调用用量的直接原因是同步处理停用，不是必须扫码充值。本轮没有重新核算该模型的原计价及折扣算法，余额守恒不等于价格已重新验收。

此前检查的客户 160 和 `test091402`（ID 162）是其它用户，在首次核验时没有消费记录；历史用户 149 是另一个存在事件积压的样本。以下保留它们作为诊断背景，不混淆为领导实际测试用户。

### 用户 161 的核验结果（领导实际测试样本）

| 项目 | 2026-09-14 11:40:50 只读结果 |
| --- | --- |
| 当前归属 | agency 2，binding 4，`agency-durable-v1` |
| 管理员加额 | `quota_grant` 1 笔，684932 quota |
| 兑换码到账 | `redemption` 1 笔，684932 quota |
| 实付充值 | paid=0；没有支付 lot |
| 成功调用 | 1 次，模型 `Hunyuan/hy3`，token ID 54 |
| 输入/输出 Tokens | 49075 / 697 |
| 成功调用扣费 | 1916 quota；journal 和主站 consume log 一致 |
| 当前总余额/非付费余额 | 均为 1367948 quota，debt=0 |
| 取消调用 | 另有 1 条 cancelled journal，最终扣费 0，不应当成成功消费 |
| 待同步事件 | 6 条 pending：funding_adjusted 2、billing_reserved 2、billing_finalized 2；attempts 全为 0（finalized 类型中包含取消结算事件，须看业务状态） |
| 代理用量/充值事实 | usage facts=0；topup facts=0 |
| 来源批次 | funding lots=0；当前只能证明两类入账，不能从既有无 lot 分摊反推出本次消耗了哪一笔 |

```text
684932（管理员）+ 684932（兑换码）- 1916（真实消费）= 1367948（余额）
```

上述数值都是内部最小额度单位 quota，不是人民币元。成功调用的关联 ID 为 `202609140044212473605798268d9d6mHhYXC0B`，主站消费日志 ID 为 7971；文档没有包含该 token 的 API key。**调用真实发生且总余额对得上；来源扣费追溯和代理报表展示仍有缺口。** 本次只读检查没有修复或重放这些记录。

2026-09-14 对正式库的只读核验结果如下：

| 检查项 | 结果 | 说明 |
| --- | --- | --- |
| Blue、Green、Agency Hub | healthy | 镜像为同一发布版本 |
| `AGENCY_HUB_COMMISSION_PROCESSING_ENABLED` | `false` | 普通 billing consumer 不运行 |
| 用户 149 | `agency-durable-v1`，quota 684921 | 当前代理绑定 agency 1 |
| 用户 149 的 finalized journal | 3 条，charged quota 合计 7 | 是另一个历史样本，不能冒充截图客户 |
| 用户 149 的 delivery | 3 条 `agency.billing_finalized`、1 条 `agency.funding_adjusted` 均 `pending` | 开启 consumer 后才会处理，历史 hash/对账异常仍需单独核验 |
| 用户 149 的 usage facts/topup facts | 0/0 | 因此页面显示“暂无记录”是符合当前查询表状态的 |
| 用户 149 的主站 consume logs | 5 条，扣费合计 11 quota | 与 3 条 journal/7 quota 不完全对应，必须按 charge/request ID 逐条对账，不能直接补 5 条金融事实 |
| 用户 149 的资金汇总 | nonpaid 684923，与 users.quota 相差 2 quota | 是最小额度单位，不是 2 元；尚未修复 |
| 截图客户用户 160 | agency 2；quota/used_quota/request_count/money_seq 全为 0 | 无 ledger/journal/consume log/usage fact/topup fact |
| `test091402` 对应用户 162 | agency 2；上述计数与余额全为 0 | 与客户 160 不同；同样没有本次消费证据 |
| 对账问题 | `billing_operation` 3 条、`billing_outbox` 4 条、`funding_account` 1 条 open | 不能把 readiness=ready 当作财务已验收 |

管理员“用户→更新用户→调整额度”走 `POST /api/user/manage` 的 `add_quota`，现在通过带操作者的事务入口写入用户余额、`AgencyFundingAccount`、来源 lot、ledger、`AgencyTopupFact`、outbox 和 delivery。兑换码在 `model/redemption.go` 中使用兑换码 ID 作为不可变来源 ID，写入同一条完整事实链。两者均按 nonpaid 展示并参与用量归因，但不产生支付佣金。

`Redemption.ExpiredTime` 在兑换成功时冻结到对应 `AgencyFundingLot.ExpiresAt`；只有未过期兑换码余额计入可用总额。资金分配器已按冻结的 `FundingRuleVersion` 切换算法：旧快照保持 `paid_first_v1`，新请求使用 `redeem_admin_paid_v2`，按兑换码、管理员/其它赠额、历史未分类、已验证 paid、debt 的顺序分配。同属兑换码的批次按最早到期优先（FEFO），永不过期批次排在有期限批次之后。

本轮仅通过 SSH 执行 `docker inspect` 和开启 `default_transaction_read_only` 的定向 SQL 查询，未调用模型、创建用户、修改开关、变动余额或执行支付。核验脚本放在 E 盘临时目录，未保存 API key、密码、完整回调或请求正文。后续实时变化不由此时间点快照保证。

### 1.1 对现有空报表的修复路径

1. 用实际失败样本的请求 ID/时间确认 `logs.user_id`、`tokens.user_id`、当时 binding；不要只对比昵称。测试环境与生产分开检查。
2. 存在主站日志时，继续核对同 charge 的 journal、operation、outbox、delivery、usage fact。仅有文本日志不能证明金融事务已提交。
3. 对合法 pending 事件做独立用量投影；对于旧 hash、缺少 operation 元数据或余额不一致的事件，先建立可审计修复方案，再重放。
4. 本次不直接打开原佣金开关“试一下”，因为它还会真实更新佣金余额。解耦后关闭佣金也能显示用量。
5. 如果精确用户确实从未调用，展示真正的空状态；如果同步停用/异常，则展示同步状态，避免让用户猜测资金是不是丢失。

### 1.2 与旧设计的关系

本设计生效后替换 05 文档 §2.4、§8.6 的 **新请求资金来源顺序**，补充 §9.2 的独立投影和 §13.5/§14 的报表。不改变模型协议、渠道选择、原计价引擎、代理销售折扣算法、币种换算快照和历史佣金。`paid_first_v1` 的历史请求与原退款算法继续保留。

本文同时记录已落地核心能力和后续扩展要求。以 0.1 节和第 14 节实现状态为准；标记为扩展验收或拟新增的接口、状态机不能当作已经上线的生产能力。

## 2. 产品范围与明确规则

### 2.1 四种入账方式

| `source_kind` | 入口 | 是否外部支付 | 会计属性 | 默认是否产生佣金资格 |
| --- | --- | --- | --- | --- |
| `redemption` | 用户兑换码 | 否 | nonpaid | 否 |
| `admin_grant` | Root 在用户管理中调整额度 | 否 | nonpaid | 否 |
| `payment_self` | 用户钱包二维码 | 是 | paid（另有 bonus 子额） | 是，按原销售/结算规则 |
| `payment_assisted` | Root 为指定用户生成代充二维码，付款成功 | 是 | paid（另有 bonus 子额） | 是，和用户自付同一 paid 规则 |

“由谁创建二维码”“谁实际付款”“额度记到谁”是三个独立身份。代充创建人必须审计保存，但不能把代充误记成管理员赠额，也不能在付款前增加受益人的余额。

### 2.2 扣费优先级

新规则版本命名为 `redeem_admin_paid_v2`：

```text
可用 redemption lot（兑换码）
  → 可用 admin_grant / other_grant / payment bonus（非付费赠额档）
  → 可用 legacy_unknown（仅历史迁移过渡档）
  → 可用 paid lot（用户自付与管理员代充不区分先后）
  → debt（余额不足时按既有欠费规则）
```

同一档位内默认按 `money_seq ASC, lot_id ASC` FIFO；兑换码档位先按 `expires_at ASC` FEFO，`expires_at=0`（永不过期）排在有期限批次之后，再以 money sequence 和 lot ID 决胜。一次请求接受时把规则版本写入 pricing snapshot；旧请求继续使用 `paid_first_v1`，新请求才使用 v2。预扣释放、最终结算和退款都使用已冻结的 allocation，不能在结算时重新挑选一批更高优先级资金。

余额不足的新请求仍按现有准入规则拒绝（不能用上面的 debt 分支授予无限透支）。debt 只记录已受理请求真实最终费用超过预扣、支付拒付等现有可审计负债。存在债务时入账先按 debt ID 顺序偿债，剩余才可消费；偿债也保留来源分配，不追补原欠费调用佣金。

兑换时把兑换码当时的 `Redemption.ExpiredTime` 冻结为到账 lot 的 `expires_at`，后续修改兑换码主记录不能延长或缩短已到账批次。到期扫描和每次消费前的惰性检查只扣除 `bonus_available`，同步减少 `users.quota` 与 `AgencyFundingAccount.nonpaid_available`，并累计到 `bonus_expired`/`AgencyTopupFact.expired_quota`。退款或预扣释放发生在期限之后时，原调用仍完整冲销，但对应额度直接进入 `bonus_expired`，不得回到 `bonus_available`。

### 2.3 余额与佣金口径

`users.quota` 是对外兼容的总余额；Agency 资金账户保存可审计分项：

```text
users.quota = paid_available + nonpaid_available - debt_quota
```

兑换码和管理员赠额消费不计代理佣金，但必须写用量；自付和代充的 paid 消费按冻结销售系数、结算系数和原计价引擎计算佣金。支付 lot 上的 bonus 必须单独标记为 nonpaid，不能因为和 paid 在同一个订单就自动产生佣金。

**优惠先计价、资金再扣减。** 已绑定客户使用代理商销售折扣计算本次最终费用，使用兑换码/赠额/paid 不改变该折扣。只有佣金的实付覆盖比例受来源影响；不能因为不计佣金就按原价向客户扣费。

### 2.4 评审默认值与交付边界

| 决策 | 本方案默认值 | 原因 |
| --- | --- | --- |
| 用户范围 | 完整目标覆盖所有钱包用户；先灰度代理客户，随后普通用户 | 非代理用户目前绕过 durable 账本，不能只改 Agency 页面后宣称平台全部完成 |
| 兑换码计佣金 | 默认 nonpaid、零佣金 | 兑换码自身不是支付凭据；若实际售码，另建带支付证据的业务来源 |
| 促销/签到/补偿 | `other_grant`，第二档，与 admin_grant 按同一 FIFO 排序 | 不能漏掉已有钱包入口，也不能伪装管理员操作 |
| 自付/代充 paid | 第三档统一 FIFO | 创建二维码的人不改变资金性质 |
| 来源不明旧余额 | `legacy_unknown`，第二档之后、verified paid 之前；不计新佣金 | 保留用户可用金额，避免无证据返佣；归类须保留迁移审计 |
| 自定义代充金额 | CNY 实付金额，支持两位小数，渠道/平台限额内自由输入 | 无需固定档位，但支付机构及 quota 有真实上限 |
| 金额默认范围 | 最小 `max(0.01, 渠道最小额)`，最大 `min(平台配置, 渠道限额, 可用quota容量)` | 不把金额上限写死在 UI；正式配置由运营设置 |
| 报价/二维码 | 报价有效 5 分钟；订单可支付有效期 15 分钟且不超过渠道限制 | 刷新/重复扫码不重新创建入账 |
| 支付范围 | 首期代充接入已有 Epay 的支付宝/微信可用方式 | 其它支付提供方继续记录来源；不承诺它们都支持自定义 QR |
| 旧余额追溯 | 仅有完整可信流水才重建，否则明确历史未分类 | 不从消费时间、文字备注推算财务来源 |

这些是供领导评审的一致默认方案，后续实现者应照此实现；如评审修改，先更新本文版本和测试期望，不能自行改变计佣或过期政策。

## 3. 一次调用如何记录多种资金

财务身份分三层，不能用一条“充值来源”字段代替：

```text
financial_charge_id（一次模型调用/一次可结算财务请求）
  └─ financial_segment_no（普通请求为0，Realtime为持久结算段）
       ├─ allocation_id → funding_lot（原资金扣减行及来源批次）
       ├─ component_id（文本、缓存、图像、工具等计费组件）
       └─ component_funding（component_id × allocation_id 的金额矩阵）
```

例如一次调用扣 80 quota，兑换码只剩 50，管理员赠额 30：

| 记录 | 数值 |
| --- | ---: |
| 主调用 `financial_charge_id=CH1` | charged=80，调用次数=1，tokens 只计一次 |
| allocation A | source=redemption，quota=50 |
| allocation B | source=admin_grant，quota=30 |

页面主表只显示一条调用；展开后显示两条来源分摊。确定提供两份独立 CSV：调用汇总和资金分摊，避免重复统计。网关内部上游重试、退款、取消和组件分段不增加调用次数；用户重新发出的独立请求仍是新调用。Realtime 按会话 charge 一行、segment 展开，另列结算段数。

## 4. 代充二维码业务流程

### 4.1 创建订单

Root 在主站用户管理中点击“代充”，确认该行受益用户，输入 **实际需支付人民币金额**，选择支付方式。金额不是数学意义上的无限制：必须经过支付渠道最小/最大金额、币种小数位和 `MaxQuota`/int32 quota 上限校验。金额以 decimal/string 传输，禁止 JavaScript `Number` 或 Go float 作为新账务真值；旧 `TopUp.Money` 仅作兼容展示。

主站创建不可变的 assisted top-up order，至少保存：

```text
order_id / source_operation_id
beneficiary_user_id
initiated_by_root_id
payer_identity（支付机构返回的必要脱敏信息，可为空）
payment_provider / payment_method
requested_money / credited_quota / paid_quota / bonus_quota
price_group_snapshot / quota_conversion_snapshot
payment_state / credit_state（详见 §13.3 的双状态机）
expires_at / created_at / paid_at
idempotency_key / request_hash
```

二维码只编码支付机构 checkout URL 或不可猜测的短 token，不编码 API key、完整用户资料或可直接加额的 quota。创建订单成功但未支付时：`users.quota`、funding lot 和 `AgencyTopupFact` 均不变。

### 4.2 支付回调与入账

先在事务外验签及验证字段，再在事务内锁定订单、重新校验订单号/金额/币种/当前状态，并完成入账。第一次合法支付成功才创建 `payment_assisted` lot、ledger、topup fact 和 outbox；重复回调核对证据一致后返回原结果。不一致的已支付凭据进入待核验队列。**伪造/无效签名不能改变订单状态**，否则攻击者可以冻结别人的正常订单。过期后确实付款不能只丢弃通知，须记录 `paid_late` 并按 §13.3 处理。

复用 Epay 的验签和改造后的统一入账能力，但不能直接复用当前 `POST /api/user/pay`：该接口固定从当前会话取受益用户，无法表达 Root 发起人和目标用户。新增 Root 专用接口，详细契约见 §13.4：

```text
POST /api/admin/users/:user_id/assisted-topups
GET  /api/admin/assisted-topups/:order_id
POST /api/user/epay/notify                 # 保留现有签名回调入口，按订单类型分流
```

创建接口要求 Root 会话、针对动作/金额/目标的二次验证 proof、幂等键和审计理由；受益用户必须在创建时锁定，不能通过修改 URL 更换目标。仅查询已授权订单不重复要求输入密码。二维码允许其他人付款，实际付款人不因此获得查看受益用户资料/调用日志的权限。

### 4.3 取消、过期和退款

未支付订单过期只改变订单状态，不产生资金。支付退款/拒付引用原 `source_operation_id` 和 lot：未消费额度从可用余额撤销，已消费部分按原 allocation 建 debt，并冲回相应佣金；不能以新的管理员赠额补偿，也不能把失效 paid 恢复成可用 paid。模型退款引用原 `charge_id/allocation_id`，按累计退款 delta 还原来源，不能重新运行 v2 分配器。

## 5. 数据模型改造

### 5.1 继续复用的权威表

继续使用 `AgencyFundingAccount`、`AgencyFundingLot`、`AgencyFundingAllocation`、`AgencyFundingLedger`、`AgencyBillingJournal`、`AgencyBillingOperation`、`AgencyBillingOutbox`、`AgencyEventDelivery`、`AgencyUsageFact`、`AgencyTopupFact`、组件退款矩阵和 debt 表。主库仍是资金权威，Hub 只投影和记佣金。

### 5.2 必需新增字段/表

现有 `SourceKind` 在很多调用处表示 `epay/stripe/...` 等原始业务来源，**不就地改写其含义**。新增 `FundingSource` 为统一产品来源（API 字段名仍为 `source_kind`）；新代码同时保存原 `SourceKind/SourceID/CompletionSource`，旧路径按版本读取。在 `AgencyFundingLot` 增加或确认：

```text
funding_source           redemption|admin_grant|payment_self|payment_assisted|other_grant|legacy_unknown
source_business_id       redemption_id / admin_adjustment_id / topup_order_id
actor_user_id            操作管理员或兑换操作者（可为空）
beneficiary_user_id      受益用户快照
entry_channel            wallet_self|admin_assisted|admin_console|redemption
expires_at               兑换时冻结的 Redemption.ExpiredTime；0 表示永不过期
bonus_expired            到期未用额度，以及到期后退款/释放而不可复活的额度
source_snapshot_json     订单、兑换码、操作者和规则的脱敏快照
```

支付订单继续复用 `TopUp` 作为全站订单索引，新增 **`WalletPaymentOrderContext` 一对一扩展表** 保存自付/代充入口、发起人和不可变精确报价，不另建会独立入账的第二张代充订单表。`AgencyTopupFact` 增加来源/业务 ID、actor、beneficiary 和已入账状态；一次支付可能有 paid 和 bonus 两个批次，通过关联查询返回 lot 列表，不用单一 `lot_id` 掩盖它们。`AgencyFundingAllocation` 增加冻结的来源快照，防止后续修改 lot 备注改变历史报表。

`AgencyUsageFact`/usage API 增加 `financial_charge_id`、`segment_no`、`component_id`、输入/输出/缓存 tokens、净消费、退款状态和投影状态。来源分摊通过明细接口返回，不能把一条调用复制成多行后再让前端猜测。

历史数据无法证明来源时使用 `legacy_unknown`，保存 cutover 快照和证据哈希。不能把旧 aggregate-only nonpaid 余额强行拆成兑换码或管理员赠额，也不能把初始化时的 `legacy_migration` paid 当作真实支付凭据。

### 5.3 资金操作 API 约束

逐步淘汰只接收 `(user_id, delta, source_kind)` 的通用调用，改为带类型的 `TypedFundingContext`：

```text
operation_id, source_kind, source_business_id,
actor_type, actor_id, beneficiary_user_id,
completion_source, payment_evidence,
paid_quota, bonus_quota, expires_at, reason, idempotency_key
```

所有正向、负向和退款操作必须在一个主库事务中同时更新用户 quota、资金账户、lot、ledger、订单/兑换状态、审计和 outbox。管理员“设置绝对余额”先锁定旧值计算 delta；负向调整必须选择明确来源 lot 并二次确认，不能默认按 paid→nonpaid 跨来源扣减。

## 6. 用量、充值和佣金报表

### 6.1 充值记录

保留“充值记录”菜单名称，改成统一 funding operations 视图，四种来源全部显示：

| 字段 | 说明 |
| --- | --- |
| 来源 | 兑换码、管理员调整、钱包自充、管理员代充 |
| 入账总额/paid/bonus | 原始值、不可变 |
| 当前可用/冻结/已消费/撤销/过期 | 状态分项，互斥统计 |
| 操作者 | Root 调整人、兑换操作者或代充二维码创建人 |
| 受益用户 | 对 Root 视图明确显示 |
| 实付信息 | 仅支付来源显示脱敏金额、渠道和支付引用 |
| operation/lot | 可追溯到资金流水和订单 |

当前归属代理可查看客户实时余额；历史代理只能查看自己任期内的事实和解绑时快照，不能因重新绑定而看到客户全部历史资金。

### 6.2 调用用量

主行显示：时间、模型、token 名称/ID 快照、状态、输入/输出/缓存 tokens、charged quota、已退款、净消费、`charge_id`、请求 ID、佣金状态。来源标签显示 `[兑换码] [管理员赠额] [实付]`，多来源显示“混合”并可展开 allocation 明细。

状态必须区分：

```text
noncommissionable       不计佣金，但用量已确认
commission_pending       用量已投影，佣金等待处理
projection_lagging       事件尚未消费
projection_paused        worker 被开关暂停
reconcile_required       金额或 hash 需人工核验
```

前端不能在 `rows.length == 0` 时一律显示“暂无记录”。API `meta` 应返回 `as_of_money_seq`、`generated_at`、`projection_status`、`last_projected_at`；Root 可见 backlog/poison 数，代理只能看到自己范围内的同步状态。

### 6.3 报表 API 契约

兼容现有接口并增加详情接口：

```text
GET /agency/api/v1/customers/:user_id/usage
GET /agency/api/v1/customers/:user_id/usage/:charge_id
GET /agency/api/v1/customers/:user_id/usage/:charge_id/allocations
GET /agency/api/v1/customers/:user_id/funding-summary
GET /agency/api/v1/customers/:user_id/funding-lots
GET /agency/api/v1/customers/:user_id/funding-operations/:operation_id
GET /agency/api/v1/customers/:user_id/topups              # 兼容旧客户端，返回新 source 字段
```

所有 quota、金额微单位和用户 ID 在 JSON 中按字符串返回；分页 cursor 必须绑定代理、用户、身份和查询版本。汇总调用次数按 `financial_charge_id` 去重，不按 allocation/component 行计数。

## 7. Worker 与事件契约改造

事实摄取/用量投影必须与佣金入账拆成两个可独立暂停的进度：

```text
网关事务 → immutable source event
          ├─ usage/funding projection（默认开启）
          └─ commission projection（可独立开关）
```

**已落地实现：现有 delivery 负责验证事件和写事实；同事务创建独立 `AgencyCommissionJob`，佣金 worker 单独处理。** 关闭佣金时 job 保持 pending/deferred，不能把它当作“不计佣金”跳过。事实 consumer 不因佣金暂停而停止；具体唯一键、迁移和事件版本见 §13.6。`AGENCY_HUB_FACT_PROJECTION_ENABLED` 默认 true，仅作运维暂停，佣金开关只控制佣金 job，提现开关独立。

事件必须有 `schema_version` 和稳定 canonical hash。新增字段不能用当前 Go struct 重新序列化后验证旧 v1 hash；旧事件应按其原版本校验，冲突进入 poison/reconcile，不得修改历史金额或 hash。

Realtime 和部分直接写 usage fact 的路径要统一唯一键，避免“网关直写 + 新投影器”重复。`recordUsageFact` 先于佣金资格判断的语义应保留。

## 8. 安全、并发和数据库兼容

1. 代充创建、退款和负向调账需要 Root 权限、动作绑定的二次验证、幂等键和审计；查询按授权验证身份即可。二维码在有效期内可以重复打开，同一订单最多入账一次；短 token 不等于只能扫描一次。
2. 受益人、创建人、付款人分开授权和记录；普通用户不能通过修改订单请求体替换受益人。
3. 支付回调与兑换码 CAS 必须防并发重复。实现前统一所有触及同一批表的锁顺序，包含支付退款、兑换码、调账和任务回调；明确顺序见 §13.5，不能在事务锁内访问上游。
4. 金额计算使用 decimal/整数、checked 加减乘除；入账采用现有 `common.QuotaFromDecimalStrict` 和钱包容量校验，越界不能截断后少入账。模型计价继续使用 `common.QuotaFrom*Checked` 并保留 saturation 审计；历史负债是单独业务，不能因计算溢出产生。
5. GORM 标准查询使用 `lockForUpdate(tx)`；SQLite 使用短事务/CAS，有界 busy 重试；MySQL 5.7.8+、PostgreSQL 9.6+ 和 SQLite 的迁移都只能增加兼容字段，不能使用单一数据库特性。
6. 兑换码过期、lot 过期、支付退款、模型退款和 debt 偿还必须共享原 allocation 证据，不能定时任务无锁清零。

## 9. 对现有代码的改动范围

| 层 | 主要位置 | 工作内容 | 规模判断 |
| --- | --- | --- | --- |
| 资金模型/迁移 | `model/agency_models.go`、`model/agency_funding.go` | lot 来源、creator、expiry、typed context、v2 allocator、历史 unknown | 大 |
| 兑换码 | `model/redemption.go` | redemption lot、source ID、操作人、幂等和回滚 | 中 |
| 管理员额度 | `controller/user.go`、用户管理 service | 正负调整预览、来源选择、Root 审计 | 中 |
| 支付 | `controller/topup.go`、`model/topup.go`、Epay callback | assisted order、受益人/创建人分离、支付回调分流 | 大 |
| 计费网关 | `service/agency_gateway.go`、billing session/task/realtime | v2 规则快照、allocation、退款和多组件保持单 charge | 大 |
| Hub worker | `pkg/agencyhub/worker.go`、`finance_service.go` | usage/funding 与 commission 独立 receipt、hash 版本兼容 | 大 |
| 报表 API | `pkg/agencyhub/report_service.go` | 来源、分摊、净消费、投影状态和权限过滤 | 中 |
| 前端 | `agency-web/src/features/reports/Pages.tsx`、主站 users 页面 | 代充按钮/二维码、来源标签、展开分摊、空状态 | 中 |
| 运维 | migration、备份、reconcile、蓝绿发布 | 双版本兼容、回滚、积压和对账门禁 | 中 |

总体属于跨主站、资金账本、支付回调、异步投影和报表的中大型改造，不能按“增加一个按钮”估算。建议拆成 4 个可回滚阶段，每阶段单独验收。

## 10. 迁移与发布方案

### Phase 0：盘点和保护

- 备份 PostgreSQL、验证恢复；保存每个 durable 用户的 `users.quota`、account、lot、ledger、outbox 快照。
- 统计 pending/retry/poison、open reconciliation issue，修复或隔离历史 hash 漂移；不重写旧 payload。
- 明确旧用户的 `legacy_unknown` 策略，不凭总余额推断付款来源。

### Phase 1：兼容字段和事实投影

- 先上线可读新字段、新版 source event（不是覆盖现有组件 v2）和独立 usage/commission 进度，旧 v1/组件 v2 事件继续可消费。
- 开启事实/用量投影，保持佣金开关关闭；恢复有完整证据的用量与入账摘要。当前不存在的逐笔 lot/来源余额需在 Phase 2 写入后验收，不能凭空从旧事件补出。

### Phase 2：四来源写入和 v2 分配器

- 所有 Blue、Green、Hub 命令和异步任务升级后，才允许创建 v2 funding lot。
- 灰度启用 `redeem_admin_paid_v2`，新请求按 v2，旧 journal 按 v1；核对 `users.quota` 与 lot aggregate 守恒。
- 上线 Root 代充订单和支付回调，先限制金额范围和管理员权限，观察重复回调/冲突。

### Phase 3：报表和佣金/提现

- 报表显示来源和分摊后，再独立开启佣金处理；核对 commission balance、usage、topup 和 daily stats。
- 佣金积压、open reconciliation、支付密钥和提现状态通过 Go/No-Go 后，才开启提现申请。

回滚只能停止创建 v2 新请求并保留能理解 v2 的兼容镜像；不能直接回滚到只认识 aggregate-only 或 paid-first 的旧镜像。任何阶段都不能删除原始 PostgreSQL volume 或重建数据库。

## 11. 验收测试矩阵

### 11.1 资金和计费

| 场景 | 必须结果 |
| --- | --- |
| 仅管理员加额后完成调用 | 1 条 usage，来源 admin_grant，佣金 0 |
| 仅兑换码后完成调用 | 1 条 usage，来源 redemption，兑换截止不影响已到账余额 |
| 兑换码 50 + 管理员 30，调用 80 | 1 个 charge、2 个 allocation，总额 80，tokens/调用次数不翻倍 |
| 兑换码不足后支付 30 | 先扣 redemption/admin，再扣 paid；来源明细可展开 |
| 自付与代充 | 同属 paid 佣金规则；代充 creator 与 beneficiary 分开 |
| 代充订单未支付 | 不改 quota、不建 lot/topup fact |
| 支付回调重复 | 只入账一次，返回原 operation 结果 |
| 已验签但金额/币种不符 | 不入账，支付核验异常，审计可查 |
| 无效签名 | 拒绝并作安全审计，不能改变原订单状态 |
| 模型部分退款 | 按原 allocation 恢复，累计退款单调且不超原额 |
| 支付拒付后模型退款 | 不恢复失效 paid，债务与偿债来源可追溯 |
| 关闭佣金开关 | usage/funding 继续更新，佣金显示 pending，不显示暂无记录 |

### 11.2 权限、迁移和兼容

- Root 以外不能创建代充，代理商不能替换受益人或查看其他代理历史。
- 代理转移后，旧代理只能看到任期内事实；当前代理才能看实时客户余额。
- SQLite、MySQL 5.7.8+、PostgreSQL 9.6+ 的余额、唯一键、迁移和并发 CAS 结果一致。
- 旧 v1/组件 v2 outbox 按原契约校验；新 v3 事件不会改变旧 hash。
- 同一 charge 的 retry、component、segment、allocation、refund 不会重复统计调用数、tokens 或佣金。
- 对账恢复演练：备份恢复、消费者中断、重复支付回调、数据库重启、蓝绿切换后均能继续处理。

### 11.3 上线门槛

以下任一项未通过，不得对外宣称闭环完成：

1. 三类既有入账和新代充均有不可变 lot、ledger、topup fact 和审计来源。
2. 一次多来源扣费只显示一条调用，展开分摊合计严格等于 charged quota。
3. 用量投影与佣金开关独立，停佣金不会导致空报表。
4. 退款、拒付和 debt 不产生重复余额或超额冲佣；允许有证据的负向佣金冲正，已提现后的可用佣金允许负值。
5. 正式库备份可恢复，pending/poison/open issue 有明确处理结果。
6. 前后端契约、CSV、权限、三数据库和蓝绿回滚测试全部通过。

## 12. 实施交接清单

后续编码模型应按以下顺序提交小步变更，并在每步附测试结果：

1. 先实现 schema/version、TypedFundingContext 和 funding lot 来源字段，补三数据库迁移；以 §13 的确定契约补全接口，不自行缩减范围。
2. 接入管理员调整和兑换码，保留 operation/actor/source ID；补用量/充值 API 字段。
3. 拆分 usage projection 与 commission receipt，处理旧 hash 版本。
4. 实现 `redeem_admin_paid_v2` allocator、单 charge 多 allocation、累计退款和 debt 保护。
5. 实现 Root assisted top-up order、二维码页面、验签回调和重复回调幂等。
6. 更新 Agency 报表和主站用户管理 UI，补权限、空状态和 CSV。
7. 执行 SQLite/MySQL/PostgreSQL、并发、支付故障、迁移恢复、蓝绿发布和生产只读验收。

本文件是设计和验收基线。它没有修改正式环境，也没有声称四种资金方式已经实现；实现完成后必须逐项回填测试证据、迁移版本、镜像版本和回滚演练结果。

## 13. 开发执行规格（不得自行省略）

### 13.1 版本、数据归属与全站范围

以下四个版本维度独立存储：

| 维度 | 新版本 | 含义 |
| --- | --- | --- |
| `wallet_funding_version` | `2` | 用户钱包已迁移到逐来源批次，不依赖是否绑定代理 |
| `funding_rule_version` | `redeem_admin_paid_v2` | 新调用选取资金的次序 |
| `balance_schema_version` | `2` | 新 lot/allocation 明确使用 reserved/consumed 语义 |
| 事件 `schema_version` | `agency-billing-v3` | 新来源事件；已存在的 `agency-billing-v2` 是组件版本，不得覆盖 |

在 users 增加 `wallet_funding_version`，现有用户回填 1，新钱包初始化后才置 2。既有 `agency-durable-v1` 标记保留；普通用户拥有来源钱包但没有代理绑定，不得为其伪造 AgencyPricing。统一钱包工厂依据 `wallet_funding_version` 选择 durable 路径，再依据真实 binding 决定代理价格/佣金；无绑定时使用主站原价规则并令 `agency_id/binding_id=NULL`。已迁移用户不能因机构禁用、开关关闭、无代理缓存而回退到批量旧钱包写入。

全站资金权威仍在主站 `model/`；Hub 不同步参与模型请求、不直接修改 users.quota。可以复用现有 `agency_hub_funding_*` 表，表名前缀不代表只允许代理用户。新表与列由主站显式 migration 管理，Hub 自有 job/投影表由 Hub migrate 管理，启动时只检查兼容版本。

### 13.2 表、字段和唯一性

字段采用 BIGINT 对应 Go int64；JSON 中 ID/quota/微金额用十进制字符串，业务状态用有限枚举，时间统一 UTC Unix 毫秒。原有秒字段不改变单位，新字段以 `_ms` 结尾。以下为增量模型规格，不是直接执行的 SQL。

| 模型/表 | 增量字段或职责 | 必须的唯一键/索引 |
| --- | --- | --- |
| `users` | `wallet_funding_version`，迁移完成后不可回退 | 原 user PK |
| `AgencyFundingAccount` | `balance_schema_version`、迁移检查点；paid/nonpaid/debt 仍为总账缓存 | user PK，version CAS |
| `WalletFundingOperation`（新增 `wallet_funding_operations`） | `operation_id`、`scope_hash`、user、source、source_business_id、actor_type/id/display_snapshot、reason、credit/revoke/refund/migration 类型、paid/nonpaid 总额、money_seq、发生时间、binding 快照、input_hash/result_json | operation_id 唯一；scope_hash 唯一；(user_id,money_seq)，(user_id,occurred_at_ms,id) |
| `AgencyFundingLot` | `funding_source`、`funding_class`、`priority_class/priority_rank`、`funding_operation_id`、`tranche_no`、`balance_schema_version`、`source_snapshot_json`、`spend_expires_at_ms`；新增 `paid_expired/nonpaid_expired`，沿用原 SourceKind/SourceID | operation+tranche 的短 hash 唯一；(user_id,priority_rank,money_seq,id) |
| `AgencyFundingAllocation` | 新增 `financial_segment_no`、`allocation_no`、`funding_rule_version`、`balance_schema_version`、`funding_source`、`funding_class`、`source_business_id`；原 ChargeID/LotID 保留 | (charge_id,financial_segment_no,allocation_no) 的 scope hash 唯一；(user_id,lot_id)，(charge_id,financial_segment_no) |
| `AgencyFundingLedger` | 新增精确 `reserved_delta/consumed_delta/revoked_delta/debt_repaid_delta` 与批次快照；不可变分录 | 继续 (operation_id,entry_no) 唯一 |
| `WalletTopupQuote`（新增 `wallet_topup_quotes`） | quote_id、受益人、Root、CNY money_minor、credited/paid/bonus quota、汇率/分组/折扣版本、expiry、body_hash；只保存白名单参数 | quote_id 唯一；expire 索引；actor+created 索引 |
| `WalletPaymentOrderContext`（新增 `wallet_payment_order_contexts`） | top_up_id、quote_id、entry_channel、Root 发起人、受益人、两者昵称快照、checkout_token_hash、money_minor、币种、冻结到账额、payment_state、credit_state、退款累计额、回调凭据、provider reference、version、expiry | top_up_id 唯一；actor+幂等键的 scope hash 唯一；provider+merchant+transaction 的证据 scope hash 唯一（未取得时 NULL） |
| `AgencyTopupFact` | 入账操作的可重建只读投影；source、actor、beneficiary、绑定快照、入账额/实付证据；不把未付款订单投影为入账 | 原 source_operation_id 唯一；来源+客户+发生时间索引 |
| `AgencyUsageChargeFact`（新增 `agency_hub_usage_charge_facts`） | charge_id、user、token_id/name_snapshot、request_id、agency/binding、model/endpoint、accepted_at、最后结算版本、业务/财务状态、token usage、charged/refunded/net、segment_count | charge_id 唯一；(agency_id,user_id,accepted_at_ms,id)；request_id 检索 |
| `AgencyUsageFact` | 保留为段/组件子事实，补齐 charge/segment/token usage/version；关联上面主行 | (charge,financial_segment,component_key) 的 scope hash 唯一 |
| `AgencyCommissionJob`（新增 `agency_hub_commission_jobs`） | event_id、source_hash、user/money_seq/event_index、status、lease/fencing、attempts、next_retry、error、完成时间；不保存第二份可编辑金额 | event_id 唯一；(status,next_retry_at,id)，(user_id,money_seq,event_index) |

`scope_hash` 使用规范输入的 SHA-256（64 小写 hex），同时保留原业务键并在冲突时比对，避免 MySQL 5.7 复合长字符串索引超限。新增列先 nullable/兼容默认再分批回填，不依赖 partial index、数据库 JSON 运算、generated column 或 MySQL8 专属语法；应用校验和幂等唯一键在三库一致。

常用枚举固定：`funding_class=paid|nonpaid`；`priority_class=redemption|grant|legacy|paid`，rank 分别为 10/20/25/30；`source_kind` 使用 §2 中六种产品来源，支付 bonus 保持其支付入口来源并设置 class=nonpaid、rank=20。debt 是消费分配/偿债性质，不是第五种充值，单独返回 `allocation_type=debt`。`commission_status=not_eligible|pending|processing|posted|partially_reversed|reversed|error`；`projection_status=current|lagging|paused|error`，不混用字段或状态。

每个 v2 lot 只代表一种会计分量：paid 或 nonpaid。支付到账 100+赠送 10 建两个 tranche，共用 operation/order；paid tranche 属第三档，bonus tranche 保留 `payment_self/payment_assisted` 来源但属第二档。非付费新 lot 沿用旧 `Bonus*` 列存储，代码统一访问器对外称 `nonpaid_*`，不将兑换码称作充值赠送。

不得把现有 `AgencyFundingAllocation.SegmentNo` 直接当 Realtime 段号：旧分配器把它用作分配行序号。新增 `financial_segment_no/allocation_no` 消除重载；旧记录通过 journal/组件关联适配，来源不明时显示 unknown。

### 13.3 代充报价与支付状态机

**金额解释**：输入 `money="20.00", currency="CNY"` 表示付款 20 元，不表示 20 个 token、20 quota 或 20 美元。后端抽取现有充值报价逻辑为公共服务，使用受益用户当前分组，绝不能使用 Root 自己的分组。

代充自定义报价使用 `rate = Price × TopupGroupRatio(beneficiary)`，精确十进制计算 `credited_quota = floor(money_CNY / rate × QuotaPerUnit)`；`rate/QuotaPerUnit` 必须正且有限。自定义金额默认 `preset_discount=1`，不从金额倒推命中旧固定档位折扣；已有钱包档位报价保持原规则。若要给代充使用固定档位优惠，前端须另选具体 preset 并使用同一报价服务，不能隐式叠加。这里是充值换算，不是 API 请求的代理销售折扣，二者不能重复应用。示例中的 `Price/QuotaPerUnit` 必须来自配置，不在代码写死。

paid/bonus 由服务端优惠规则确定；没有显式赠额策略则 credited 全部为 paid。实际收款金额、到账 quota 和模型费用的展示币种分开保存，不能声称 20 元必定等于 20 元的页面余额。`credited_quota<=0`、字段超界、库存/用户不可用等在创建前拒绝。付款后再做原子容量校验，不能因为用户在下单后又收到其它资金而溢出。

创建流程：

1. 验 Root 和绑定报价的 proof/幂等键，在一个短事务先创建 `TopUp(status=pending)` 与 context `payment_state=creating`，固定全局唯一 trade_no。
2. 提交后调用 Epay 生成收银台参数。成功保存并进入 pending；明确失败为 create_failed；超时不明为 create_unknown，使用同 trade_no 查单/重试，禁止另造订单蒙混成功。
3. 现有 Epay Purchase 返回 `url + params`，不保证直接返回二维码图片。新增 `/pay/assisted/:token` 轻量收银台，向支付方提交这些签名参数；Root 页面二维码编码该收银台 URL。域名固定来自部署配置，不接受客户端 redirect/notify URL。
4. 收银台允许有效期内重复访问，仅显示脱敏受益人、实付金额、商户、状态和付款按钮；无 Root/API 权限。token 以高熵随机值产生，数据库仅存 hash，路由访问日志脱敏，`Referrer-Policy: no-referrer`、`Cache-Control: no-store`，不加载第三方分析脚本。

支付状态和入账状态分离：

| 情况 | payment_state | credit_state | 行为 |
| --- | --- | --- | --- |
| 刚创建/待扫码 | creating / pending | not_credited | 不增加任何余额 |
| 创建失败/未知 | create_failed / create_unknown | not_credited | 查单确认后复用同 trade_no，不重付 |
| 正常验签成功 | paid | credited | 入账与状态同事务提交，返回 success |
| 已真实收款但用户容量不足/账户待迁移 | paid | blocked | 持久化支付事实，重试同 operation 或经验证原路退款；不能静默少入账 |
| 收银台超时/主动关闭 | expired / cancelled | not_credited | 禁止发起新支付；不代表渠道绝不可能晚到支付 |
| 过期/关闭后实际付款 | paid_late_review | blocked | 记录实收，查单；默认核验后入同一受益人原报价，不能入账则原路退。不得吞掉已收款 |
| 签名有效但金额/商户/币种冲突 | verification_conflict | blocked | 保留证据，禁止自动改变余额，进入处理队列 |
| 无效签名/未知伪造通知 | 原状态不变 | 原状态不变 | 拒绝和限速，不污染订单 |
| 已 paid 的相同回调 | paid | credited | 对照证据幂等成功，不重复入账 |
| 部分/全部支付退款 | partially_refunded / refunded | partially_revoked / revoked（未入账则not_credited） | 原 lot/debt/佣金联动；旧成功回调不把状态改回 paid |

成功通知先验证固定商户身份、签名、trade_no、真实交易号和精确 money_minor；新支付证据不能为空。当前 `RechargeEpay` 有重复状态保护，但不等于已实现新订单报价金额匹配，必须补测试。持久化支付事实后若余额入账被阻断，可确认收款通知并交给可靠恢复任务；数据库故障未持久化则返回渠道约定的失败以促使重试。普通用户钱包订单历史从 TopUp.UserId 读取，因此代充也出现在受益用户的钱包中。管理员“补单”不是代充，不得绕过验签手动改成 paid。

新增 context.credit_state 是新订单的入账判定；旧 `TopUp.Status` 保持兼容映射，只有 credited 才映射为旧 success。付款已到但 blocked 不能在旧管理界面提供“手动补单”绕过恢复任务。管理员未付款订单过期不产生 topup fact，但 context 保留创建/失效历史便于追踪二维码是谁生成的。

### 13.4 接口契约与示例

下表包含核心已落地入口和后续扩展入口；具体状态以第 14 节矩阵为准。已有 topups/usage 列表保留。写接口统一遵循：缺少会话 401、权限不足 403、参数/金额非法 400、资源不可见 404、状态/幂等/版本冲突 409、依赖不可用 503。幂等键相同且规范请求相同返回原订单；同键不同内容返回 `idempotency_conflict`，禁止创建第二笔。金额下限/上限由服务端校验并供 UI 使用。

| Method/路径 | 权限 | 请求要点 |
| --- | --- | --- |
| POST `/api/admin/users/:user_id/assisted-topup-quotes` | Root | money、currency、payment_method；返回报价/到账额/有效期 |
| POST `/api/admin/users/:user_id/assisted-topups` | Root + 动作 proof | quote_id、reason；Idempotency-Key；不能传到账 quota |
| GET `/api/admin/assisted-topups/:order_id` | Root | 只读订单、两种状态、可分享 URL、发起人快照 |
| POST `/api/admin/assisted-topups/:order_id/cancel` | Root + 动作 proof | expected_version、reason、幂等键；只关闭未付款订单 |
| GET `/pay/assisted/:token` | 有效 checkout token | 只读脱敏收银台，支持重复扫描 |
| GET `/api/pay/assisted/:token/status` | 有效 checkout token | 仅付款/入账状态和脱敏金额；限速，不返回调用/余额 |
| POST `/api/admin/wallet-action-proof` | Root + 密码/已有二次验证机制 | action、object_id、request_hash；有效 5 分钟，绑定当前会话和权限版本 |

新 proof 用专有 audience `wallet-admin-action`，避免复用原 `agency-hub-verification` 的跨服务证明；重用密码验证/限流基础设施，但不能绕过当前会话撤销验证。创建订单需 proof 与幂等记录在同事务消费；已成功幂等重试在确认当前授权后返回原结果，不再次消费 proof。只读报价/查单无需反复验证密码。取消与退款不同，本期取消不自动转出资金；真实退款经支付方接口或已验证付款证据入冲正流程。

报价请求：

```json
{"money":"20.00","currency":"CNY","payment_method":"alipay"}
```

创建请求（`Idempotency-Key` 和 proof 放请求头，不写入 URL）：

```json
{"quote_id":"qt_example_01","reason":"客户委托充值"}
```

响应形状，quota 数字仅为合成示例，实际必须取报价：

```json
{
  "success": true,
  "data": {
    "order_id": "ord_example_01",
    "beneficiary_user_id": "161",
    "initiated_by_user_id": "1",
    "source_kind": "payment_assisted",
    "money": "20.00",
    "currency": "CNY",
    "credited_quota": "10000000",
    "paid_quota": "10000000",
    "bonus_quota": "0",
    "payment_state": "pending",
    "credit_state": "not_credited",
    "version": "1",
    "checkout_url": "https://gateway.example.com/pay/assisted/opaque-example",
    "expires_at_ms": "1790000900000"
  }
}
```

来源余额接口返回 `items[]`，每项固定 `source_kind/funding_class/priority_class/initial_quota/available_quota/reserved_quota/consumed_net_quota/revoked_quota/expired_quota/debt_repaid_net_quota`，外加 `total_available_quota/debt_quota/net_wallet_quota`。同一 source 可能有 paid 和 bonus 两项，UI 合并为“自付/代充”卡片时仍展示拆分，不漏掉 bonus。

用量主行示例（与真实用户 161 无关，用于锁定接口契约）：

```json
{
  "charge_id": "charge_example_80",
  "user_id": "161",
  "token_id": "54",
  "model": "Hunyuan/hy3",
  "business_status": "success",
  "charged_quota": "80",
  "refunded_quota": "0",
  "net_charged_quota": "80",
  "input_tokens": "100",
  "output_tokens": "20",
  "commission_status": "not_eligible",
  "source_kinds": ["redemption", "admin_grant"],
  "allocations": [
    {"allocation_id":"a1","lot_id":"l1","funding_operation_id":"f1","source_kind":"redemption","charged_quota":"50","refunded_quota":"0"},
    {"allocation_id":"a2","lot_id":"l2","funding_operation_id":"f2","source_kind":"admin_grant","charged_quota":"30","refunded_quota":"0"}
  ]
}
```

主列表不内嵌全部 allocation，以上为单次调用详情的形状。列表只返回摘要/source_kinds，点开后取详情。时间、状态、模型、source_kind、actor、是否混合为服务端过滤项；source 筛选主调用用 EXISTS，不 JOIN 后重复计数。cursor 按主行 `(accepted_at_ms,id)` 稳定排序，与权限/过滤 hash/查询版本绑定。一个 charge 更新退款或结算后不改变 accepted_at，从而不因明细增长挪动分页；导出额外固定快照版本，不宣称动态列表有跨请求事务快照。

完整调用次数按 distinct charge；Realtime 会话另返回 segment_count。不同协议的 input/cache tokens 按原标准化 usage 记录，不能把缓存数再次加到已含缓存的 input 数上；缺失上游 usage 显示 null/unknown，不能把缺失伪造为 0。请求失败无收费显示 charged=0，有已确认部分收费的失败显示实际费用和佣金排除原因。

### 13.5 分配、结算与退款算法

**资金状态**：当前代码的 reserve 会直接写 Consumed，不应照列名直接算“已消费”。新 v2 明确 `available → reserved → consumed`，v1 留在适配器。新 lot 的互斥守恒式（paid/nonpaid 各自计算）：

```text
initial = available + reserved + consumed_net + revoked_unspent
          + expired + debt_repaid_net
account.paid_available = Σ paid lot.available
account.nonpaid_available = Σ nonpaid lot.available
users.quota = Σ lot.available - outstanding_debt
```

`consumed_net` 是实际完成收费减模型退款，reserved 不是最终消费。支付拒付已消费部分仍保留 consumed，另建债务，不能同时把它计入 revoked_unspent 导致翻倍；`payment_reversed_total` 为流量指标，不再加进上式。冻结资金被拒付时仍保留原 reserved 历史占用，但标记其 revoked-reservation 债务关联；释放时转入 revoked_unspent 并抵销对应债，不能恢复 available。ledger 记录所有状态转移，业务展示不靠当前余额减原金额猜消费。

新 lot 的 PaidConsumed/BonusConsumed 保存净已消费，累计正向消费/模型退款由 ledger 和 allocation 累计字段分别保存；PaidDebtRepaid/BonusDebtRepaid 同样表示净偿债占用。退回已撤销来源时将对应 consumed_net 转 revoked_unspent，保证原 lot 守恒；恢复已偿债新资金时减少新 lot 的 debt_repaid_net 并增加其 available（或归还其它债务）。原批次金额不因模型退款增加 initial，迁移转入额也单独标记 opening，不计本期新充值。

**统一事务**：按只读定位拿到 owner ID 后，涉及机构先锁 agency（如有），再按 user_id 锁 users/binding/account，再锁支付订单或兑换码业务行，再 token/journal，再按 lot ID 锁目标批次。同类操作全链遵守一个顺序，包括退款、补单与回调；不得一条路径先锁订单、另一条先锁用户。兑码用 CAS `enabled→used`，即使 SQLite 不支持行锁也必须全事务回滚重复兑换。MySQL 避免对不存在的全局 charge 范围做 `FOR UPDATE`，先锁用户再幂等查询。CAS/busy/deadlock 有界重试，数据库提交未知先按 operation_id 查询结果；事务重试不能再次调用模型或付款。

**预扣**：

1. 读取稳定钱包版本、binding、原计价引擎报价，创建一次 financial_charge_id；持久保存规则/币种/优惠快照。
2. 同事务校验用户真实可用金额与 token 额度，按优先档/FIFO 选出 lot；批次按 ID 锁定后重校验，分配逻辑仍遵循优先排序。
3. available 减少、reserved 增加；用户 quota 与 token 预扣使用既有计费契约；创建 allocation/journal/ledger/outbox。非代理用户也走这条持久资金链，只是不应用代理价格。
4. 余额不足的新请求不发上游；免费请求仍建金额 0 的 journal，不凭零佣金跳过事实。

**最终结算**：

- 先用冻结引擎/标准化真实 usage 得出费用，再处理来源。不得从资金余额反推模型价格。
- 最终费用小于预扣：按原 allocation_no 逆序释放尾部，保留已优先取用的高档资金；同来源解冻，不能创建新充值记录。
- 最终费用大于预扣：保留原分摊，按原规则从结算时可用 lot 增补，允许用请求之后到账的余额；增补不足只对已提供服务的差额建有来源 debt，停止该用户后续资金准入并进入核验，不能重发上游。
- 金额相同也必须把 reserved 转成 consumed 并完成 journal；不能因为 delta=0 直接跳过用量/事件。
- user quota、token 的 remaining/used、lot、allocation、journal revision、operation、outbox 同事务提交。token 删除/无限额度按原约束，不能复活 token 或因 token 不存在退掉真实用户收费。

**组件归因**：先确定本次选用的 paid/nonpaid/debt 总量，再按现有 `AllocateComponentFunding` 的确定性比例算法将总量分给收费组件；这一步不再去用户余额选资金，因此不改变优先级。组件内按本次已选 lot 的原 allocation 顺序填充，并保存 `AgencyComponentFunding` 矩阵。保留组件 ID 稳定排序和精确整数余数分配；输入 tokens 只在对应 usage 维度记录一次，不按来源比例臆造物理 token 数。

**佣金例子**：合资格模型组件收费 B=100、冻结结算成本 T=75，理论差额 G=25；其中 verified paid 覆盖 P=20，则按原 `CommissionForPaid` 得 K=5 quota，再以原币种/换算快照转成佣金微单位。其余 80 的赠额部分有用量但不计佣金。新增 nonpaid 优先会减少近期 paid 覆盖比例，运营应预期佣金可能延后至真实消耗 paid 时产生。充值本身不直接返佣。

**取消与模型售后退款**：预扣取消逆序释放全部 reserved；售后部分退款使用原累计比例算法，不按当前优先级重新选择。先按原组件 ID 的条件比例拆分累计 R，再在每组件按 paid→nonpaid→debt 的固定条件比例算累计恢复额，最后在每类原 lot 顺序中按尚未恢复量分配。每次只提交 `new_cumulative - prior_cumulative`；全退必须精确恢复原数。赠额的退款留在原赠额 lot，不能变 paid。使用原 `CumulativeComponentRefunds` / `CumulativeComponentRefund` 的整数半离零舍入，不用每次独立最大余数排序做退款。

例：原分摊兑换码 50+admin 30，退款累计 R=40，nonpaid 恢复 40，按原 lot FIFO 恢复兑换码 40；累计全退 R=80 时最终恢复兑换码 50+admin 30。此处退款回哪一批与“新增消费先扣哪一批”分开；对外展示实际恢复到的原来源，不用按比例伪造每种 token 消耗。

**支付退款与债务**：按原订单累计收回额度，先作用于该订单尚未消费的可用 lot，再标记其冻结/已消费 allocation 并建 debt；部分 paid/bonus 的撤销比例沿用冻结支付优惠规则，累计可撤销不能超原额。model refund、payment reversal、debt repayment 共享 operation 幂等与原 allocation 的累计边界。同一部分同时遭遇支付拒付和模型退款，先抵原未还债；原债已被后续入账偿还时按 repayment 记录恢复实际偿债来源，不能恢复被拒付的原 paid。偿债不算新的调用、不追发原欠费佣金。v2 拒付与组件退款的组合必须通过回归，现有明确不支持的分支不能带到正式启用阶段。

**管理员减额**：保留 UI 的 add/subtract/override。add 或 override 的正差额创建 admin_grant；负差额需选择可撤回的未使用 admin_grant lot，展示预览和 expected wallet version，余额不足返回 409。需要动用支付余额时走绑定原支付证据的冲正，不允许普通调额任意没收 paid。不能改写历史 initial/consumed、不能删除旧流水。delta=0 写审计但不创建虚假充值。

### 13.6 用量同步解耦、事件版本与恢复

确定处理顺序：

```text
主站同事务写 immutable outbox + delivery
  → facts consumer 校验来源/version/hash/journal
  → 同事务写 source receipt + funding/usage 投影 + commission job + delivery done
  → commission worker 独立判断开关、租约、原事件依赖
  → 同事务写 commission ledger/balance + job done/skipped
```

facts consumer 完成后，不得因为 `AgencySourceEvent.ProcessingStatus=done` 让 commission worker 提前返回。`AgencyCommissionJob.status` 固定为 pending/claimed/retry/deferred/done/skipped/poison；开关关闭写 deferred 或保持 pending，不写 not_eligible。恢复开关后按 user money_seq/event_index 顺序领取；金额不合资格才 skipped，原因写清楚。前序异常只阻断有关用户/原退款依赖，不能阻断全站。

job 的金融效果恰一次依赖 event_id 唯一键、佣金 ledger 唯一业务键、数据库事务和租约 fencing，不依赖内存锁。source receipt、job、调用主表都存 payload hash，重放内容相同幂等，内容冲突隔离。source event 中已经冻结的 K/M 为佣金权威，Hub 不按当前折扣/价格重算。用户无代理绑定时照常生成资金事实，commission job 标 skipped(no_agency)，代理 API 不返回这些用户数据。

`agency-billing-v3` 采用独立 DTO/validator，事件含 operation_id、money_seq、event_index/count、charge/segment/component、来源 allocation 的白名单快照、actor/binding、净费用与冻结佣金。v3 payload 只序列化一次存 UTF-8，`hash_algorithm=sha256-raw-payload-v1` 对存储原字节求 hash；消费者先验证原字节再解析，并核对 envelope 与数据库 journal 元数据。禁止解析后以最新 struct 重新序列化验证旧 hash。

v1/现有 v2 仍用各自**生产时版本**的规范化器；同一 schema 历史上已有字段漂移时按保存的版本/实际合法候选格式离线核验，必须有原 journal/operation 证据。不能因为当前 struct 对不上就把哈希全部改成新的。未知格式 poison，单独修复审批/补偿事件，保持原 outbox 不变。历史用户 149 的 hash/operation 差异须按此调查，不能当作确认数据库损坏或人为删表。

投影唯一所有者固定为 facts consumer：迁移后移除 `RecordAgencyTopup` 的直接报表写入和 Realtime 直接 usage 写入，统一写完整 outbox。在转换窗口里消费者先校验现有 direct fact 的业务键/hash，已存在则接管，不重复插入。旧 done delivery 迁移到 commission job 之前，检查真实佣金 ledger：存在且匹配→done；可信零资格→skipped；否则 pending/review，不能默认再次付佣金。

**同时迁移资金代码对报表的权威依赖。** 当前 `RecordAgencyTopup` 的幂等核验及 `ReverseAgencyTopupTx` 的原充值/退款核验会读 `AgencyTopupFact`；须改读主库 `WalletFundingOperation`、支付 context、lot 和不可变 reversal 记录。否则仅删除直写会使“已到账但尚未投影”的重复通知/退款失败。主站资金正确性不能依赖 Hub 在线或报表同步进度，事实表只承担查询展示。

新 `AgencyUsageChargeFact` 从已验证段/组件事实重建，tokens 来自持久 journal/usage 契约，不能只接通字段却全填 0。主站日志在单独 LOG_DB 或 ClickHouse 时仍不能依赖跨库 JOIN 作为金融原子性；日志只用于验证 request 关联，权威交易在主库。`readyz` 增加独立 facts/commission capability，报告 scoped backlog、oldest lag、poison 和 last projected；facts 同步正常不代表提现合格。

恢复用户 161 的初步计划：先验证其 6 条原事件与 2 个 journal 的哈希和 operation 完整性，再投影一条成功消费及一条取消状态；仅成功消费计 charged=1916。两笔入账可由 ledger 证实，但当前 allocation 无来源 lot，不能把旧消费擅自全部归到兑换码。如完整证据可重建，需记录显式历史迁移规则和 dry-run 差异；否则旧消耗来源标 `legacy_unknown`，新规则仅向未来生效。

### 13.7 迁移守恒、有效期扩展和兼容回滚

先做 dry-run 清单：每用户 current quota、pending reservation、lot/account 差异、未结算任务、未知原单、direct facts、事件/佣金回执状态。快照记录 cutover 时间、source cutoff、原币种和版本；不能只备份 SQL 文件而不检查能否恢复。

每个钱包从版本 1 到 2 时，暂停该用户新的资金操作，等待其旧预扣/异步任务终结，或者保持该用户 v1 待后续迁移；不把正在使用 v1 的 consumed 当作新 v2 finalized。普通用户还需先排空原异步余额队列并对账，禁止缓存/批处理在迁移后补写旧余额。切换事务生成新的 opening lot/迁移 ledger，旧 lot 标记版本与归档归属；只有完整证明的剩余 paid 才作为 verified paid，其余进入 legacy_unknown。迁移前后用户可用总額必须不变；账本已存在差异的用户（例如历史 149）先隔离，不能选择某个余额覆盖另一个了事。

既有 `legacy_migration` 把旧余额全当 paid 的初始化逻辑必须改掉，未来初始化不凭空证明 paid。对历史剩余金额归类生成显式迁移分录，不改已完成请求的价格、来源快照或佣金。旧退款在新钱包中仍按原版本计算恢复额，通过迁移映射归入原真实来源或 unknown，不可产生双份 opening balance。

兑换码到账余额有效期为本期规则：兑换时冻结 `Redemption.ExpiredTime`；兑换码来源先按到期时间 FEFO，无到期批次排最后。过期只收回 available，截止前已 reserved/consumed 的合法请求允许完成结算；取消或模型退款回到已过期 lot 时，同时记录恢复与过期，实际 available 不增加。定时扫描负责让闲置账户及时更新，reserve/refund 同事务的惰性检查负责阻止调度延迟绕过规则。管理员赠额和 paid lot 不继承兑换码期限。

发布前所有资金写入服务升级到能读 v1/v2/v3 的兼容版本，数据库扩展先于新写入。两个主站、任务结算 worker、支付回调、退款、订阅购买、签到/奖励等都需纳入版本盘点。新请求开关失败只能暂停新准入，**付款成功回调、旧请求结算和恢复任务继续由兼容服务处理**。无需重建 PostgreSQL、不执行 `down -v`、不删除命名卷。有真实 v2 交易后不能用旧数据库快照回滚覆盖新资金。

### 13.8 页面、权限与导出实施清单

| 页面 | 实施行为 |
| --- | --- |
| 主站用户表及更新抽屉 | 与“调整额度”并列显示 Root 专有“代充”；金额、实付、到账和受益人清晰分栏 |
| 代充弹窗 | 报价→复核→二维码→支付状态；刷新恢复原订单，不自动重复下单；显示创建人、有效期、复制链接、已付款待入账提示 |
| 用户钱包 | 代充和自付都显示实际已支付订单；未付款订单单独列，不能加到余额/累计已入账 |
| 客户资金概览 | 兑换码/管理员/自付/代充四卡片，显示初始累计、当前可用、冻结；other/legacy 不隐藏，另列来源明细 |
| 充值记录 | 一次 funding operation 一行；多 tranche 展开；管理员调整实付列显示“—”，不写虚假的收款 0 元 |
| 调用用量 | 一次 charge 主行、段/组件/来源展开；按资金来源筛选；同步暂停/异常明确提示 |
| Root 运维/财务 | blocked paid、paid_late、poison、对账差异与待佣金分别可查；提供经验证重放入口，不直接改表 |

主站沿用 React 19、Base UI/shadcn、已有 Dialog/Table/Toast，字体/按钮/键盘焦点与原页面一致；无需更换技术栈。所有新增文本走各前端现有 i18n 管线，主站覆盖 en/zh/zh-TW/fr/ja/ru/vi，实施时必须加载仓库 i18n skill。前后端同时控制权限，不只隐藏按钮。二维码无障碍提供等价可复制链接，金额/错误信息不要只靠颜色表达。

金额明细敏感信息按角色脱敏：Root 可查完整内部 creator ID 和审计；受益用户只看本人，代理仅看其授权任期的运营信息。旧代理不得通过 lot/current summary 或 `as_of_money_seq` 观察解绑后的变动；新代理可看当前汇总，但来源跨旧任期的批次仅显示必要的“历史余额”，不泄漏旧订单/操作者。token 删除后保留 ID/显示快照，不回传 key；不存请求正文、密码或完整支付账号。

导出固定为两类独立文件：`usage-charges.csv` 每行一个 charge（tokens/调用数在此统计），`usage-funding-allocations.csv` 每行一个 allocation（不重复输出 tokens，含 charge/lot/operation ID 供关联）。充值导出为 funding operation，一笔支付 paid+bonus 不重复实付金额。CSV 防公式注入、按同一筛选快照和权限生成，过期下载失效，导出再次检查当前访问权限。

### 13.9 测试和验收证据模板

每项验收记录 `case_id、场景、初始余额、请求、预期、实际、关联 charge/order/operation、数据库种类、commit/image、执行时间、证据路径、pass/fail`。不能把“已有单元测试能跑”当作新增功能已完成。

| Case | 确定输入/故障 | 必须断言 |
| --- | --- | --- |
| F01 | 兑换 50、admin 30、paid 100；扣 80 | redemption=50、admin=30、paid=0；一调用两分摊 |
| F02 | 同上扣 100 | paid=20；总额/三来源守恒，原价折扣只计算一次 |
| F03 | 自付 20 先到账、代充 30 后到账；只有 paid，扣 25 | self=20、assisted=5；创建人不改变顺序 |
| F04 | 支付 100 paid+10 bonus；另有兑换 5，扣 20 | redemption5、payment bonus10、paid5；bonus零佣金 |
| F05 | 预扣100实际80，或预扣80实际100 | 尾部释放/按冻结规则增补；不重新排列已有分摊 |
| F06 | 退款累计40→80，重复发送40；另有新兑换入账 | 只恢复原lot，重复无增量，累计全退精确 |
| F07 | 同一码并发兑换；Blue/Green并发调用 | 只兑换一次、总余额/token不超扣，三库相同 |
| F08 | 两个不同订单并发回调接近钱包容量上限 | 一个正确阻断或都在容量内；不得溢出/少入账 |
| F09 | 多张兑换码逆序到账，分别早到期/晚到期/永不过期 | 早到期先扣、永不过期最后；过期未用额从总余额扣除，重复扫描幂等 |
| F10 | 兑换码额度消费后到期，再取消/退款原调用 | 调用全额冲销；到期部分进入 expired，不恢复可用余额、不参与偿债 |
| P01 | 创建代充但不付款，刷新页面/重复扫码 | 只有原订单，余额0增量，creator保留 |
| P02 | 同一合法通知重复、回调先于浏览器返回 | 一次入账；return页不入账 |
| P03 | 伪造签名/篡改beneficiary/quota/金额 | 拒绝；伪签不改变正常订单；前端不能控制quota |
| P04 | 创建响应超时、晚付、支付成功但容量不足 | 同trade_no恢复；真实收款有持久记录及明确后续 |
| P05 | 支付拒付与模型退款/预扣取消并发 | 不恢复无效paid，债务来源正确，累计冲佣不超原额 |
| R01 | 关闭佣金，完成新调用和四来源入账 | facts继续，commission job保留；再开启只入佣一次 |
| R02 | 事实worker暂停/poison/重启 | 显示同步状态，重放不重复，单用户异常不阻塞全站 |
| R02a | 支付已入账，暂停事实投影，再发送重复通知/真实退款 | 根据主库权威资金记录幂等/冲正，不依赖尚不存在的topup fact |
| R03 | Realtime多段、多个组件、内部渠道重试 | 会话1行、段数正确、usage不因allocation重复 |
| R04 | 代理转移、token删除、CSV下载越权 | 正确历史归属/快照，无API key或实时余额侧漏 |
| M01 | v1/组件v2/v3事件混合重放 | 原hash不变，新消费者不重复直写事实/佣金 |
| M02 | 旧无来源余额、在途任务、异步余额队列 | 不推测paid；等待/隔离后迁移，余额守恒 |
| M03 | 普通无代理用户四来源入账和调用 | 有来源账本、无伪绑定、原主站价格不改变 |
| M04 | 备份恢复/进程崩溃/蓝绿切换/兼容回滚 | 已收支付不丢、最终结算可恢复、不覆盖新交易 |

模型路径矩阵至少包含 Chat/Responses/Anthropic/Gemini 流式及非流式、Embedding/Rerank、图像/音频、视频/任务、Realtime、订阅回退钱包和旧任务入口。协议只在真实存在且启用的功能范围内测试；尚未支持的生产路径须显式禁用或先补齐，不能静默走旧钱包。测试 mock 上游和支付回调验证确定逻辑，另以受控小额真实支付验证 Epay 商户/二维码/回调链路；任何真实转账仍遵守明确授权，不伪造真实支付成功。

本地默认 SQLite；三数据库验证用隔离实例，不连接正式库跑写测试。Go/Bun/浏览器下载缓存、构建中间产物和数据库测试数据全部放 `E:/new-api-test-cache/`，避免继续挤占 C 盘。实施时按实际变更运行相关 `go test`、前端 typecheck/build、浏览器流程；如改动 relaykit 必须在独立模块 `GOWORK=off go build ./...`。全部通过后把上述表逐项回填为实际证据。

## 14. 工作量、里程碑与评审确认

以下为基于当前代码缺口的工程粗估，不是已验证工期。一个熟悉系统的后端、一名前端和测试配合时，可并行部分页面；支付与旧数据修复的不确定性需单列。

| 包 | 预计工程量 | 交付门槛 |
| --- | --- | --- |
| A：用户161证据、用量/佣金解耦、旧事件验证 | 3–5 人日 | 合法事件投影可恢复；不开启佣金也有用量 |
| B：全站批次来源、迁移、v2资金规则、退款/债务 | 8–13 人日 | 普通/代理用户三来源完整，三库守恒与并发通过 |
| C：代充报价、订单、收银台、验签、恢复 | 4–7 人日 | 小额支付与重复/失败回调闭环 |
| D：两套前端、报表权限、分摊与CSV | 4–6 人日 | 角色浏览器验收与精确金额展示 |
| E：全路径回归、压测、恢复与灰度上线 | 4–7 人日 | Go/No-Go证据齐全 |
| 合计 | 约23–38人日 | AI可加速编码，但不替代三库/支付/历史对账验证 |

优化、额外支付商户接入、历史无法还原资料的人工调查不计入以上确定范围。兑换码到账余额有效期已纳入本期核心范围。既有 05 文档的历史日结、组件拒付等未闭合项不能因新文档完成而自动视为已修复；凡与本次资金正确性直接相关的缺口必须纳入 B/E，不留到上线后。

可以先发布 A 修复代理用量可见性，再发布 B+C+D+E 完成四来源功能；A 不具备完整来源追溯，不得将其称为四来源项目完成。阶段一源事件处理只能恢复有证据的旧入账摘要，管理员/兑换码逐批次追溯要等 B，不能凭投影补出不存在的历史 lot。

给领导评审的核心决策为：确认用户161的根因；确认默认赠额/兑换码不计佣、代充实付计佣；确认三档顺序与非付费优先对佣金的影响；确认兑换码到期时间冻结到到账批次、未用额度到期失效并按 FEFO 消费；确认金额限制来自渠道与系统容量；确认先灰度代理客户后覆盖普通钱包用户；确认历史未知来源如实展示。设计确认后按本文开工，任何默认值改变必须同步更新验收样例。

## 15. 源码核验索引

| 核验点 | 当前源码（以基线提交为准） |
| --- | --- |
| 调额按钮与真实调用接口 | `web/src/features/users/components/user-quota-dialog.tsx`、`controller/user.go:1224` |
| 非付费增加不建lot/topup fact | `model/agency_funding.go:153`（ApplyAgencyQuotaDeltaTx） |
| 兑换截止与兑换入账 | `model/redemption.go:137`（Redeem） |
| 旧余额当paid初始化 | `model/agency_funding.go:959`（EnsureAgencyFundingAccount） |
| 在线支付lot及报表直写 | `model/agency_funding.go:1005`（RecordAgencyTopup） |
| paid-first与旧reserved语义 | `model/agency_funding.go:1723`（reserveAgencyFundingTx） |
| 原订单金额/重复支付 | `controller/topup.go:266`、`model/topup.go:207`、`model/topup_payment_snapshot.go` |
| 普通调用投影受佣金开关影响 | `pkg/agencyhub/worker.go:81` |
| 不计佣消费仍写用量 | `pkg/agencyhub/finance_service.go:403`、`pkg/agencyhub/agencyhub_test.go:395` |
| 页面只读usage/topup事实 | `pkg/agencyhub/report_service.go:899`、`:966`，`agency-web/src/features/reports/Pages.tsx` |
| Realtime直写例外 | `service/agency_gateway.go:195` |
| 组件来源/累计退款 | `pkg/agencycontract/funding_components.go`、`model/agency_component_settlement.go`、`model/agency_component_refund.go` |
| 债务偿还及来源恢复 | `model/agency_funding_debt_repay.go`、`model/agency_funding_debt_restore.go` |
| 价格与溢出约束 | `pkg/billingexpr/expr.md`、`common/quota_math.go` |

本轮验证范围是代码阅读、生产定向只读核验、文档内部一致性检查。业务功能实现、真实支付、数据库写迁移和新功能回归均尚未执行，不应向领导表述为“功能已经开发上线”。
