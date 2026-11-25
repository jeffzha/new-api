# 交接文档：代理商旁路服务（Agency Hub / Distributor）

## 2026-11-25 产品钱包由 OPS 接管：开发状态，禁止按此草稿上线

本节是当前任务的状态记录，不覆盖下文历史发布记录。工作树 `C:/Users/16062/.codex/worktrees/product-ops-wallet-20261125/new-api`，分支 `codex/product-ops-wallet-20261125`，基于产品 `afcc9a12b`。本节所述钱包接管改动仅为隔离开发检查点，尚未合并到产品/项目或部署，提交及远程状态以该分支 Git 记录为准。原定北京时间 2026-11-25 03:20 的发布未执行；现网未切换资金归属，OPS 源码及生产数据库、运行配置均未由本任务修改。

### 用户确认的资金边界

- 目标：产品与项目复用同一套 OPS 资金适配；所有充值、代充、兑换、奖励、增减/覆盖、调用扣款和退款由 OPS 记账，余额/累计金额/调用前金额判断都以配对 OPS 为准。聚合保留支付验证、订单/兑换资格、用量事实、权限和可重试业务记录。
- 不能先修改聚合本地金额，再发送第二次资金操作给 OPS。旧代码可保留但接管后不得执行本地资金写入；本地余额只能是标注来源的投影，不能失败时偷偷回退成另一套钱包。API Key 配额预算仍是独立的访问限制，不是用户资金。
- 接口依据为 OPS 产品仓库 `docs/contracts/STATION_INTEGRATION_SPEC_v1.md`：第 9/16.7 节资金事件、16.7 节余额/扣款结果、第 17 节后台调额。管理员 add/subtract/set 走 `POST /api/v1/catalog/v1/balance-changes`，同一操作不再推 adjustment；支付/赠送/退款走有稳定业务编号的 usage-events。HTTP 接收成功不等于资金已经入账。
- 部署必须不中断现有请求、流、任务及支付回调。不得增加维护/写暂停门禁或故意返回 409/503 来完成切换。按本仓库及技能规定完成隔离验证、不可变构建、零流量候选验证、静态资源归档、渐进蓝绿与连接排空。

### 本地已实现的草稿与验证边界

- 共享 OPS 余额/扣款读取及管理员调额；资金业务事实与不可变 outbox 同事务提交，不增加本地 User.Quota。事件冻结业务编号、金额换算、账本 scope/目标；超时/丢 ACK 重发不改事件身份，部分确认不清除事件。资料同步不覆盖 OPS 余额，审计日志不再次生成同一资金事件。
- 模型请求与任务保存接受时的资金归属及 OPS 账本绑定，避免运行开关变化改变已接受请求的结算/退款归属。Midjourney 退款同事务保存 OPS 退款事件、任务退款标志、API Key 预算及统计投影；不恢复用户本地金额。重复任务轮询不重复退款，删除的 Key 不重建，Key 缓存失效后从已提交记录恢复。
- 管理员本地 add/subtract/override 在 controller 与 model 两层阻止旁路写入；邀请奖励转余额和余额购买订阅也暂时拒绝本地资金写入。这些拒绝是未完成适配的开发保护，不代表相应 OPS 业务已接完，不能带着这些功能缺口切换线上钱包。
- 预支付容量检查已改用 OPS 用户余额，负余额允许充值；没有可用 OPS 数据时不读取聚合旧余额。该检查不是资金预占，不能代替 OPS 入账时的并发上限保护。
- 资金事件保留支付凭据、币种和实付金额，实付金额不从充值额度反推。提交前和发送前共同验证币种、非负金额、最多六位小数、金额上限及 CNY 一致性；重试保留原始业务事实，不使用新的汇率重估，人工补单仍是赠送资金。OPS 当前只消费契约中的 moneyCny，扩展凭据不代表 OPS 已实现完整资金来源/佣金账。
- 注册事务保存不可由客户端提交/资料更新覆盖的 registration_funding 快照，冻结注册/被邀请赠额、邀请人奖励、资金归属、OPS 账本绑定及审计换算。账户、OAuth 绑定、赠额事件和原邀请奖励同事务提交；提交后回调只读取已提交事实记审计，不因运行开关/赠额配置/用户名变化重发钱。旧账户不补发赠额，Agency 邀请注册保持独立零赠额路径；原本本地管理的 durable 用户仍通过现有非实付资金 lot/ledger 入账，未改变其期初语义。邀请人奖励仍为本地 AffQuota，不能据此声称已迁至 OPS。
- 注册审计重复写入分离日志库时，以固定业务 ID 和冻结负载镜像到 OPS；同批重复合并，负载冲突保留待核查。只对具有服务端注册赠额标记的审计去重，不把同前缀的普通模型请求当成赠额。
- 资金提交唤醒：支付/人工补单、兑换、签到、注册赠额和 Midjourney 退款成功提交后发有界合并提示；独立 funding worker 不再依附用量扫描的失败退避。单 worker 复用 HTTP 客户端、8 秒有界发送、成功突发合并 250ms，保留 3 秒补漏、租约、共享 ingest 预算和 Retry-After；发送失败不确认事件。提示不携带金额、不直接记账，不能视为 OPS 已完成入账。
- 充值订单与到账证据分开：订单快照保存原资金 scope/事件 ID（沿用既有 JSON，不新增列/索引），用户及管理员列表按当前页分批走事件唯一索引，只读取事件 ID、scope 和接收时间，不逐笔访问 OPS、不读取完整事件负载。成功订单附可选 `wallet_credit`：queued / accepted / unconfirmed；HTTP ACK 只显示“到账请求已提交，余额待确认”，不推断 posted。旧记录/本地钱包不按当前开关猜归属，旧 OPS 记录缺少引用则待核实。该页面仍无法确认单笔实际到账，必须等待 OPS 提供逐笔记账凭据，不能以余额变大推断某张订单到账。
- 充值邮件统一表达“订单已处理，请核对最新余额”，人工补单也不再声称收到了真实支付；人工处理确认框不承诺同步到账。界面复用 BillingHistoryDialog / StatusBadge / Label，无新增替代组件；长订单号允许截断，处理状态不被挤压。新文案七语言已补齐。
- 页面余额优先显示 OPS 状态，不把缺失金额显示成免费/零余额；累计消费、历史金额、奖励和授信等仍有本地来源，尚未满足“所有金额展示由 OPS 提供”。没有完成本轮浏览器验收。

### 尚未完成、阻止产品钱包启用的条件

1. **现有 OPS 交接协议不足以直接承接不停服蓝绿切换。** 当前 wallet-custody 从收到请求时立即生效，期初用 OPS 已收到的余额快照；缺少用户快照会被跳过，没有聚合资金流水水位。费用入账主要按 occurredAt/cutoff 判断，未使用已接受请求的 owner。因此切换后才完成的旧聚合请求可能重复扣款，切换前接受的 OPS 异步任务可能漏扣。隔离诊断已复现，不是已修复项。需要兼容多写者的准备阶段、逐用户一致期初/流水水位、迟到旧账增量去重及可回滚投影。不得用钱包冻结解决。
2. **全部资金入口尚未迁完。** 同区域 Agency Center 共主库且可独立写资金，必须一并兼容；邀请奖励转入、余额购买订阅、资金有效期、代理商授信/预占/冲正、支付原路退款和资金来源/佣金分摊尚未闭合。兑换码的有效期涉及历史资金批次到期语义，不能直接删掉草稿的有效期保护。
3. **并发余额检查不是原子预占。** 目前 CheckQuota 只是近期 OPS 余额检查，仍可能多个请求同时通过；冷读存在有界网络等待及共享读取预算，不能宣称所有调用只读内存、任意并发均不超支或 CPU 不升高。OPS 需要配套原子预占/结算或明确的可控信用额度协议。读取默认 60 次/分钟不能当作逐调用高并发钱包接口使用。
4. **回滚和历史归属仍需完成。** 产品继续使用原生任务表/游标 1，项目使用隔离任务表/游标 >=2；项目关闭接管后不能直接切回产品表来“补发”。草稿拒绝保留项目凭证却扫描产品任务/日志的配置。仍需 scoped 回滚 worker、既有未绑定任务兼容/核对、崩溃间隙验证及历史无绑定日志对账。
5. **准备和容量验证仍有缺口。** 资金事件表只在聚合主库准备；索引门禁见 operations/INDEX_CHANGE_AND_RELEASE_POLICY.md。Redis 关闭时的多实例预算、最低支持 MySQL/PostgreSQL 版本、实际容量/时延、端到端资金切换与兼容回滚未验收。尚无本次生产 schema ready 回执或可发布镜像。

`pkg/opsauthority.Validate()` 当前明确拒绝启用 product 钱包接管，原因是在线交接未实现和验证。不得删除这条保护来通过发布。OPS 必要配套改动的范围已向用户询问，尚未收到答复；继续 Gateway 独立工作，不假称 OPS 已修改，也不改同事工作树。

### 已实际执行的隔离检查

- 充值到账证据：`TestOPSTopUpWalletCreditsAcrossDialects` 真实 SQLite / MySQL 8.4.6 / PostgreSQL 16.16 全通过、无 SKIP（0.66s），覆盖支付与人工赠送、ACK 不是 posted、缺少/历史引用、运行开关变化、取消查询及 JSON 不泄露内部引用；日志 `D:/DevCache/product-ops-wallet-credit-status-matrix-20261125.log`。首次用例缺少 users fixture，修正测试前置后通过，没有为此放宽生产校验。四组既有独立三库矩阵再次全通过、无 SKIP，日志 `D:/DevCache/product-ops-wallet-credit-status-regression-20261125.log`。没有新增 DB 列/索引，最低支持数据库版本仍未验收。
- 本轮 `go test ./model ./controller ./service ./service/opswallet ./service/opspush -count=1` 通过（66.492s / 48.990s / 10.203s / 0.091s / 0.614s）。前端钱包 4 文件 19 项通过，最终提示/布局用例再次 4 项通过，typecheck 与所改 TS/TSX 文件 oxlint 通过；生产前端构建通过。本地模拟订单在浏览器窄视窗核对了状态提示与长单号，无线上支付或真实用户数据。i18n 使用 add-missing-keys 脚本并运行 sync，排除了 sync 对历史无关翻译的补全/排序变化。Windows Application Control 阻止 oxfmt 原生模块，未修改系统策略；相关文件已人工整理格式、lint 无错误。
- 最终状态引用落库/重载断言通过后，再跑新增三库矩阵全通过（0.58s，无 SKIP），日志 `D:/DevCache/product-ops-wallet-credit-status-final-20261125.log`。`go build ./...` 通过；最终前端 4 项、typecheck/lint 通过，前端构建 6.22s，日志 `D:/DevCache/product-ops-wallet-credit-status-web-build-20261125.log`。真实 OPS handler/billing/PostgreSQL 资金回归通过（11.22s），日志 `D:/DevCache/product-ops-wallet-credit-status-real-ops-20261125.log`，仅临时 overlay、无 OPS 源码修改，临时数据库清理成功。以上均不是在线交接或容量验收，产品启用保护继续保留，本轮不部署。
- 后续资金归属补强：五种支付渠道及人工补单将事务决定的 owner 持久化到既有换算快照，兑换/签到也将本次事务 owner 传到审计与缓存；提交后不再按当时开关猜测归属。OPS 已接收的充值在开关关闭后不会再增加本地缓存；本地已提交充值在开关打开后仍同步本地缓存，历史未知归属只失效缓存。重复人工补单不再生成 user=0 的日志；Creem 人工补单快照保留原人工额度换算，不改变既有补单金额。
- 资金 worker 的最终受影响包测试通过：`go test ./model ./controller ./service/opspush ./service/opswallet ./pkg/opsnotify -count=1`（67.584s / 50.164s / 0.614s / 0.097s；opsnotify 无独立测试文件），`go build ./...` 通过。使用 Go synctest 虚拟时间验证即时唤醒、10 秒 Retry-After 不被新提示缩短、原事件/批次重放、丢提示补漏、多实例租约及接手；真实业务矩阵验证事务回滚不唤醒、退款只唤醒一次。四组独立 SQLite / MySQL 8.4.6 / PostgreSQL 16.16 矩阵全通过、无 SKIP，日志 `D:/DevCache/product-ops-wallet-funding-wake-matrix-20261125.log`；仅清理本轮独立测试库。该测试是功能和调度验证，不是容量/不停服切换或真实到账时延验收。
- 最后将资金租约查询纳入同一次 8 秒 context，避免只限制 HTTP 而让租约 SQL 超时失控；取消后不获得/续期租约，正常请求及多实例租约仍有效。`go test ./service/opspush -count=1` 通过（0.586s）；真实 SQLite / MySQL 8.4.6 / PostgreSQL 16.16 × v1/v1.1 六项完整上报矩阵通过、无 SKIP（1.33s），日志 `D:/DevCache/product-ops-wallet-push-matrix-runner-20261125.log`，运行器同名 py。真实 OPS handler/billing/PostgreSQL 资金回归再次通过（12.22s），日志 `D:/DevCache/product-ops-wallet-funding-wake-real-ops-20261125.log`；没有修改 OPS 源码，临时库已清理。
- 上述最终代码的 `go test ./model ./controller ./service/opspush ./service/opswallet -count=1` 通过（69.091s / 51.384s / 0.692s / 0.113s），`go build ./...` 通过。四组独立真实 SQLite / MySQL 8.4.6 / PostgreSQL 16.16 矩阵全部通过、无 SKIP：BusinessOperations、MidjourneyOwnershipSchema、WalletFunding、RegistrationFunding；日志 `D:/DevCache/product-ops-wallet-credit-owner-final-retry-20261125.log`，运行器沿用 independent-matrix。首次最终矩阵在 BusinessOperations 通过后因 WSL 清理超时中止，未重启共享 WSL，恢复后检查并清理本任务无活动连接的遗留测试库，再完整重跑通过。最低支持数据库版本和线上交接仍未验收。`git diff --check` 与索引政策检查通过；本批没有新增数据库列/索引，也没有生产变更。

- Go 1.26.6，WSL OpsDev：`GOWORK=off GOMAXPROCS=2 GOMODCACHE=/mnt/d/DevCache/gomod CGO_ENABLED=0 go test ./controller ./service ./service/opsbilling ./service/opspush ./service/opswallet ./middleware ./router ./relay -count=1` 已通过；后续变更需重跑受影响包。Go 主应用草稿构建已通过，未作为已提交发布产物。
- 真实 SQLite、MySQL 8.4.6、PostgreSQL 16.16 的 BusinessOperations 与 MidjourneyOwnershipSchema 矩阵通过：充值回调重复/冲突、兑换重复/事务回滚、签到重复、本地余额不变、本地调额旁路、任务退款重复与 Key 缓存读取，以及新建/既有表升级/连续两次迁移和原索引保留。运行器 `D:/DevCache/product-ops-wallet-refund-runner-20261125.py`；脱敏日志为同目录 refund-runner/refund-matrix-20261125.log。仅创建并清理本次独立测试库。后续修改相关代码必须重跑。
- 最新支付凭据变更后，四组测试分别使用独立的真实数据库通过（共 12 个引擎子项，无 SKIP）：BusinessOperations、MidjourneyOwnershipSchema、WalletFunding、RegistrationFunding。覆盖异常现金/币种/精度/凭据拒绝、有效事实不被覆盖、原支付凭据透传、权限及奖励事务回滚。运行器 `D:/DevCache/product-ops-wallet-independent-matrix-20261125.py`，结果 `product-ops-wallet-independent-runner-20261125.log`；主应用重新构建通过。首次合跑因外部测试库共用导致其他组遗留事件进入总数断言，未修改生产实现来规避，改为逐组独立库后通过。
- 后续修正测试自身的数据库前置假定（首轮准备可能已经由其他组完成；产品 scope 可存在其他用户事件），保留逐笔隔离与索引回执断言；四组在同一独立外部测试库合跑也通过，无 SKIP。运行器 `D:/DevCache/product-ops-wallet-combined-matrix-20261125.py`，日志 `product-ops-wallet-combined-money-runner-20261125.log`。最终受影响九包测试及 `go build ./...` 通过，真实 OPS 合约重新通过（12.11s）。没有把这些功能测试作为容量或不停服切换验收。
- Gateway 签名资金事件到真实 OPS handler/billing/posting/PostgreSQL：`TestLocalRealDomesticFundingContract` 通过，使用临时 overlay，无 OPS 源码修改。期初 10、实付 73、赠送 7.3，丢 ACK 重试不重复入账；set10/add2/subtract1 得11，真实OPS消费1再退款1恢复11，退款丢ACK重试仍11，聚合本地额度始终123。日志 `D:/DevCache/product-ops-wallet-refund-real-ops-20261125.log`。这不代表在线交接已通过。
- `TestDomesticCutoverContractEvidence` 仅用于证明当前 OPS 的缺失期初/owner 与时间裁定缺口，诊断测试通过不等于修复通过；临时测试源在 `D:/DevCache/product-ops-wallet-cutover-evidence-20261125_test.go`。
- 先前前端 3 文件 16 项、typecheck/build 通过；本轮未新增 UI 修改，未浏览器验收。索引源码政策检查、git diff --check 通过；没有进行线上资金测试、生产数据修改或停服操作。
- 注册快照补强后的最终树：独立四组和合跑四组的真实 SQLite / MySQL 8.4.6 / PostgreSQL 16.16 矩阵均通过，无 SKIP；覆盖旧用户表加列并重复迁移、密码哈希/重复邮箱、OAuth 全事务回滚、提交后配置改变、重复回调、MySQL/PG 同邀请人并发，以及 durable 用户被邀请赠额仍保留唯一非实付 lot/流水。SQLite 并发注册沿用单写者执行，未把顺序用例当成并发验收。日志在 D:/DevCache/product-ops-wallet-registration-reviewed-20261125.log 与 product-ops-wallet-registration-reviewed-combined-20261125.log。
- 最终受影响包 `go test ./model ./controller ./service/opspush -count=1` 通过（73.200s / 55.597s / 0.645s），`go build ./...` 通过；注册审计批内去重、冲突拒绝及普通模型同前缀隔离均通过。认证参考 OWASP Authentication / Session Management Cheat Sheets；本轮保留现有密码哈希、邮箱唯一及 OAuth 原子绑定，不记录密码/令牌，未宣称完整 ASVS 认证。仍未达到在线资金交接与容量验收，禁止产品启用/发布。

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


## 2026-11-17 产品使用日志接入 OPS 计算费用（待部署）

产品分支 codex/seedance-cn-adapter 增加可选费用读取：后台 GET /api/v1/catalog/v1/fees?since=cursor，沿用项目分支同名 ops_fee 模型、游标/租约/请求节流及 OPS 签名客户端。仅使用日志费用列展示 OPS opsAmount（人民币）；请求ID、用户及模型必须匹配。未知费用显示等待/待确认，真实零金额显示零；后续退款/费用更正由增量更新处理。管理员及本人费用接口只读已授权的当前请求，100条/16KiB/2秒预算，页面独立静默刷新费用，不重新检索列表。

默认不开启；部署时为产品实例设置 OPS_FEES_ENABLED=true、OPS_FEES_URL=配对的OPS产品地址，以及该租户的费用读取凭证。配置示例见 .env.example。共享数据库实例使用相同URL/KeyID/namespace时共用游标、租约与请求间隔；不同OPS/租户必须使用不同连接配置。产品不绑定项目钱包回执；不启用余额/价格/扣款切换，不改变现有本地钱包、消费日志quota、统计和对外调用费用口径。OPS服务无业务代码修改；不导入/重算历史资金。

通用生产文件与项目分支保持一致；项目专用scope和内存回执绑定放在main启动处。后台默认活跃5秒、空闲300秒，最小HTTP间隔3秒；日志待计算2秒静默刷新，最终/异常30秒，隐藏标签页暂停。此阶段是费用展示接入，不是约1秒实时回执的完成承诺。异步视频终态沿用既有上报路径。

验证：使用日志25文件378项通过，最终费用相关4文件20项通过，typecheck、改动文件oxlint、前端生产构建通过。Go主应用构建、service/relay/opspush/router及日志格式/业务费用权限回归通过；费用读取及controller race通过。真实SQLite3.50.4、MySQL8.4.6、PostgreSQL16.10矩阵验证两次迁移、已有日志保留、租约/游标/去重、更正回滚、主日志分库和用户隔离；6个专用临时库已删除。证据和复现脚本 D:/DevCache/gateway-product-fees-20261117。全仓库lint有既有无关错误；go build ./...扫入既有data/release目录触发历史工具依赖下载失败，因此主应用使用go build -o <scratch>/gateway-check .完成。

本轮不部署国内产品、/test、联通或OPS，不重启本地8765；线上未开启产品费用开关，未执行真实付费调用。


## 2026-11-17 费用回执加速第二阶段（仅聚合，待部署）

本节更新前一阶段的行为：先在产品 codex/seedance-cn-adapter 实现，再合入项目 codex/project-integration-uac-20261029。共用费用缓存、推送回执解码、跨实例通知、调用归属缓存、业务回执选择和页面刷新代码；项目实际扣款读者以启动绑定独立接入。OPS 无业务修改，不更改产品本地钱包、项目 OPS 钱包和持久扫描补漏。没有部署、改线上配置、调额或真实付费调用。

- 成功消费日志提交后立即登记 pending 和调用归属，并进入有界微批；普通同步调用低流量合批25ms、每批最多250条、一个发送worker。网络时间计入批次间隔。既有Redis可用时，两个实例共用每800ms一次快通道发送预算；无Redis采用每实例1500ms节奏。此预算不替代OPS真实限流/容量；队列满、失败、超限包的剩余事件仍由原持久扫描恢复，不推进快速通道游标。异步任务沿用原终态流程。
- 绑定同一OPS/租户凭证的上报回复，只有charged/station_billed且logFee请求ID、用户、模型、金额和时间均通过校验时才进入通用费用内存。产品显示opsAmount，不将运营计算金额误报成产品钱包扣款；项目外部API继续返回真实扣款回执，日志显示规范logFee。最终增量费用页成功提交后更新并广播缓存，更正和退款优先于晚到旧回执。
- 内存费用缓存最多16000条，授权调用视图最多10000条；普通费用/冷pending缓存30秒，已知新提交pending一分钟。热费用刷新和热明细读不做SQL/Redis/OPS查询。原Key鉴权仍逐次执行；冷请求、淘汰、重启、跨实例首次归属确认保留一次SQL回源，不能宣称整个调用过程无IO。未知费用仍pending/null，绝不当0；旧记录恢复由持久增量补偿负责。
- 使用已有Redis异步Pub/Sub共享规范费用及允许字段的项目回执。队列64、发布200ms超时，两个常驻协程；不把Redis读写放进热费用HTTP读取。发送实例跳过自己的通知；未广播密钥、日志内容、完整extra或余额快照。Pub/Sub丢失/断连可由原路径恢复，不能据此承诺每次1秒。
- 启用OPS_FEES后，include=billing与请求费用详情默认使用配对OPS费用；产品显式billing_source=gateway仍可读原本地结算。不开启费用功能时保留原默认。默认billing_wait_ms为800，显式0～1000；模型完成后才等待通知，SSE正文继续输出，尾块/DONE在结果或超时后结束。每进程最多1024个等待者，不建每请求轮询协程、不轮询SQL。满载/超时返回pending，并建议1秒后查同一个request_id。
- 页面只静默刷新当前费用：待计算前20次500ms，随后10秒；完成/错误30秒，后台标签页暂停，固定检索时间和列表不变。500ms刷新不代表费用端到端500ms。

验证：两线opsfees/opspush、项目opsbilling/businessbilling、业务JSON/SSE/原Key权限/费用更正回归、service和relay/...与router通过；两线Go主应用构建通过。产品500ms界面用例、两线typecheck/改动文件oxlint/前端构建通过，项目使用日志26文件387项通过。SQLite3.50.4/MySQL8.4.6/PostgreSQL16.10实库矩阵覆盖费用页、快速推送、主日志分库授权、两次迁移与更正；产品8个、项目12个临时库均已删除。race通过费用通知/缓存和业务接口，原Key并发热明细SQL查询次数0。费用内存函数本机Linux/AMD9955HX基准1/4CPU约95.89/117.6ns每次，160B/1分配；仅函数基准，非端到端容量或CPU承诺。脚本和脱敏日志D:/DevCache/gateway-product-fees-20261117。

上线前必须核实配对OPS的上报响应是否实际返回logFee（OPS产品现有开关可能不返回），并核实推送Key与费用读取Key的同租户绑定；缺少logFee只能等原增量接口，不能只改聚合保证1秒。延迟从最终用量确定后开始计算，不包含模型生成时间。OPS计算/网络/凭证限流、积压和缓存未命中均可能超过1秒；未做在用服务器容量压测，不能保证任意高并发CPU不升高。

用户约定的后续国内产品发布：先查现网权重，将一个健康旧实例切100%、另一实例0%，等待正在处理的请求/长连接排空；只更新0%实例。新实例/API/前端静态资源及Caddy主动探测均通过后，渐进转流至新实例100%；排空另一个旧实例并更新，确认后恢复50/50。每步观察502/503及模型调用，出现异常即停止推进并回退健康实例；不承诺绝对无故障。不能同时停两个实例，不能Docker healthy后立即假定Caddy已恢复。按参考会话与技能发布手册执行静态资源归档验证、灰度和优雅退出。项目/test允许直接更新。本轮不执行任何发布。

## 2026-11-17 国内产品费用代码蓝绿发布完成

## 51. 2026-11-17 国内产品聚合费用读取及回执代码蓝绿发布

- 用户授权参考AAB-梳理四大平台服务部署后部署国内产品聚合。实际发布codex/seedance-cn-adapter已推送提交a7bba74c5cea13e33963d5f27fd9325d239a63a4，镜像new-api-seedance:a7bba74c5。只从git archive固定提交构建，不包含现场工作树插件或其他会话未提交改动。北京时间23:03:01—23:13:08切流，最终Blue/Green均healthy、Caddy准入，两个产品域名入口均50/50；公网50次版本样本全部a7bba74c5。
- 产品Gateway改动来自314558cba/9e69f9abe/ce0f08d09/a7bba74c5等已提交代码：model/ops_fee*.go、service/opsfees/、service/opspush/、controller/log_ops_fees.go、业务费用回执路径和web/src/features/usage-logs/。开通产品配对OPS费用读取、内存费用和有界跨实例结果共享、已落库用量快速上报、只刷新费用、默认费用来源OPS及显式gateway兼容。产品本地钱包仍负责预占和扣款，没有切换OPS资金归属；价格、余额、代理商报价和模型配置不作发布调整。
- 只增加两产品槽的OPS_FEES配置：enabled=true、URL=https://ops.nexus-reach.com、现有gateway-production上报凭证、独立namespace=product-fees-20261117、活跃5秒/空闲300秒、首次since=1794841381（发布前约24小时）。不复制密钥到文档。两槽通过数据库租约限制后台增量读取；快回执通过现有Redis有界通知，热HTTP费用读取不等OPS。约73万历史记录未作全量回扫，24小时前的历史费用不能承诺补齐。
- OPS本轮无源码、迁移、配置、推送或部署变更。当前OPS产品运行c5bcdaaa系列镜像尚未包含main的c1875993（station_billed也返回logFee），因此费用增量接口可用，但本次不能宣布产品已实现约1秒即时回执。Gateway已具备接受合法回执能力，需同事部署匹配OPS产品补丁后再验收端到端时延。项目/test运行版本f207446e0、本地8765、联通及海外均未由本轮替换；43个其他运行容器ID和StartedAt保持。
- 数据库：发布前newapi PostgreSQL一致性备份3531461102字节，pg_restore --list通过，SHA256=79a8dacf7ee8c08aa73fb3c3e74d201e47623b6f1a6308758a1368bcbe8fdce8，服务器/opt/new-api/deploy/backups/newapi-before-product-a7bba74c5-20261117.dump。费用独立表ops_fees/ops_fee_syncs按现有可重复加法迁移启用；未改消费日志/用户资金。备份与旧镜像a6faf41a2、旧静态资源全部保留。
- 蓝绿：旧Blue100%→Green0%，排空至少120秒并确认0连接→更新Green→Docker健康和两轮Caddy探测→251项静态资源公网MIME/SHA256与镜像绑定验证→1/5/25%灰度；25%阶段30次小样本未见候选，脚本按门禁暂停，健康Green接管100%，复核30次均新版本后继续→Blue0%排空至少120秒/0连接→更新Blue→健康/资源验证→1/99→50/50。首次代理定位断言发现两产品域名代理、实际并非单一路由，未修改流量或容器就中止；随后按两处共同权重校正。没有绕过真实502/503门禁。
- 发布窗口累计1076次四平台公网探测全部200；Caddy新增HTTP502=0、HTTP503=0、no upstreams=0、在用上游拒绝连接=0。替换零流量实例期间主动健康探测拒绝连接4条单独记录；未将其声称为零日志。两槽无panic/fatal或费用读取错误，5个配套二进制SHA256逐一保持。主机268段CPU平均18.96%、最高73.0%，包括其他工作负载，不能解读为本功能独占或容量保证；当前Gateway采样低于单核1%。
- 只读验收：费用游标追上当前，36761条中36760条available、1条unconfirmed，last_error为空、failures=0；最近三笔与OPS fees接口金额精确相同。23:11的两槽直接授权费用API各3笔及公网200/3笔匹配；后续重复验证遭原有429，尊重限流未调整配置，等待后公网再次200/3笔精确匹配37.08ms。匿名首次401、后续遇限流429，不绕过鉴权。未新建付费调用、充值或客户资金调整，未重新完成真人登录、代理商资金全链路、浏览器视觉或持续容量压测。
- 本机验证：固定提交前端typecheck、费用相关3文件6项及生产构建、Linux主应用构建、opsfees/opspush/router全包通过；沿用此前SQLite3.50.4/MySQL8.4.6/PostgreSQL16.10费用/用量/授权分库矩阵和race记录（第48节与D:/DevCache/gateway-product-fees-20261117），没有把单元测试模拟503当成线上错误。新构建准确dist仅构建一次，251项静态归档publish/verify两槽与公网通过。
- 脱敏证据和复现脚本：D:/DevCache/gateway-product-release-20261117-a7bba74c5/server-final.json、server-postcheck.json、server-api-final.json及构建/测试日志；服务器/tmp/gateway-product-a7bba74c5-20261117。私有环境快照仅留服务器0700/0600目录，不下载、不输出。三份台账同步；文档提交不重启任何业务服务。
