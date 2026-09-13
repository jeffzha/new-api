# 代理商旁路服务：需求逐项复核与实际验收记录

审查日期：2026-09-12（Asia/Shanghai）。基线：[05-agency-hub-sidecar-design.md](../pkg/doc/05-agency-hub-sidecar-design.md)。对象：本地当前工作树，以及生产 `gateway.nexus-reach.com` 的只读部署状态。

## 1. 结论和证据边界

**结论：未完成，No-Go。已经存在 new-api 网关联动，但不满足文档所要求的完整财务闭环、完整操作界面和上线门槛。问题不只是“新镜像还没部署”。**

本次完整阅读设计的第 0～24 节，核对接口、调用点、数据模型、实际提供页面的代码和部署配置；重跑现有测试，另加入按需求断言的验收探针。没有把接口存在、表存在、构建成功或旧报告写了“完成”当作业务验收通过。

状态定义：

- **已验证**：仅指定测试/操作及其明确断言通过；不推导未覆盖的生产场景。
- **部分**：实现了一部分，有通过证据，但同一目标仍有缺失或失败分支。
- **失败**：本次需求验收已实际复现错误。
- **代码缺口**：沿实际调用链确认缺少实现；未以生产故障注入验证。
- **未验**：缺少环境、专项用例或长时间运行证据，不能签收。

本次仅新增审查材料与隔离验收测试，未修复/替换业务实现、未推送或部署、未重启容器、未改生产开关或数据库、未调用付费上游、未执行真实充值或提款。原工作树中的业务改动全部保留。

## 2. 生产环境实际状态

只读 SSH 与公开健康接口核查结果：

| 项目 | 本次观察 | 能证明什么 |
| --- | --- | --- |
| `/agency/readyz` | HTTP 200 | Caddy 路由、hub 进程、数据库连接与基本表检查可用 |
| 佣金消费者 | `commission_worker=false` | 当前没有启用自动消费佣金事件 |
| 提现 | `withdrawals=false` | 生产提现能力关闭 |
| 导出 | `exports=false` | 生产导出能力关闭 |
| 投递队列 | `pending=4`，`claimed/retry/poison=0` | 存在待处理事件；不能仅据数量证明内容完整或佣金正确 |
| hub 镜像 | `new-api-seedance:agency-live-20260911-linux` | 运行的是 9 月 11 日的 hub 镜像，不是当前工作树的构建产物 |
| Blue 镜像 | `new-api-seedance:openaiseedance-1000-20260912` | 手工命名的网关镜像；名称本身不证明 agency 能力完整 |
| Green 镜像 | 20260910、提交标记 `gce11a2c498e6` 的 gateway 镜像 | 与 Blue、hub 不是统一构建版本 |
| Caddy 流量权重 | Blue 100 / Green 0 | 权重指向 Blue；本次未检查每个粘性 Cookie 的请求归属 |
| OCI revision 标签 | 多个不同镜像沿用 `6b1e4235...` | 标签有陈旧继承现象，不能据此确认实际二进制源码版本 |

健康响应中 `agency_durable_v1=true` 等字段在 hub 中是硬编码能力声明（`pkg/agencyhub/app.go`），不是对所有在线 gateway/worker 的能力探测。**不能把它当成蓝绿兼容验收。**

## 3. 第 1 节：12 个业务目标逐项结论

| 目标 | 实现与实测 | 结论 |
| --- | --- | --- |
| 1. Root 创建、编辑、启停代理商 | 有 `agency_service.go` 与 Root API；实际“创建”按钮未取得验证证明，POST 返回 403；编辑/启停交互未完整提供 | 失败 |
| 2. 每机构独立单账号 | 独立 operator 表、唯一约束、密码哈希及会话实现存在；新账号首次改密被前端卡住 | 部分 |
| 3. 真实 new-api Session SSO、明确代管 | SSO 签票、state、单用 jti、源会话检查有合同测试；实际页面未完成进入/退出代管，Compose 缺平台 SSO URL | 部分 |
| 4. 唯一长期邀请码与链接 | 创建邀请码、预览及链接/二维码测试通过；代理商邀请管理页面未完整提供 | 部分 |
| 5. 受邀客户为真实 new-api 用户、自行管理 Key | `controller/user.go` 邀请分支及原子注册测试存在；宿主注册页传 invite 并隔离 aff；未完成真实浏览器注册到模型收费的完整验收 | 部分 |
| 6. Root 转移、历史归属不变 | 绑定历史、active 指针、provisioning、历史事件授权有实现与部分测试；页面无转移/屏障进度完整交互 | 部分 |
| 7. Root C、代理商/Root S | 整包政策和销售 DTO 有实现，保留 C 的测试通过；页面仅打印 JSON，不能试算/发布 | 失败 |
| 8. 默认政策 + 精确对外模型例外 | 精确 UTF-8 model key、继承、BPS 校验有单元测试；模型选择/例外编辑未形成界面闭环 | 部分 |
| 9. 由最终实际结算和资金来源生成佣金 | 有 quote、wallet、journal、outbox、consumer；预扣无 journal、分离事务、Realtime 成本错、非计佣扣款次序错、异步提交提前最终化 | 失败 |
| 10. 客户、用量、充值、佣金、提现报表 | 部分查询/汇总/授权测试通过；实际使用/充值页点击客户无请求，Root 客户/账本 403；用量字段投影不完整 | 失败 |
| 11. 申请提现、人工审核并记录打款 | 账户加密、状态机、CAS/凭证测试存在；实际申请被 403；Root 无完整审批交互；对账异常未阻断支付入口 | 失败 |
| 12. hub 停机不影响网关，恢复自动补佣金 | 网关没有同步 HTTP 依赖 hub，投递重试/租约有测试；主账事实存在缺口，生产消费者关闭；30 分钟停机恢复未验 | 部分，不能签收 |

第 1.1 节排除项：当前没有交付多级代理商、第二操作员、自助绑定旧用户或自动银行打款。客户 Key 仍属于 new-api；默认查询响应避免展示 prompt/上游 Key。数据库强制最小权限与全部敏感字段扫描尚未通过，不能概括为“安全全部完成”。

## 4. 已复现的高优先级缺陷

### 4.1 网关计费与资金

| 编号 | 文档要求 | 实际复现/代码证据 | 影响 |
| --- | --- | --- | --- |
| G1 | §8.4 预扣成功必须同事务建立 reserved journal、冻结 quote | `TestAgencyDesignReservePersistsAcceptedJournal`：钱包从 200 扣至 100，但按 charge_id 查询 journal 为 record not found；`model/agency_funding.go` 的 reserve 未建 journal | 上游调用前没有完整持久财务锚点，无法保证崩溃后核验 |
| G2 | §3.2/9.1 禁用代理商后客户仍按旧政策调用，仅停佣金 | `TestAgencyDesignDisabledAgencyKeepsCustomerCharging`：拿禁用后的当前 state revision、commission=false 仍被拒；`ValidateAgencyPricingSnapshotTx` 硬要求机构 active | 禁用会影响客户 API，不符合业务承诺 |
| G3 | §2.4/8.6 所有真实钱包扣减 paid 优先 | `TestAgencyDesignNoncommissionableDebitUsesPaidFirst`：paid=100、nonpaid=100，余额购买订阅扣 50 后先减少 nonpaid | 改变之后模型消费的 paid 比例，可能多发佣金 |
| G4 | §2.1 C 从同一原始 basis 计算 | `TestAgencyDesignRealtimeCostUsesOriginalBasis`：Q=1000、S=.9、C=.75，期望 T=750/G=150，实际 T=675/G=225 | Realtime 结算成本偏低、理论佣金偏高 |
| G5 | §8.6 unlimited 也必须校验存储累计边界 | `TestAgencyDesignUnlimitedTokenPreservesInt32Bounds`：边界上再预扣 10 仍成功，SQLite 写入 used=2147483657、remain=-2147483657 | 不满足跨数据库字段边界，不能依赖生产库截断/报错保护 |
| G6 | §8.4 wallet/token/funds/journal/outbox 同事务 | `service/billing_session.go` 结算与 `service/agency_gateway.go` 记录事件分开提交，delta=0 提前返回仍存在 | 现有“原子钱包测试”不证明整条财务链原子 |
| G7 | §8.5 submit 不等于财务最终，发上游前落 submit_attempt | `AgencyTaskSubmissionAttempt` 仅有表定义/迁移；正常任务提交链调用 `SettleBilling` 后写 finalized、FinancialFinal=true | 超时/崩溃窗口缺持久提交证据，佣金可能在任务终态前产生 |

G1～G5 已通过隔离需求测试复现失败；G6/G7 为真实调用链审查发现，未在生产制造崩溃或生成收费视频。

### 4.2 消费者、对账和提现联锁

| 编号 | 文档要求 | 实测结果 | 影响 |
| --- | --- | --- | --- |
| R1 | §9.2 消费前核验原 journal/operation | `TestAgencyAuditConsumerDoesNotPayWithoutCommittedJournal`：有效 hash 的 outbox 没有对应 journal/operation，实际仍生成 1 条佣金 | hash 只能证明 payload 一致，不能代替已提交扣费事实 |
| R2 | §10.1 有未解决资金异常禁止开始打款 | `TestAgencyAuditReconciliationIssueBlocksPayment`：Reconcile 已生成佣金不平 issue，但 paying gate 返回 nil | issue 使用 `agencyID:currency`，gate 只匹配 agencyID 或 currency，查询未联通 |
| R3 | §16.2 未支付提现金额之和等于 locked | `TestAgencyAuditReconciliationDetectsUnbackedWithdrawalLock`：无提现单却 locked=20，汇总恒等式相等时未报异常 | 只验余额等式不足以校验冻结资金 |
| R4 | §16.2 同一 money_seq/operation 截面 | `TestAgencyAuditReconciliationDoesNotCompareDifferentCommittedVersions`：在两次 SELECT 之间原子更新 wallet/funding，前后均平账，扫描却报 1 个 issue | 正常并发充值/扣费可能触发假差异 |

此外：`reconcile.go` 目前只做 funding/user、佣金汇总等式、active binding、outbox 缺 delivery 四类检查；没有 lot 流转、journal/allocation/debt/token、operation event_count/永久回执、累计退款/双冲、usage/journal 全套检查。每轮全表读和无界 IN 不是增量扫描；没有每日北京时间 02:30 调度。`ignored` 从健康状态 open 计数移除，resolve 主要修改状态/说明，未形成带补偿事件的资金修复流程。

### 4.3 实际提供的页面

真正的 `/agency/` 由 `pkg/agencyhub/app.go` 调用 `static.go` 内嵌 HTML。`agency-web` 的 React build 产物没有进入该服务的静态文件链路。以下测试执行实际 HTML、DOM 点击和真实 Gin API；仅使用本地合成 SQLite 数据，不 mock API。

| 场景 | 实际结果 |
| --- | --- |
| 首次登录 | `must_change_password=true` 后页面请求业务接口得到 `password_change_required`，没有改密表单，形成死端 |
| Root 客户页 | 请求 `/customers?limit=200`，得到 403 `agency_required`；路径映射仅处理完全等于 `/customers` 的情况 |
| Root 佣金账本 | 未建立服务器端代管上下文，403 `agency_required` |
| Root 新建代理商 | 未取得一次性验证证明，403 `verification_required` |
| 新建收款账户 | 账户字段匹配 DTO，但缺验证证明，403 `verification_required` |
| 申请提现 | 缺验证证明，403；另 `account_id` 被转换成 Number，后端要求十进制字符串，补 proof 后仍需修参数 |
| 销售/结算价格 | 仅 JSON 展示，无编辑、例外、试算、发布和版本对比 |
| 客户使用明细/充值记录 | 点击客户行没有触发详情 API 请求 |
| 客户筛选 | 输入筛选值不改变结果，没有绑定筛选逻辑 |
| Root 代管 | 无完整 enter/leave 操作；本地更改 me.agency_id 不等于服务器更新 Session |
| 提现审核/支付/unknown | 后端有路由，实际页面没有完整操作流程 |
| 邀请、CSV、密码安全 | 代理商专用邀请操作、导出任务/下载、账号安全页未完整交付 |
| 显示规范 | 微单位金额/Unix 时间直接显示；多币种仅取第一项；分页未消费 next_cursor；页面硬编码中文 |

本次 DOM 操作共 19 项：7 PASS、12 FAIL。通过项是页面可提供、Root 概览与机构列表、代理商概览与客户列表等有限行为，不代表剩余菜单可用。不是 Chrome 视觉/无障碍验收，后者仍未验。

### 4.4 安全与部署的独立验收失败

| 编号 | 文档要求 | 本次实际结果 |
| --- | --- | --- |
| S1 | §5.3 操作员高风险证明绑定签发 Session | Session A 获取证明，在同账号 Session B 创建收款账户得到 HTTP 201，要求应拒绝；普通 operator proof 没有绑定 Session。Root proof 的 Session 检查已存在，不能混为同一问题 |
| S2 | §13 提现 account_id 为精确十进制字符串 | 实际页面传数字 `1`，后端解析返回 `value must be a decimal string`；必须传 `"1"` |
| D1 | §19/20.3 内部命令双向 TLS 验证客户端身份 | 持合法服务签名与 Root proof、但没有已验证客户端证书的 TLS 请求仍得到 HTTP 202，期望 403。应用签名/Root proof 仍校验，这不是匿名任意资金操作 |
| D2 | §19 readiness 必须拒绝不完整财务 schema | 隔离库删除 `agency.price_revision` 后，`/agency/readyz` 仍 HTTP 200 且 schema.ready=true |
| D3 | §17 导出任务进程崩溃可恢复/过期终止 | 构造已过期 48 小时的 processing 任务，运行 worker 与 cleanup 后仍 processing，无 lease/恢复机制 |

上述 5 项是单独的真实 API/数据库验收失败，加上 G1～G5、R1～R4，共 **14 项后端/契约/安全/恢复验收失败**；另有前述 12 项页面操作失败。不能把这些验收描述成“测试已经全部通过”。

## 5. 第 2～20 节技术要求逐项矩阵

同一行列出紧密相关的原子要求；“部分”不能作为该行通过。文件路径均相对仓库。

| 文档 | 功能点 | 代码/测试及实际边界 | 结论 |
| --- | --- | --- | --- |
| §2.1 | 同 basis 算 B/T；S 替换原 group 一次；保留路径舍入 | 有 quote/Calculate/计费接入；G4 实测失败，全部协议黄金 fixture 不完整 | 失败 |
| §2.1 | C=0 明确零成本 | `pkg/agencycontract/contract_test.go` 的零值/继承测试通过 | 已验证：纯函数范围 |
| §2.1/8.6 | 单笔 checked quota、财务 int64、原字段累计边界 | 既有 quota_math 防溢出测试通过；G5 越界失败；daily stats 仍有裸累计加法 | 部分 |
| §2.2/2.3 | Token/视频实例收费 | 黄金佣金纯函数存在；未把两种示例全部走真实收费生命周期 | 未验完整链路 |
| §2.4 | paid/nonpaid/debt 守恒、paid FIFO | model funding 核心测试通过；非计佣扣款次序 G3 错 | 部分 |
| §2.4 | 多组件比例分配/最大余数/稳定 component_id | ledger/usage 仍写固定 `default`，未形成文档要求完整组件矩阵 | 代码缺口 |
| §2.5 | 按 origin_model_name 精确匹配，不按渠道/上游名 | `ModelKey`、Resolve 及相应测试存在；三库运行证据不足 | 部分 |
| §2.6 | 币种桶、冻结换算、退款原金额、TOKENS 禁止现金提现 | 有 snapshot、commission balance、提现币种处理；未全链路验改汇率/币种并退款 | 部分 |
| §3 | 独立服务、网关不反向同步调用 hub | 网关引用 contract/model/service；hub 独立 binary/router，代码审查确认 | 已验证：结构 |
| §3.2 | 停用不改客户价格、不影响模型、停新增佣金 | G2 失败 | 失败 |
| §4 | 单 operator、Root/operator 分权、客户只用原系统 | 模型唯一键、requireRoot/ownAgency、对象授权测试存在 | 部分：全页面未通 |
| §5.1 | 12～72 字节密码、哈希、首次限制、锁定 | auth.go 有逻辑；首次限制有效但 UI 无法完成改密 | 部分 |
| §5.1 | 5 分钟临时密码密文、同 Root 恢复、ack 销毁 | delivery_secret_test 的 AAD、过期、scope、redaction 测试通过 | 已验证：合同范围 |
| §5.2 | Session-only SSO、state/jti/aud/时间、源会话撤销 | sso_contract_test、controller SSO 测试通过；线上完整浏览器 SSO 本轮未执行 | 部分 |
| §5.3 | Cookie、CSRF、Origin、会话撤权、proof audience/action/body | 现有 scope/版本/CSRF 实现与部分测试通过；页面未请求 proof，内网传输见 §19 | 部分 |
| §5.3 | 操作员 proof 绑定签发 Session | S1 真实双 Session 验收失败；Root proof 的通过不能替代 operator proof | 失败 |
| §6.1 | crypto 邀请码、公开脱敏预览、禁用预览、固定站点链接 | 创建与公开预览测试通过；唯一冲突重试完整覆盖未验 | 部分 |
| §6.2 | 真用户、零额度、绑定/资金/默认 Token 原子注册 | controller/user_agency_invite_test、model 邀请注册分支存在并通过现有测试 | 部分：端到端未验 |
| §6.2 | Registration-Idempotency-Key；提交后丢响应可重试 | controller 注册没有读取该键/保存同一成功结果；重复账号被拒不能替代幂等注册 | 代码缺口 |
| §6.2 | invite/aff 互斥、拒 OAuth invite、尊重全局注册开关 | 宿主注册表单传 invite 并隐藏 OAuth；服务端有相关拒绝分支/测试 | 部分：所有开关组合未验 |
| §6.3 | 旧用户屏障、排空、fencing、取消 | provisioning worker 的 drain/blockers/cancel/stale fence 测试通过；全节点在途长连接排空未验 | 部分 |
| §6.3/20.3 | 全节点在途工作排空、最终绑定重验 Root | 当前 blocker 主要查询通用 Task，缺跨节点流式/Realtime/MJ/Batch 排空，最终事务未完整核验源 Root Session | 代码缺口 |
| §6.3 | 原子转移、锁序、历史事件范围、资金不重建 | binding/history 模型及转移/授权逻辑存在；真实长任务并发切换未验 | 部分 |
| §7.1/7.4 | 默认/模型 C/S、null 继承、cap/spread、S>0 | ValidatePolicy 与 model-key/零 C 测试存在；全部 DTO 边界与收费模型 S=0 拒绝未全面覆盖 | 部分 |
| §7.2/7.3 | Root/operator 配价、试算、发布 | 后端有；实际页面无操作 | 失败 |
| §7.3 | 当前客户有效价格、公开页提示、Key 路由组不能绕价 | `controller/agency_pricing.go` 有接口和测试；`web/src` 未找到有效价格 API 的消费入口 | 部分 |
| §7.4 | 整包不可变发布、expected_revision、C/S DTO 分离 | pricing_service、销售发布保留 Root C 例外测试通过 | 部分：竞态及全量 DTO 仍待验 |
| §7.4/7.5 | 发布/转移/禁用与 reserve 权威版本核验 | 有 ValidateAgencyPricingSnapshotTx，但禁用语义失败且 reserve 未持久完整 quote | 失败 |
| §8.1/8.4 | 响应交付、业务状态、财务状态分开 | 多处现有 settle/事件调用未组成专用持久状态机 | 代码缺口 |
| §8.2 | 全部资金入口清单与真实接入 | topup/admin/checkin/redemption/subscription 等有接入；记录事件与扣费仍分离 | 部分 |
| §8.3 | 重启后可重算 billing_basis、表达式依赖、usage 白名单 | 有 BillingBasis 文本/快照，但不能等价于完整冻结引擎输入；普通 usage 投影缺字段 | 部分 |
| §8.4 | 专用 AgencyBillingSession、reserve=0、delta=0、同步退款 | 仍为旧 BillingSession 分支，delta=0 早退、退款独立提交 | 代码缺口 |
| §8.4 | durable 永久标记、不因开关/缓存回旧钱包 | 有稳定 BillingMode 与受管分支；未完成全部缓存/工厂/未知提交边界 | 部分 |
| §8.4/8.6 | 真实用量超额记 debt、reconcile_blocked 阻断、已删除 Token 在途结算 | 普通补扣仍受余额不足检查；网关未接完整 reconcile_blocked 状态流；删除 Token 后补扣读取失败 | 代码缺口 |
| §8.5 | Chat/Responses/Claude/Gemini 各协议黄金收费 | 共享文本收费路径确有接入；没有每种协议完整 agency 生命周期黄金断言 | 未验全部路径 |
| §8.5 | audio/embedding/rerank/image/fixed/expression | 共享价格/收费函数存在；不能用通用测试证明各路径的 T/佣金正确 | 未验全部路径 |
| §8.5 | 视频提交持久化、未知结果、finalize CAS | G7，submit_attempt 缺生产写入，提交即最终化 | 代码缺口 |
| §8.5 | Realtime 持久分段、累计 usage 去重、失败尾段 | 已有分段测试，但成本 G4 错，扣款与分段事件仍分离 | 失败 |
| §8.5 | Midjourney 老任务提交与退款 | 有 WithSequence/RecordAgency 接入；完整 crash/最终事实联锁不足 | 部分 |
| §8.5 | 失败但收费、免费、测试、违规费零佣金 | 有 flags/非计佣接入；非计佣来源次序 G3 错，全部失败协议未验 | 部分 |
| §8.6 | 实际 credited_quota、paid/bonus、人工补单 nonpaid | topup/funding 现有 tests 通过，不从统一 Amount 猜单位的实现存在 | 部分：每支付商链路未逐端到端验 |
| §8.6/11.3 | 充值真实支付金额/币种、赠额拆分事实 | 当前真实支付桥接传 paid=credited、bonus=0，缺实际支付金额字段贯穿；TopupFact 未填完整实际金额 | 代码缺口 |
| §8.6 | 管理绝对改额、签到、兑换、aff、订阅购买 | 相关 model 入口有受管处理及测试；锁/事件/paid 次序不是全通过 | 部分 |
| §9.1 | 成功+最终+paid 才入佣金 | 生产消费条件依赖 event flags，缺完整 journal 核验，R1/G7 | 失败 |
| §9.2/12.2 | pending/retry、低 ID 晚提交、租约 fencing、回执原子 | lease/low-ID/poison/hash/reversal-before-original 现有测试通过 | 已验证：这些消费分支 |
| §9.2/12.2 | 每用户 money_seq 完整、同 operation event_count | 有前序计数等待；未按 operation/event_index 完整性逐项校验、reconcile 也未补上 | 部分 |
| §9.3 | 累计部分模型退款、微额残差、组件/原 lot 归还 | 退款函数与部分测试存在；多组件和原 K/M 累计闭环不足 | 部分 |
| §9.3 | 支付拒付、冻结债、已还债来源恢复、防双冲 | 多个 funding debt/reversal 测试通过；网关/消费者完整冲正链未全验 | 部分 |
| §9.4 | 可用/冻结/已付守恒、多币种、负余额 | checkedAdd、余额/冲正测试通过；R2/R3 显示联锁/对账不完整 | 部分 |
| §9.4 | 负佣金余额可由新赚佣金逐步补足 | finance_service 正向入账后 available 仍为负会报错，-100 新赚 50 不能变成 -50 | 代码缺口 |
| §10.1 | submitted/reviewing/approved/paying/unknown/paid 状态与 CAS | unknown/reversal/unique-reference/version 测试通过；UI 申请审核不通 | 部分 |
| §10.1 | paying 前检查所有资金异常、已有单 on_hold | on_hold 测试通过；R2 异常查询失败 | 失败 |
| §10.1 | 已付款后冲正仍如实 mark-paid、备份防重打 | 部分状态机测试存在；真实银行核查和备份恢复演练未做 | 部分/未验 |
| §10.2 | AES-GCM/AAD、轮换、尾号、账户不可变版本 | 加密/轮换/快照测试通过；账户创建需 UI proof；数据库 account_versions 完整目标另待核对 | 部分 |
| §11.1～11.3 | 模型/迁移完整、不只是空表 | AgencyModels 覆盖多数表；task_submission_attempts、archive_manifest 的存在不代表功能已使用 | 部分 |
| §11/13.1 | int64 ID/quota/micros 字符串，时间统一 | 部分 DTO 用字符串；Agency/Identity/operator 客户列表仍直接数值 ID，时间秒/毫秒混用 | 代码缺口 |
| §11.4 | 三库 migration/短索引/并发锁 | SQLite 实测通过；MySQL/PG 缺本机 DSN 跳过，不能签三库通过 | 未验三库 |
| §12 | 版本、hash、白名单事件，不泄露敏感数据 | schema/hash 隔离测试通过；事件投影 lacks journal validation/完整 usage | 部分 |
| §13.1 | 统一错误、字段错误、CSRF、幂等、cursor、大小上限 | 多个合同测试通过；部分列表直接裸模型、历史分页未完整，所有业务写 proof/版本未端到端覆盖 | 部分 |
| §13.2 | 宿主 SSO/verify/effective-pricing/register 路由 | router/api-router.go 有实际宿主路由；宿主导航与有效价 UI 未接齐 | 部分 |
| §13.3 | 机构 CRUD、enter/leave、bind/transfer、delivery ack | hub app.go 路由齐备；用户界面不齐，命令执行边界见 §19 | 部分 |
| §13.4 | 两类政策 DTO、模型搜索、历史、diff | 后端有，前端 JSON 占位；模型目录直接 join abilities/channels 需调整最小权限访问 | 部分 |
| §13.5 | 客户详情/历史授权、日期筛选、usage 全字段 | customerUsage/Topups 仅部分分页字段，未接完整日期/模型筛选；API usage 缺输入/输出/cache/规格 | 代码缺口 |
| §13.5 | 汇总金额按全筛选，日期含尾日 | report_service_test 的汇总/日期边界通过 | 已验证：特定报表范围 |
| §13.6 | 账户/提款/冲正/对账运行 API | 多数路由存在；`POST /root/reconciliation/runs` 没有注册 | 代码缺口 |
| §14.1 | Root 全部管理页面 | 实际 DOM 8 项中 5 FAIL；详情流程仍缺 | 失败 |
| §14.2 | operator 全部运营/账号安全页面 | 实际 DOM 9 项中 6 FAIL，首次登录另 1 FAIL | 失败 |
| §14/20.3 | React19+i18next 独立前端、可访问性、空状态 | 服务用硬编码 HTML；React 项目另有自定义双语字典，不是 i18next，未接服务；视觉/键盘未验 | 代码缺口 |
| §15.1 | 缓存只加速、DB 权威、Redis 故障回源 | 有 DB 解析/受管分支与部分 tests；未完成断 Redis 的实流量恢复验收 | 部分 |
| §15.2 | 实测事件放大 k/行宽、保留预算、100 万追平 | 旧 benchmark 不等于当前环境证据 | 未验 |
| §15.3 | 60 RPS 1 小时、120 RPS 5 分钟、单用户/单机构热点 P99 | `TestDrainConsumerBudgetSustainsAbove60RPS` 仅一次排 700 事件，没有持续速率或 P99 断言 | 未验 |
| §16.1 | 权威恢复、未知上游可查且不盲退、故障不依赖日志 | 存在 outbox 重建与重试，但 G1/G6/G7 使完整恢复依据不足 | 部分 |
| §16.2 | 增量/02:30/同截面/9 类公式/修复操作 | 只有 4 类全表检查，R2/R3/R4，缺 runs API | 失败 |
| §16.3 | 主库+密钥一致备份、恢复停写、银行对账 | 运维说明存在，当前环境没有恢复演练证据 | 未验 |
| §17 | 永久财务与回执、归档回读后清理、冷热查询 | archive manifest 模型存在，归档链未实现；一期可不归档但不得删历史，需容量预算 | 部分/未验 |
| §17 | 异步私有 CSV、复核授权/撤权、10 分钟下载、BOM/注入 | 现有 CSV/filter/tamper/path/cleanup 测试通过；无完整 UI；处理状态崩溃缺 lease 恢复 | 部分 |
| §17 | 百万行限制、不截断、流式/容量 | 有 count 上限但 Find 整批进内存；百万级导出未验 | 部分 |
| §18 | 所有对象范围隔离、Secret/日志脱敏、安全头/限流 | 部分授权/密码/加密/CSV 测试通过；未完成全面权限矩阵和真实部署隔离 | 部分 |
| §18 | 登录 IP+账号/验证/导出限流、管理站 CSP、SSO 多 kid 轮换 | Router 未接完整限流，管理 HTML 未设完整 frame 保护，SSO 单公钥结构；锁账号不等于 IP 限流 | 代码缺口 |
| §19 | 网关写资金、hub 最小权限视图、独立 migrate 账号 | 现有 hub 仍直接查源用户/会话表；完整 GRANT/受限视图运行未验 | 代码缺口/未验 |
| §19/20.3 | 网关专用内部 mTLS command listener、签名/原 Root proof | 签名/命令 queue/worker 有实现；未形成文档要求的独立网关 mTLS 监听部署 | 部分 |
| §19/20.3 | 客户端证书验证 | D1：仅 Request.TLS 非空便通过传输校验 | 失败 |
| §19 | schema 验证与 readiness | D2：只检查表、不检查必要列与迁移版本，缺字段仍成功 | 失败 |
| §19 | onboarding 独立开关 | 没有 AGENCY_ONBOARDING_ENABLED 的实际读取；关闭消费者不能代替关闭新开通 | 代码缺口 |
| §19 | Secret/SSO URL/导出卷/共享网络/资源池/开关配置 | Compose 与 README 存在缺项/错误 env 名；线上三开关关闭 | 部分 |
| §19 | migrate-first、全 slot+worker 能力确认、旧实例排空、兼容回滚 | 文档有顺序说明；发布脚本未执行完整 agency 能力门禁 | 代码缺口/未验 |
| §20 | contract→model→service、hub 独立、不污染 relaykit | Go build、relaykit 独立 build 通过；资金生命周期仍需跨目录实现 | 部分 |

## 6. 第 21～23 节：验收门槛与保证清单

Phase A～E 均不能整体标记完成：A 的完整黄金 fixture/真实三库证据不足；B 有 G1/G3/G5/G6；C 有 G2/G4/G7 及宿主页面缺口；D 有 R1～R4 与报表缺失；E 页面提款/导出未通，容量、备份、回滚未验。

| §22 验收组 | 本次结果 |
| --- | --- |
| 22.1 原用户/S=1/每协议/边界/并发/预扣差额/资金来源/回调/路由/订阅/Token/Realtime/异步 | 现有单元测试通过不覆盖全部 13 组目标；已新增 5 个失败财务探针，不能标“通过” |
| 22.2 版本竞态、DTO、SSO、注册回滚、跨机构、临时密码、CSV | 已有部分真实合同测试通过；UI 12 处失败，全链路竞态和全权限矩阵仍待验 |
| 22.3 崩溃窗口、低 ID、lease、重复/乱序、退款、提现、备份防重打 | 现有低 ID/lease/退款/提现状态测试通过；G1/G6/G7/R1/R2 阻止整体签收 |
| 22.4 长时容量、热点、100 万积压、三 DB、停机/Redis/主库/蓝绿/恢复 | 本轮不具备完整证据；未用旧机性能数替代，不进行生产压测 |
| 22.5 所有门槛通过才开通 | **No-Go；不能现在直接把三个财务开关打开** |

§23 的 15 项保证，逐项对应：

| 保证项 | 结果及阻断原因 |
| --- | --- |
| 独立单账号/真实 Session/抗重放 | 部分；SSO 合同通过，首次改密/代管界面失败 |
| 邀请注册同事务零额度无 aff | 部分；有代码和测试，未完整浏览器/全故障点验收 |
| durable 永不回旧钱包/所有余额含 Token 原子 | 不通过；G1/G5/G6 |
| 默认+例外/C-S 权限/原子发布转移 | 部分；政策测试通过，G2 与 UI 发布缺失 |
| 冻结 basis/舍入/C-S/币种 | 不通过；G4、完整 basis/黄金 fixture 缺口 |
| paid 预扣与所有非计佣扣减/充值单位 | 不通过；G3 |
| 每种协议/任务均有 journal 与未知结果闭环 | 不通过；G1/G7 |
| 扣费/退款/outbox 原子、money_seq、不依赖日志 | 不通过；G6/R1 |
| 佣金/微额/累计退款/拒付/溢出 | 部分；资金退款 tests 通过，但 G4/G5 和多组件不足 |
| 负余额冻结、unknown 核查、付款事实、恢复防重打 | 部分；状态 tests 通过，R2 与恢复未验 |
| 事件范围/当前权限/敏感字段/CSV | 部分；授权与 CSV tests 通过，部署 DB 隔离不足 |
| 永久回执/归档可检索/重放不再计佣 | 部分；回执存在，归档/恢复重放未验 |
| 全 slot/worker 兼容后开通、回滚不降级 | 未验，发布门禁缺失 |
| 60 RPS/热点/积压/磁盘/三库/故障恢复 | 未验 |
| 手册区分已实现与目标、披露资金优先/退款/提现 | 不通过；旧审计材料高估完成度，产品页面也未完整显示规则 |

## 7. 本次执行的测试、可复现材料

| 检查 | 最新结果 | 限定 |
| --- | --- | --- |
| `go test -json ./... -count=1` | 49 个有测试包通过；1,870 条 Test/subtest PASS，5 条 SKIP；62 个无测试包跳过 | 不含 opt-in 需求探针；不是 1,870 个业务需求全部通过 |
| `go build ./...` | PASS | 编译，不证明业务完整 |
| `go vet ./pkg/agencyhub ./cmd/agency-hub` | PASS | 静态检查范围有限 |
| relaykit `GOWORK=off go build ./...` | PASS | 独立模块可构建 |
| agency-web `bun run build` | PASS，含 tsc + Vite | 该产物没有实际接入 hub 服务 |
| 宿主 web `bun --bun run typecheck` / `bun --bun run build` | PASS | 默认 Node 16 执行 tsgo 曾失败；显式使用 Bun 运行后类型检查与生产构建成功，无依赖降级/业务修改 |
| 财务/消费者/对账需求探针 | 9 项 FAIL | 按文档期望断言，故意不改成“接受当前错误”的绿测试 |
| 操作员 proof / 提现 DTO 独立探针 | 2 项 FAIL | S1/S2，使用真实 handler 与合成账号/账户 |
| mTLS / schema / 导出恢复探针 | 3 项 FAIL | D1～D3，仅隔离测试库/测试服务器 |
| 实际页面 DOM + Gin API 操作 | 19 项：7 PASS / 12 FAIL | 本地真实路由/合成数据；jsdom，不是视觉浏览器 QA |
| MySQL/PostgreSQL | 未验 | 本地 Docker daemon 未运行，未配置一次性测试库；外部迁移测试 SKIP，资金并发还用日志报告方言跳过 |
| 线上健康/容器/开关 | 完成只读检查 | 无生产写操作、真实财务业务验收 |

运行目录为仓库根。普通测试与需求验收必须分别看结果：

```powershell
# 常规回归
go test ./... -count=1

# 需求验收：当前预期报告 9 项失败，修复后必须全部转绿
go test -tags agency_audit ./service ./pkg/agencyhub -run '^(TestAgencyDesign|TestAgencyAudit)' -count=1 -v

# 实际提供页面 + 实际后端 HTTP；使用本次审查 overlay
go test -overlay artifacts/agency-review/frontend/overlay.json ./pkg/agencyhub -run 'TestFrontendAcceptanceAudit|TestFrontendAudit' -count=1 -v

# 部署边界/故障恢复：当前 3 项失败
go test '-overlay=artifacts/agency-audit-20260912/deployment-overlay.json' ./pkg/agencyhub -run '^TestAuditDeployment' -count=1 -v
```

验收测试使用 `//go:build agency_audit` 明确隔离，默认 `go test ./...` 不会执行它们。上线流水线必须明确运行该验收命令，不能仅引用普通回归的绿色结果。

原始证据：

- [全仓 Go 测试 JSONL](../artifacts/agency-audit-20260912/go-tests.jsonl)
- [9 项需求验收 JSONL](../artifacts/agency-audit-20260912/acceptance.jsonl)
- [Session 证明/提现 DTO 验收 JSONL](../artifacts/agency-audit-20260912/security-dto-acceptance.jsonl)
- [mTLS/schema/导出恢复验收 JSONL](../artifacts/agency-audit-20260912/deployment-acceptance.jsonl)
- [Root 页面逐请求结果](../artifacts/agency-review/frontend/root-results.json)
- [代理商页面逐请求结果](../artifacts/agency-review/frontend/operator-results.json)
- [首次登录结果](../artifacts/agency-review/frontend/first-login-results.json)
- [财务验收测试](../service/agency_design_audit_test.go)
- [对账与支付联锁验收测试](../pkg/agencyhub/reconciliation_acceptance_audit_test.go)
- [消费者权威事实验收测试](../pkg/agencyhub/consumer_acceptance_audit_test.go)
- [前端、身份与 API 逐项专题报告](../artifacts/agency-review/frontend-audit.md)
- [网关联动、资金与逐协议验收专题报告](../artifacts/agency-audit-20260912/gateway-audit.md)
- [部署、三库、恢复与生产只读专题报告](../artifacts/agency-audit-20260912/deployment-audit.md)
- [部署故障验收源码](../artifacts/agency-audit-20260912/deployment_regression_test.go.txt)

测试质量补充：定向组合运行 service 的 Agency/Midjourney 测试曾出现 `users.aff_code` 唯一键冲突，单独运行对应用例及本次全仓测试通过。审查发现测试软删除残留与后续 fixture 空 aff_code 冲突，以及一处清理表名为错误单数。这是测试隔离缺陷，不能误报为生产付款失败，也不应因全仓一次通过而忽略。SQLite 的资金“并发”测试分支实际按顺序运行；真正行锁竞争仍需真实 MySQL/PostgreSQL 验证。

## 8. 修复与重新验收顺序

1. **先修资金权威链**：把受管 reserve/finalize/cancel、用户/Token 边界、lot 分配、journal、operation、outbox/delivery 放进完整原子生命周期；修禁用策略、Realtime 同 basis 成本、非计佣 paid-first。
2. **补异步任务安全边界**：发上游前持久提交尝试，返回任务前持久 ID；未知留待核验；只在最终业务/账单确定后 finalize，避免提前入佣金。
3. **补消费者核验与完整对账**：原 journal/operation 校验、event_count、同截面、9 类公式、每日调度、带证据修复，并修好对账 issue 到 paying gate 的联锁。
4. **统一实际前端交付链**：明确构建与服务的唯一页面入口；补首登改密、Root/operator proof、代管、机构 CRUD、邀请码、价格编辑/试算/发布/历史、客户详情、报表/筛选/分页/CSV、提款全状态机、i18n/金额/时间显示。
5. **补部署配置和门禁**：迁移角色/运行账号权限、平台 SSO URL、Secret、私有导出卷、共享网络、内部 mTLS、资源限制、所有 slot/worker 能力与旧任务执行权排空；校正镜像源码标记。
6. **在隔离环境签收**：14 项后端/安全/部署契约失败探针与 12 项页面操作失败全部转绿，增加逐协议黄金、真实三库、crash/回滚/恢复与规定长时容量试验。普通用户回归也须继续通过。
7. **最后按 §19 部署**：备份及恢复证据→迁移→所有网关兼容→hub→影子核账→消费者→对账→提现。当前不能用“重启/打开开关”代替上述实现与验收。

本报告取代旧材料中“Phase A～D 完成”“本机可闭合全部通过”“只剩窄缺口”的判断。旧测试依然具有局部回归价值，但它们没有覆盖本文复现出的关键业务要求。
