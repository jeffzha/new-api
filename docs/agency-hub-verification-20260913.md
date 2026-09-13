# Agency Hub 验证记录与剩余验收项

日期：2026-09-13（Asia/Shanghai）。依据：[代理商旁路服务完整设计与实施规格](../pkg/doc/05-agency-hub-sidecar-design.md)。对象：本地当前工作区，包含未提交修改；HEAD 为 `8c55b04c1188`，不能仅用该提交号代表受测代码。

## 核心上线能力补齐（19:00 后）

本批优先完成资金命令和通用异步任务的实际调用链。没有修改前端页面，也没有连接生产或执行真实付款。稳定清单仍有 44 项，其中 42 项未关闭；以下能力通过不自动关闭尚含生产验收或独立协议工作的条目。

- **命令事务**：网关锁定持久命令并使用其当前签名正文，事务内再次核验 Root 用户和源会话；资金冲正、provisioning 受控变更/审计与最终命令回执共享事务。回执写入失败会撤销业务变更。取消操作检查当前 job fencing version；旧 worker 重复执行已完成命令不改变结果。提交后按实际变更用户失效钱包缓存。
- **支付退款幂等**：原付款操作、用户、额度、币种、付款参考、证据和原因均参与重试比对；币种省略使用原冻结币种，文本只做约定的空白规范化。同退款 ID 改凭证或金额返回冲突；同时给出的原事件/操作引用必须一致。用户→资金账户→充值来源采用一致锁顺序；重复成功返回原受影响收费列表，不再重复冲佣金。本项未实现组件支付拒付与模型退款交叉规则。
- **内部 mTLS**：新增独立网关监听和 Hub 客户端。要求 TLS 1.3、可信 CA、精确客户端 URI/DNS SAN，监听只接受私有或 loopback 字面 IP。公开 Hub Router 无内部命令路由。接受新命令时在同一事务内复核 Root/源会话及唯一 proof；远端不可用不会回退 Hub 写队列。修复 Hub 将第一次临时 503 永久缓存而阻止相同幂等请求重试的问题。
- **通道到业务的联动**：真实无数据库句柄的 Hub 客户端经 mTLS 提交命令，网关 worker 执行后再通过 mTLS 查询永久结果。取消实际 provisioning job、恢复用户准入、一次审计和重复提交无副作用均有通过断言；普通传输成功没有冒充业务执行成功。
- **异步任务首次终态**：向上游提交前冻结计价模式、销售/结算政策、倍率和事件版本选择；通用视频/Suno 轮询将首次成功/确认失败、钱包/Token、journal/outbox 和 Task 同事务提交。delta=0、免费任务和已删除 Token 可完成相应终态；未知网络/超时保留预扣并标记待对账。供应商账单支持初始财务结算及已 finalized 修正，重复回执复用原事实。独立 Midjourney 全生命周期和历史未知任务的人工证据终态仍未完成。
- **部署材料**：新增 [内部命令部署指南](../deploy/agency-hub/COMMAND-TRANSPORT.md)、[覆盖文件](../deploy/agency-hub/compose.commands.override.yml)、[变量示例](../deploy/agency-hub/commands.env.example)。实际 `docker compose config --format json` 合并验证保留原 backend/edge、环境和网关/PostgreSQL 数据卷，只增加内部网络；没有发布 3443 宿主端口。证据 `E:/new-api-test-cache/agency-command-compose-merged.json` 为无凭证测试配置，不是生产配置。独立复核确认旧蓝绿发布脚本不会自动加载新增覆盖文件，指南已明确补充在非活动槽位切流量前使用完整 `-f` 链重建单服务及检查版本/健康；后续升级也必须保留该步骤，未声称旧脚本已自动支持。

最终源代码整体验证（全部实现交回后复跑，20:05 完成；之后仅更新 Markdown）：

| 验证 | 实际结果 | 证据 |
| --- | --- | --- |
| 全仓 Go 测试 | 49 个含测试包，1,470 个顶层测试通过，0 失败，5 个顶层跳过 | `E:/new-api-test-cache/runs/20260913-195806-8f6bdad2/go-tests.log` |
| 财务审计、全仓构建、vet | 4 项检查退出码均为 0（含上行 Go 测试） | 同目录 `checks.json` |
| 三库资金、任务专项 | model 106 个测试节点通过，0 失败/跳过，包含最终冻结计价、首次终态和完整退款证据校验 | `E:/new-api-test-cache/external-db/20260913-200218-c42b4115/cross-database-tests.log` |
| 三库命令事务 | 34 个测试节点通过，0 失败/跳过，覆盖回执失败、Root 禁用/降权、会话撤销/版本变化、job 版本冲突、冲突额度别名和原付款引用；外库夹具隔离在外层事务中，worker 使用真实保存点 | 同目录 `command-atomic-tests.log` |
| 三库 Hub 与组件核对 | 88 个测试节点通过，包含原 finalize 和实际 `reverse` operation 的证据校验 | 同目录 `export-concurrency-tests.log` |
| PostgreSQL 备份恢复 | 建数/异库恢复后回执各 1 项通过；dump 209,764 bytes，恢复后重复付款回调不重复入账 | 同目录 `primary-postgres.dump`、`postgres-restored-receipts.log` |
| Linux amd64 编译 | `CGO_ENABLED=0`，最终网关和 Hub 均编译成功；只是二进制构建，未构建运行最终镜像 | `E:/new-api-test-cache/agency-core-linux/new-api`、`agency-hub` |

测试节点计数包含父子用例，不能与顶层测试数相加。上述外库均为 E 盘新建隔离实例，测试结束已停止其进程并释放 13316/15436，数据与日志保留。MySQL 使用 5.7.43，PostgreSQL 使用 16.4；未因此证明最低版本或生产最小 GRANT。全仓 5 个顶层跳过中的 Agency 外库迁移、PostgreSQL 备份恢复已在此专项真实执行；另两项既有 Token 字段迁移测试和 Windows 符号链接权限测试仍未通过本批验收。前端沿用 18:00 批次的已验证代码和产物，本批没有重跑或修改页面。

最终产物 SHA256（均为本地测试产物，无生产备份或发布含义）：

| 产物 | 字节 | SHA256 |
| --- | ---: | --- |
| `agency-core-linux/new-api` | 142,249,640 | `f70c3b012e31c5eecc81adae55fdccfad136d7e23fc4c792faa8c88f030c5687` |
| `agency-core-linux/agency-hub` | 51,630,520 | `bacf850c81a9a4f0b5627101e71c92a274dec89234d1a6d71b305e1965d784e8` |
| 最终外库 `primary-postgres.dump` | 209,764 | `4971882d354c04982d5f4ccc4c68bddb3cac81ecd14be864ba4124692f0a7108` |

首批通过证据保留：`runs/20260913-194554-b879b742`（1,465 个顶层测试）、`external-db/20260913-194505-09782072`（106/28/88 个测试节点）。最终结果以上表为准，不把不同轮次测试数累加。

本批剩余边界：组件支付拒付与模型退款的交叉累计规则、独立 MJ 调用链、长 provisioning 最终提交鉴权及执行归属、最小数据库权限、历史同截面对账、生产 SSO/证书网络部署、最终镜像运行及容量/恢复仍需完成。生产 v2/开户/佣金处理/提现开关没有由本批开发自动开启。

## 前一批实现与复核（18:00 后，历史证据）

本轮继续围绕网关资金、异步退款、客户价格和对账展开；与较早记录相比，关闭 W01.02、W06.02 两项。稳定清单仍为 44 项，目前 42 项未关闭；条目数不表示剩余 42 个同等大小的开发任务。当前依然是 **No-Go**，组件支付拒付、部分异步首次结算、历史日结及生产部署边界没有被本轮通过结果替代。

- **退款来源与债务归属**：新增 allocation 级债务身份；模型退款先取消原未偿还债务，再恢复原债务的实际 paid/nonpaid 还款来源，返回资金先抵扣其他未清债务。历史聚合债务只有归属唯一可证明时才采用，歧义拒绝且事务回滚。
- **通用任务与供应商账单**：对已 finalized 的 v2 费用，较低最终账单进入原始累计退款事务，钱包、Token、来源矩阵、组件佣金、journal/outbox、Task 和对账回执同一事务提交。相同账单重放不增加退款/佣金；变更证据、更高最终费用、原结算缺失或冲突会拒绝。保留真实 worker 的用量统计，不把诊断日志当作第二次资金事件。Midjourney 和 submitted/reserved 首次财务终态仍未完成。
- **PostgreSQL 任务持久化**：真实外库复测发现 `TaskPrivateData.Value` 把 JSON 作为 `[]byte` 返回，在生产同样采用的简单协议下被编码为 `\\x...`，触发 SQLSTATE 22P02。`model/task.go` 中任务 properties/private_data 现以 JSON 文本写入，读取兼容字符串、字节和 NULL；重新读取时不会保留上一份计费上下文。新增真实三库保存/更新/清空回归，并验证私有计费身份不出现在公开 JSON 中。没有更改数据库驱动模式或放宽数据库类型来绕过失败。
- **组件对账**：扫描原始 finalize operation、journal、组件不可变原结果、资金矩阵及 allocation/lot 身份；检查累计退款与来源恢复、独立佣金 quota/micros 和全组件缺失，求和使用大整数防止损坏数据溢出后误判一致。纯消费者夹具没有实际资金矩阵，完整账务一致性断言已迁到真实网关充值/结算/累计退款集成测试，保留消费者投递、回执与投影断言。
- **历史日结的真实边界**：当前手动及普通定时对账使用 `cutoff=0` 并标记 `current_state_per_page`。历史 cutoff/daily 请求记录明确失败；不再把当前余额扫描报作历史对账成功。日调度可在 03:00 后重启时补发尝试，但完整历史同截面重建仍未实现。
- **客户有效价格**：主站 `/api/pricing` 与卡片、表格、详情按客户销售倍率显示，替代普通分组倍率；政策本地读取，按访问者隔离缓存，错误时隐藏过期价格，不暴露结算成本或佣金。实际组件 8 项、计价函数 4 项、身份切换/刷新失败 2 项测试以及类型、lint 已通过。React/UI/i18n 规范用于本次真实组件、错误状态、按需加载及七语言文案。

本批最终代码的验证结果如下。PostgreSQL 修复后重新执行全部 Go 测试、财务审计、构建及 vet；前端文件没有再改动，复用同一工作区已通过的前端验证并额外完成主站生产构建。

| 检查 | 实际结果 | 证据 |
| --- | --- | --- |
| 全仓 Go 测试 | 49 个含测试包、1,439 个顶层测试通过，0 失败；5 个顶层测试跳过，未计为通过 | `E:/new-api-test-cache/runs/20260913-185443-222566e9/go-tests.log` |
| 财务审计、全仓构建、Hub/入口 vet | 均通过；本批 `checks.json` 的 4 项退出码均为 0 | 同目录 `checks.json` |
| Agency 前端 | 类型检查、33 项测试（3,187 个断言）和 Vite 生产构建通过 | `E:/new-api-test-cache/runs/20260913-184416-bf8d5eaa` |
| 客户价格前端 | 实际组件 8 项、纯计价 4 项、身份缓存/失败恢复 2 项通过；类型与改动文件 lint 通过 | `E:/new-api-test-cache/customer-pricing-status.md`、`customer-pricing-display-results.json` |
| 主站生产构建 | 使用本机 Node 20.20.2 和 Bun 执行 Rsbuild，退出码 0；产物在 E 盘，不覆盖工作区发布资源 | `E:/new-api-test-cache/runs/20260913-184416-bf8d5eaa/gateway-web-build.log` |
| SQLite / MySQL 5.7.43 / PostgreSQL 16.4 专项 | model 77、Hub 88 个测试节点通过（包含父子用例），0 失败、0 跳过；包含最终 Task JSON 修复及真实组件对账链 | `E:/new-api-test-cache/external-db/20260913-185411-bff311ef` |
| PostgreSQL 整库备份与异库恢复 | 建数与恢复后回执各 1 项通过；dump 209,748 bytes，恢复后重复付款回调不重复入账 | 同目录 `primary-postgres.dump`、`postgres-restored-receipts.log` |

普通全仓运行中的外库 Agency 子用例由上述隔离专项覆盖。两项既有 Token 字段迁移测试、其他未纳入专项的外库条件及 Windows 符号链接权限仍未验收；不能把顶层 PASS 等同于每个条件分支都执行。没有执行真实上游付费生成、银行付款、生产 SSO 或容量演练。外库脚本退出码 0，两个测试数据库进程已停止，端口 13316/15436 已释放，日志和数据保留。

复现最终后端和三库验证（仓库根目录 PowerShell）：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\deploy\agency-hub\verify.ps1 -TestRoot E:\new-api-test-cache -Full -SkipFrontend
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\deploy\agency-hub\verify-external-databases.ps1 -TestRoot E:\new-api-test-cache -MySqlBin "D:\FileRuntimeEnvironment\MySQL5.7.43\mysql-5.7.43-winx64\bin" -PostgresBin "E:\new-api-test-cache\database-tools\pgsql\bin"
```

`-SkipFrontend` 只用于本次最后一轮后端 JSON 修复后的复测；前端结果来自上述已通过批次。首次完整检查应使用不带该参数的 `-Full`。主站 `web/` 构建要求 Node 20.19+ 或 22.12+；本机默认 Node 16 无法启动 Rsbuild，因此仅为构建进程调整 PATH 使用已安装 Node 20，没有更改系统 Node 配置。

失败现场保留：`external-db/20260913-184516-cc42664e`、`external-db/20260913-184829-dde29444` 均在 PostgreSQL Task JSON 写入处失败；第二次确认补齐正常上游结果并不能解决问题，随后定位并修复上述私有字段的驱动编码。没有跳过 PostgreSQL 用例。

本轮没有连接或修改生产数据库、部署镜像、切换流量或执行真实支付；所有测试数据库、构建产物、缓存和临时文件在 E 盘。

## 历史批次：继续开发（15:xx）

以下是当时发现的问题与状态，其中 allocation 身份和其他债务抵扣已由 18:00 后批次完成，不再作为当前待实现项。

在上一轮完整验证基线之后，本地又补充了跨无关欠费的来源感知退款处理：paid/nonpaid 还款来源和 `BonusDebtRepaid` 被持久化，部分/全额退款、重试、错误 Token 回滚及已偿还债务的真实 lot 恢复用例通过；同一笔支付退款的服务重放也已改为不重复创建佣金冲正。新增 focused model/service 测试通过：

```text
go test ./model -run 'TestAgencyRefundDebtOffsets|TestAgencyBonusTopup|TestAgencyComponentRefund' -count=1
go test ./service -run TestFundingReversalReplayDoesNotDuplicateCommissionCompensation -count=1
```

这些改动**没有关闭**组件支付拒付的验收项。独立复核确认：当同一充值来源支撑多个 charge 时，现有旧拒付债务仍按 source topup 聚合，不能证明某个 allocation 的债务/还款归属；需要 allocation 级债务身份或对歧义历史数据 fail-closed。该 P0 阻断项及其回归测试保留在 `E:/new-api-test-cache/debt-offset-review-status.md`，等待 allocation 归属实现后再做三库复测。剩余工作细化清单为 [44 条验收项](agency-hub-remaining-work.md)；本轮没有生产连接、迁移、发布或真实支付操作。

## 结论

**尚未完成设计文档全部目标，不能按完整业务上线验收通过。** 当前已具备与 new-api 网关的部分计费、资金、任务和身份联动，并有自动化测试通过；仍存在前端操作链、资金组件、对账和部署边界缺口。

自动化测试通过只证明被执行用例的断言。MySQL 5.7 / PostgreSQL 16 的迁移、资金/修复并发及对账扫描专项已经实测通过，另完成隔离 PostgreSQL 的整库备份、异库恢复及回调幂等验证；完整三库业务矩阵、容量和生产恢复演练仍未验收。真实浏览器已扩展到六个流程，不能扩大解释成全部前端需求已验收。2026-09-12 的逐项审计仍可用于追溯；其中已修复的缺陷以本记录更新，不能把旧失败全部视为仍然存在。

## 历史批次：收费分项、累计退款和消费投影（14:xx）

本批对应设计 §2.4、§9.3。新增 `agency-billing-v2`，历史 v1 消息继续兼容。新版把多个收费分项保存在同一个不可变事件内，每个事件仍只有一份操作凭据、Outbox、投递租约和永久回执。

- 实际网关 `AgencyCommitWalletCharge` 已接入组件资金分配：paid 按费用最大余数分配，nonpaid 按剩余费用分配，余量为 debt；稳定 ID 排序决定同余数顺序，具体 lot 按 FIFO 填入矩阵。原模型定价引擎的收费和成本结果不在旁路重算。
- 新增 `agency_hub_charge_components` 和 `agency_hub_component_funding`。保留旧 allocation ID，单独持久化不可变收费结果、allocation/lot 分配矩阵及累计退款水位，避免破坏原拒付引用。
- `AgencyRefundWalletCharge` / `service.RefundAgencyModelCharge` 接收稳定退款 ID 和累计目标，可指定分项，也可按整笔累计退款条件比例分摊。钱包、Token、原资金来源、累计冲佣金、journal、资金账本和事件同事务提交；重复命令重放原结果，冲突及来源异常全部回滚。
- 新模型欠费保存精确原 allocation 对应的债务记录。退款先取消其未偿还部分；已经被新充值偿还的部分恢复实际还款 lot，不恢复失效旧本金、不虚构赠額。预扣取消也复用这一来源恢复规则。
- 消费者逐项记录用量和正/负佣金，核验完整不可变提交结果，并按原事件+精确分项定位冲正。原结算可以在退款已提交后才被投递；依然先处理原始事件。佣金 quota 与金额 micros 独立累计，金额差额舍入到 0 时仍保存非零 quota 冲正。
- 修复两个报表计数问题：预扣和充值事件不再进入模型用量；同一事件的多个组件只算一次请求。金额仍按所有分项求和。
- MySQL 大小写不敏感排序规则不能合并 `Model` 与 `model`：用量/佣金的唯一键和原始分项查找改用 SHA256 精确标识，保留原 ID 核对。迁移仅回填标识，不重算历史金额。readyz 检查新列及唯一索引，并公布支持的 billing schema。
- 钱包结算 API 明确限定 segment 0；Realtime 的独立分段流程仍为 v1。从已接受快照恢复模型名、系数和归属/币种，避免恢复调用传入的元数据覆盖原事实。

**发布边界：** `AGENCY_COMPONENT_BILLING_ENABLED` 是网关环境变量，默认 false。显式内部组件输入使用 v2；现有普通输入保持 v1，只有经评审启用开关才自动生成组件。这不是本批完成后立即开启生产的指令。组件拒付佣金归属、退款抵扣其他未清债务、旧异步退款调用方完整切换仍有缺口；不支持的组件资金操作在事务提交前明确拒绝。不能把默认关闭误认为这些业务已完成。

新增的真实联动测试直接调用网关充值、预扣、结算与退款事务，再由 Hub 消费真实 Outbox，最后读取真实报表 API；没有手工伪造提交结果来代替这条链路。覆盖大小写不同的两项模型费、独立费用、先退费用、同一模型多次退款、延迟消费、全额资金恢复和仅一次请求计数。SQLite/MySQL/PostgreSQL 使用同一断言。

本批最终验证目录：`E:/new-api-test-cache/runs/20260913-144548-0525b528`。7 个检查步骤退出码均为 0：

| 验证 | 本批结果 |
| --- | --- |
| `go test -json -count=1 ./...` | 49 个含测试的包、1,409 个顶层测试通过，0 失败；5 个顶层测试跳过，未计入通过 |
| `agency_audit` 财务合同测试 | 通过 |
| `go build ./...`、目标包 `go vet` | 通过 |
| Agency 前端类型、测试、构建 | 类型与构建通过；33 个测试通过，3,187 个断言 |
| SQLite、MySQL 5.7.43、PostgreSQL 16.4 专项 | model 31 个测试节点、Hub 84 个测试节点通过（包含子用例），均无跳过和失败 |
| 隔离 PostgreSQL 整库备份和异库恢复 | seed/恢复后回执各 1 项通过，dump 204,055 bytes |

三库最终证据：`E:/new-api-test-cache/external-db/20260913-144523-5b66fea9`。在同一套断言中实际验证了分项分配、大小写精确 ID、迁移保留历史、累计退款，以及真实网关→消费者→报表链路。两套隔离数据库进程已停止，端口 13316/15436 已释放；保留测试数据和日志，没有连接生产。

首次三库运行 `20260913-144120-7e5ee15a` 的 PostgreSQL 联动测试暴露 Token `key` 字段引用依赖全局方言缓存。改用 GORM 字段条件自动引用后执行上述完整复测通过；没有跳过 PostgreSQL 或放宽断言。整链路测试还实际发现并修复预扣重复计次；初次失败记录保留在 E 盘。

Linux amd64 最新本地编译产物：`E:/new-api-test-cache/agency-hub-components-linux-amd64`，51,434,404 bytes，SHA256 `9f133657343c9a3c88da3a078f812d73d5496d02a1267fbe0257d734c682b354`。这是可执行文件构建验证，未构建并运行最终 Docker 镜像、未发布到服务器。之前 reconciliation 产物保留，但不代表本批新代码。

上一批的浏览器截图仍用于既有页面，本批未改页面布局，也未重复执行浏览器截图验收。普通测试的 5 个顶层跳过、外部环境需求和剩余业务缺口不能用通过数量替代。

## 上一批：对账证据、受控修复与扫描一致性

本批把 Root 的异常列表补成了可实际操作的复核流程：按状态和游标分页，查看权威来源、预期值/实际值、证据指纹、历史处理与审计，并在同页查看对账执行记录。七语言文案完整；对账 ID、金额证据和时间使用精确十进制字符串。手动执行对账的二次验证绑定为 `reconciliation.run` / `reconciliation:run`，实际浏览器返回 201 并保留执行历史。

### 已实现的约束

- 核验七类已支持对象：资金账户、佣金余额、提现冻结、资金 lot、当前用户归属、财务操作、outbox/投递回执。详情使用只读一致快照；真实差异、缺少权威来源或未知格式不能直接关闭/忽略。
- 处理请求绑定当前 Root 来源会话、具体异常、完整正文、最新证据指纹和幂等键。证明消费、恢复投递、关闭、证据保存、审计及幂等结果在同一事务中提交；审计失败全部回滚，并发重放不会重复修复。
- 自动修复仅从内容与身份均通过核验的不可变 outbox 恢复缺失 delivery。有匹配的终态永久回执则恢复为 done，否则为 pending。现有 poison/未知/矛盾投递不被覆盖，不补造事件、佣金、钱包余额或银行结果。
- 旧版 resolved/ignored 如果没有核验证据，仍计入阻止付款的未复核异常。Root 可重新核验，新证据保留原状态及说明。只有当前证据一致时才能关闭或有理由忽略。
- 已提交操作与 outbox 必须匹配事件 ID、规范化 payload 哈希、存储哈希及关键元数据，不能只靠事件数量相同通过。当前写入器是单事件结果；新 v2 在同一事件内包含多个分项，未知的多事件结果封装格式仍明确为 unsupported。
- done 投递必须存在匹配的终态永久回执；投递状态变化使旧证据失效。pending/retry/claimed 是合法队列状态，pending 搭配匹配的终态回执也允许消费者幂等完成。
- 自动扫描复用同一套证据规则，每页最多 200 个来源对象，按主键继续并固定本次来源上界；每页一致读取完成后才写入异常。可发现“总额抵消但资金桶为负”、错误事件身份、缺少回执等问题。佣金已提现后冲正产生的合法负数可用余额继续保留。

新扫描方式避免在提现/钱包并发提交之间混读出不存在的差异，但各页读取的是当前快照，**不代表完整历史同截面日结**。大量单操作明细/提现记录仍需进一步限制和容量验证；journal、allocation、debt、token、usage 和累计退款的完整核对仍未完成。

### 数据库升级

新增 `agency_hub_reconciliation_issues.active_key`、`resolution_evidence`、`repair_event_id`。迁移先添加普通可空列并为 open 回填 SHA-256 key，再创建 `uidx_agency_reconcile_active` 唯一索引，最后移除旧 `uidx_agency_reconcile_open`。closed 的 key 为 NULL，允许同一对象保留多次已关闭历史，旧证据与说明不被重写。

首轮 SQLite 旧表升级真实复现了 `ALTER TABLE ADD ... UNIQUE` 不支持，已改为普通列与独立唯一索引。三库旧 schema 升级、重复执行、历史保留和并发唯一性均已实测。缺少新列或索引时 `/agency/readyz` 返回 503，不能只检查表是否存在。

部署前备份共享数据库，停止全部旧版 Hub 副本、独立 worker 和对账任务，用新二进制执行 `agency-hub migrate`，再启动新版本。旧写入器不会填写 active_key，不能和回填同时运行。升级是在现有数据库上增量变更，不会重建 PostgreSQL 实例或清空原数据库。操作说明见 [部署说明](../deploy/agency-hub/README.md)。

### 最新验证证据（13:43 后）

完整回归目录：`E:\new-api-test-cache\runs\20260913-134321-12e24b68`；七个步骤退出码均为 0，包含最后的扫描器与事件证据修复。

| 检查 | 结果与实际范围 |
| --- | --- |
| 根模块 Go 全量测试 | 49 个测试包、1368 个顶层测试通过，0 失败；5 个顶层测试跳过，子用例不重复计数 |
| `agency_audit`、Go 构建与 Hub vet | 全部通过；包含 SQLite WAL 中正常钱包并发提交不产生误报 |
| 前端类型、契约/翻译、Vite | 全部通过；33 项测试，0 失败，3187 次断言 |
| 真实浏览器 | 6/6 通过，19.5 秒；React + Gin + SQLite，目录 `E:\new-api-test-cache\agency-browser\20260913-134321-45203f0e` |
| 三库迁移、并发和证据扫描 | SQLite / MySQL 5.7.43 / PostgreSQL 16.4 通过；包括负数桶检测、合法佣金债务、事件/回执对应关系、205 条数据跨页扫描 |
| PostgreSQL 整库 dump/异库 restore | 实际执行通过，恢复后付款回执、已付状态与充值重放幂等断言通过；dump 为 196024 字节 |
| Linux amd64 编译 | 成功；`E:\new-api-test-cache\agency-hub-reconciliation-linux-amd64`，51402472 字节；未声称容器运行通过 |

最终外库证据：`E:\new-api-test-cache\external-db\20260913-134449-0bb95c31`。统计包含父测试与子测试：model 15 PASS（18.953 秒），Hub 76 PASS（26.197 秒），备份前建数和恢复后回执各 1 PASS；所选专项 0 FAIL、0 SKIP。常规测试中的外库 Agency 子用例由该独立专项实际执行；两项既有 Token 字段迁移测试、既有 UserSession 外库子用例及当前 Windows 无权限创建符号链接的用例不在这一通过范围中。

本批前一轮外库执行发现测试数据的空 AffCode 与长 SID 不兼容，已修为唯一短值后复测通过；最后复跑又暴露 PostgreSQL 端口开放早于可接收 SQL 的启动窗口，验证脚本改为等待 `pg_isready` 成功。失败日志分别保留于 `20260913-132434-4b9878b8` 和 `20260913-134315-aa39f54d`。没有通过跳过断言或放宽业务合同消除失败。

嵌入首页现引用 `index-a9396e3a.js` 与 `index-0caa85d7.css`，历史暂存资源保留。Linux 文件 SHA-256：`24c7acd655e6344b69a4ec52f770ad815241d1c0229975373329955300a31ce8`。桌面证据、处理结果与 390px 手机弹窗都有截图；已打开最新手机截图复查，表格在弹窗内部横向滚动。Root 网关登录/签名边界仍是测试夹具，生产 SSO 和网关刷新不属于此浏览器验收。

本批所有临时数据库、编译缓存、浏览器截图和日志在 E 盘。临时数据库与浏览器服务已停止，数据保留。没有执行生产迁移、部署、切流、真实付款或上游付费请求。旧的“只改状态关闭异常”和“没有对账修复页面”缺口已由上述实现替代。

## 上一批复核证据（12:36 后，历史）

最终完整回归目录：`E:\new-api-test-cache\runs\20260913-123646-512e4e8d`。包含最后的首次充值用户→资金锁顺序修复，`checks.json` 的七步退出码均为 0。

| 检查 | 结果与范围 |
| --- | --- |
| 根模块 Go 全量测试 | 49 个测试包通过，1346 个顶层测试通过，0 失败，5 个顶层测试跳过；子用例不重复计数 |
| `agency_audit` 财务契约 | 通过 |
| Go 全模块构建、Hub vet | 通过 |
| 前端类型、契约/翻译及 Vite 构建 | 通过；29 个测试、0 失败 |
| 实际浏览器 | 5 个流程通过，14.6 秒；目录 `E:\new-api-test-cache\agency-browser\20260913-122527-57ba40cc` |
| MySQL 5.7.43 / PostgreSQL 16.4 | 迁移重复执行、资金并发、首次充值并发、同操作者多 Session 下载并发限流通过 |
| PostgreSQL 整库备份/异库恢复 | 实际 `pg_dump` / `pg_restore` 成功，恢复后原回调不重复入账、已知证据冲突拒绝、已付款状态和凭证保留 |
| Linux amd64 编译 | 成功；`E:\new-api-test-cache\agency-hub-linux-amd64`，50,367,802 字节 |
| Compose 配置及补丁空白检查 | 非生产占位配置验证通过；staged/unstaged diff 检查通过 |

最终外库及备份证据：`E:\new-api-test-cache\external-db\20260913-123619-6ca4ecf2`。该专项没有跳过，model 三库测试 12.220 秒、导出限流 3.750 秒、备份前建数测试 4.059 秒、恢复后断言 0.628 秒；`primary-postgres.dump` 为 188,811 字节，完整保留测试主库，不只是 Agency 表。恢复到另建的 `agency_hub_restore` 数据库；原库及测试数据均保留，没有覆盖生产数据。两个测试数据库及浏览器服务已停止，测试端口 13316、15436、4328 均无监听。

全量测试中跳过的外库 Agency 迁移、充值并发、下载并发和备份用例已在上述独立专项实际执行通过，不能只凭常规退出码判定。两项既有 Token 字段迁移测试及既有 UserSession 外库子用例未在本次专项运行。Windows 账号不具备创建符号链接权限，因此实际符号链接文件系统用例跳过；普通文件、目录、未知/较新文件、活跃租约保护均已测试。

本次构建已更新嵌入首页，引用 `index-b328bd5a.js` 和 `index-8cbc1c7c.css`；历史暂存资源保留。最新浏览器复测覆盖开通门禁和独立导出 worker，随后只增加/修复支付回调、充值锁顺序及相关后端测试，没有再修改浏览器交互。已打开最新提现中文截图检查排版；导出中文截图此前也已检查。

隔离恢复测试只验证所列数据与幂等合同；模拟银行凭证/加密字段不是实际密钥恢复或银行清算验证。Docker 引擎仍未启动，没有最终容器运行验收，也未在生产迁移、打包发布或切流。

## 2026-09-13 第二批补齐

本批补上三条此前缺失的操作链：

- **导出**：操作员/代管范围的任务列表、创建、轮询、下载、明确失败原因；后端分批输出，最大一百万行，租约认领和续租使用独立 owner/attempt，过期旧 worker 无权发布或覆盖接管者文件。下载重新验证操作者、机构、权限版本、Session 和文件 SHA-256。根入口、七语言字典及实际嵌入资源均已接入。
- **客户归属**：Root 查询用户当前模式/归属、检查目标代理商、绑定旧用户、查看异步任务阻塞原因、取消和转移。ID/revision 使用精确十进制字符串；目标政策及资金账户均需有效；转移不搬移历史消费事实或改动钱包。修复了旧用户正余额缺少可用非付费 lot、开通负余额缺少可偿还债务明细，以及第二次取消被旧唯一索引拒绝的问题。
- **异常付款**：`payment_unknown` 及有付款尝试的 `on_hold` 需录入银行确认未支付的结构化证据才能恢复审批；恢复不移动冻结资金，旧租约失效，审计与状态变更同事务。付款未确认时不能直接取消释放款项。

同时修复新开通门禁：`AGENCY_ONBOARDING_ENABLED` 默认/非法值均关闭，必须明确为 true；网关邀请注册、Hub 绑定入口、后台绑定提交一致检查。暂停时任务保留，可由 Root 取消；不会把已有 durable 用户降回 legacy。部署必须在网关和 Hub 两端显式配置，环境变更后重启/重建进程。独立 Compose 限制数据库池和 CPU/内存，导出使用独立后台循环，不再阻塞佣金处理，也不会在进程入口重复启动另一套导出调度。

数据库升级新增 `agency_hub_export_jobs.error_code`、`agency_hub_topup_facts.quota_conversion_snapshot`，并把 provisioning 的 `(user_id,status)` 从唯一索引改成普通索引，以保留多次失败/取消历史。网关 `topups` 还新增私有 `payment_snapshot` / `quota_conversion_snapshot` TEXT 列，由网关核心表升级流程负责；只执行 Hub migrate 不会创建这两个核心列。新版本启动前必须完成对应增量迁移；缺少导出/充值事实新列会使 Hub readiness 失败。取消绑定的网关命令补上了操作者 ID 解析及审计，相关编译回归已修复。

### 支付快照及追加资金修复

Epay、Stripe、Creem 已从真实验签回调中记录实付金额、币种和上游支付参考号；Epay 的 `money` 为 CNY 主单位，Stripe/Creem 从整数最小货币单位转换，包含零位/三位币种及 Stripe 的特例。不同供应商的额度计算基数也随实际入账保存，之后修改全局换算配置不会重写历史。Waffo 和 Pancake 已接入回调及已知上游参考号，但金额/结算币种/税额口径尚无契约证明，因此真实付款金额继续明确留空，不用订单价格或额度替代。

已知付款证据冲突会拒绝重复入账；旧成功订单没有快照时仍幂等确认原结果，不补造历史。显式零支付金额的额度归为非付费赠送；人工补单不转成付费资金。还修复了“缺少资金账户的受管用户首次充值”重复记期初与新充值的问题：先锁用户并初始化原期初账户，再增加钱包和新资金 lot，保持与网关扣费相同的用户→资金锁顺序。

新增模型测试覆盖五个供应商、受管/非受管用户、重复/冲突回调、免费订单和债务偿还；Stripe、Creem、Epay 的 HTTP 测试实际执行合成签名验证、解析、事务和余额断言，没有真实付款或外部 API 请求。

导出下载追加了按操作者跨 Session/代管机构串行的事务限流，重新检查下载证据并同事务写审计；不再存在两个并发请求同时通过最后一次额度的窗口。崩溃文件回收只处理能被已过期任务证明的精确 attempt 文件名，保留未知文件、活跃租约、较新文件和符号链接。

这批第一轮浏览器证据：`E:\new-api-test-cache\agency-browser\20260913-115244-41da048d`，5 个流程通过（14.7 秒），前端契约/翻译测试 29 项通过。CSV 文件检查包含 BOM、预期同机构内容和其他机构内容排除；客户流程断言取消后原余额 42 保持、转移归属/revision 更新。中文导出截图已打开检查。后续新开通门禁、充值快照和导出清理变化后的最终回归以本记录后文最新证据为准。

### 三种数据库的实际专项结果

证据目录：`E:\new-api-test-cache\external-db\20260913-120321-53b4e661`。

| 专项 | SQLite | MySQL 5.7.43 | PostgreSQL 16.4 |
| --- | --- | --- | --- |
| `TestAgencyFundingConcurrentReserveIsConservedAcrossDialects` | 通过（SQLite 分支顺序执行） | 通过，真实行锁并发 | 通过，真实行锁并发 |
| `TestMigrateAgencyExternalDatabaseCompatibility` | 不属于该测试，常规迁移用例覆盖 | 通过 | 通过 |

本次所选外库专项没有跳过，model 包运行 8.482 秒。两个数据库使用新建的 E 盘数据目录，只监听 `127.0.0.1:13316/15436`；测试后两个端口均停止监听。没有使用或停止本机原有 MySQL 服务。工具使用本机既有 MySQL 5.7，以及下载至 E 盘的 PostgreSQL 16.4 Windows 二进制；PostgreSQL 压缩包 SHA-256 为 `3508D8F085BC3980F38211A82E3F31E5FCAE9952105D3DC2F8BE67B64A822BAA`。

复现脚本：[verify-external-databases.ps1](../deploy/agency-hub/verify-external-databases.ps1)。首次临时 MySQL 配置的主机匹配导致失败，记录保留于 `20260913-120227-19e9197a`；移除不适用选项后重新创建独立实例通过。该结果不等于全部 Hub API 已在外库验收，也不覆盖最低 PostgreSQL 9.6、备份恢复或负载指标。

## 2026-09-13 后续实现与浏览器验收

原 `agency-web/src/main.tsx` 的单文件页面已拆成角色入口、共享请求/确认组件、代理商管理、价格、财务和查询模块。实际入口已接入新模块，并重新构建 `pkg/agencyhub/webdist`。React/UI/i18n 项目规范用于模块拆分、异步状态、表单可访问性和七语言文案；agency-web 保留独立 Vite 与原生组件架构，没有引入主站整套组件依赖。

本次实现：

- 代理商列表、创建、编辑、启停、重置密码、邀请链接、代管范围及临时密码交付确认。
- Root 按代理商选择价格，操作员只提交销售字段；默认与精确模型例外、历史复制、差异预览、版本冲突保留草稿。
- 收款账户列表、创建、版本替换、停用；查看明文需 Root 二次验证，页面隐藏或一分钟后清除显示。
- 提现按真实账户和币种选择金额；金额使用十进制字符串/BigInt；审核、批准、暂挂、付款开始、结果未知及记录原笔付款结果均发送真实 API 请求。
- 客户及用量/充值详情、佣金明细、角色各自的审计字段、同步能力与对账异常查询；查询采用游标分页，范围切换取消旧请求。
- 首次登录强制改密、SSO 超时/取消恢复；Root 桥接改为通过网关刷新 cookie 获取短期 Bearer，只在网关页面内存使用，操作证明通过来源和窗口双重校验的 postMessage 传递。

修复的真实 API 缺陷：

1. 幂等中间件返回缓存或 processing 响应后未终止 Gin handler 链，可能重复执行或拼接两个 JSON。终止分支现已中止 handler 链，集成测试验证不会重复移动资金。
2. 收款账户明文曾进入幂等响应缓存。查看接口现在不读/写重放缓存，每次必须提供新证明且独立审计；旧版缓存不能绕过验证。本地代码修复不等于生产历史明文记录已清理。
3. `payment_lease_token` 为 UnixNano，超过 JavaScript 安全整数范围。输出/输入现在使用精确十进制字符串。Root 列表只向仍持有有效租约的同一操作者返回令牌，允许页面刷新后继续记录原笔付款。
4. 销售价格发布缺少强制二次验证；Root 提现证明缺少逐操作/逐对象绑定。现均加入校验及失败不变更数据的测试。
5. 创建代理商会把显式零价差覆盖为默认值。现在仅字段缺失使用默认值，合法 `0` 得到保留。
6. 价格试算将标准额度伪作付费分配额，销售折扣小于 1 时计算报错被忽略，返回零；名为 `preview` 的模型例外还会污染默认试算。现在按默认系数及各模型分别调用计费契约，返回理论佣金与额度单位，前端直接展示服务端计算结果。

新增测试主要在 [finance_api_security_test.go](../pkg/agencyhub/finance_api_security_test.go)、[browser_contract_test.go](../pkg/agencyhub/browser_contract_test.go) 与 [浏览器用例](../agency-web/e2e/agency-workflows.spec.ts)。

完整回归目录 `E:\new-api-test-cache\runs\20260913-111337-ab0ae73e`：7 个步骤退出码均为 0。根 Go 模块 49 个有测试的包通过、1308 个顶层测试通过、3 个顶层测试跳过；`agency_audit`、Go build/vet、TypeScript、23 个前端契约/翻译测试和 Vite 构建通过。之后追加的价格试算修复已单独重跑全部 Agency Hub 测试、前端测试、嵌入构建及浏览器测试，不把旧全量日志冒称为包含后加用例。

第一次前端翻译扫描发现六个通用字段缺少字典，该轮 `20260913-110533-bc43bb78` 的 frontend-contracts 真实失败；七语文案已补齐，失败记录保留。

第一批浏览器使用本机 Microsoft Edge，接真实 Gin/SQLite，不是仅拦截接口返回固定 JSON。当时三个流程覆盖：

| 流程 | 浏览器和数据库断言 |
| --- | --- |
| Root 创建 → 试算 → 密码确认交付 → 操作员首次登录 | 试算 10000 标准额度得 9000 客户费用、7500 结算、1500 理论佣金；交付后密文销毁；未改密业务 API 403，改密后 200，Root API 仍 403；Root 审计能显示创建记录 |
| 操作员发布销售价格 → 两个会话冲突 | 仅销售 DTO、结算保持 7500；旧 revision 返回 409，用户草稿和原因保留 |
| 收款账户 → 12.34 CNY 提现 → 审核 → 付款开始 → 刷新 → 记录模拟付款 | 创建请求 amount_micros 为字符串 12340000；大整数租约精确保留；最终可用 987660000、冻结 0、已付 12340000 微单位，没有真实转账 |

Root 登录/网关证明签发在此浏览器环境中由测试夹具提供，Hub 的会话校验、CSRF、证明消费、定价、加密、幂等和资金记账均为真实实现。因此不宣称生产网关 SSO、跨域 cookie、mTLS 或外部数据库已通过。

第一批浏览器证据目录：`E:\new-api-test-cache\agency-browser\20260913-112543-341ad2de`。三个流程全部通过；`results.json`、测试服务日志及 `results` 中两张中文截图可复核。已人工打开截图检查管理列表及提现页排版。

### 构建与运行边界

根 Dockerfile、Workbench Dockerfile 增加 agency-web 构建阶段，Go 编译使用同一构建中的静态资源；Workbench 镜像也编译并复制 agency-hub。独立 Compose 改用 [源码构建 Dockerfile](../deploy/agency-hub/Dockerfile)，默认不再从磁盘复制未知版本的二进制。`.dockerignore` 排除本机前端依赖和旧嵌入资源，避免覆盖 Linux 构建结果。

`docker compose config --quiet` 使用非生产占位参数验证通过。当前 Docker Desktop 引擎未启动，且 C 盘剩余约 0.6 GB；本次没有启动引擎或运行镜像构建，**最终容器启动/运行权限与镜像验收仍待完成**。Go/Bun/SQLite/Edge 临时文件、测试数据库与截图均在 E 盘。没有操作生产配置、生产数据库、支付或部署。

第一批 `go test ./pkg/agencyhub -count=1` 在更新嵌入资源后通过（3.337 秒）。当时 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -trimpath` 也成功，旧产物大小为 49,879,055 字节；最新产物及验证见本文开头。这证明 Linux 目标编译成功，不等于已在 Linux 容器中运行。用户此前暂存的旧资源保留；源码 Docker 构建通过忽略本机 webdist，仅嵌入本次构建资源。

## 本轮修复和常规回归

### 页面资源入口

此前 `/agency/` 返回了引用 React 产物的 HTML，但 Router 未注册 `/agency/assets/*`。原资源代码还统一使用 `application/octet-stream`，不满足浏览器对 ES module 的 MIME 要求。

已注册静态资源路由，返回正确的 JavaScript/CSS Content-Type、缓存头和 `nosniff`。缺失资源、目录请求及目录穿越返回 404，不返回首页 HTML。首页的资源地址随配置的 BasePath 调整。

新增回归通过真实 Gin Router 获取首页、解析首页引用的 JS/CSS，再实际请求这些资源，检查状态码、MIME 和内容。覆盖默认及自定义 BasePath。此项不等于 React 内部全部 API 都支持自定义路径，也不等于完整浏览器业务验收。

代码与测试：

- [路由](../pkg/agencyhub/app.go)
- [资源处理](../pkg/agencyhub/static.go)
- [真实资源请求测试](../pkg/agencyhub/static_test.go)

### 将已有修复纳入常规测试

[deployment_regression_test.go](../pkg/agencyhub/deployment_regression_test.go) 验证：

1. TLS 连接缺少已验证客户端证书时，内部命令返回 403，且没有入库。
2. 缺失财务必需字段时，readyz 返回 503。
3. 已过期导出任务在 worker 崩溃、租约失效后变为 expired；清理和处理入口都不会抢占仍有有效租约的任务。

这些是对本地已实现行为的回归，不代表真实 mTLS 部署或长任务的完整租约栅栏验收。

## 测试目录与复现

检查时 C 盘约剩 0.62 GB，E 盘约剩 232 GB。本轮 Go 缓存、下载的模块、编译临时文件、运行时临时文件和前端验证产物使用 E 盘；源码及既有 node_modules 保留在 D 盘。

在仓库根目录运行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\deploy\agency-hub\verify.ps1 -TestRoot E:\new-api-test-cache -Full
```

[verify.ps1](../deploy/agency-hub/verify.ps1) 设置当前进程的 `GOCACHE`、`GOMODCACHE`、`GOTMPDIR`、`TEMP`、`TMP`、`BUN_INSTALL_CACHE_DIR`，退出时恢复环境变量。每次运行在 `E:\new-api-test-cache\runs` 下生成独立目录，保存环境记录、逐项日志及 `checks.json`。

`-Full` 不排除失败包；默认范围为 Agency Hub 和关联的 model/service/controller/relay 包。默认同时执行财务审计、构建、vet、前端类型检查和前端构建。前端依赖需预先安装；`-SkipFrontend` 会明确跳过前端检查。验证构建不覆盖嵌入的发布产物。

没有清理 C 盘已有文件，也没有更改 Docker Desktop 的磁盘镜像位置。Docker 测试所需磁盘位置须单独确认。

## 测试证据

第一轮完整验证目录：`E:\new-api-test-cache\runs\20260913-095048-b12285ad`。

- Agency Hub、model、service、controller、relay 包及上述新回归通过。
- `agency_audit` 财务审计、`go build ./...`、目标包 `go vet`、Vite 构建通过。
- `relay/channel` 的两个 HTTP/2 测试出现 Windows socket reset/abort：`TestUpstreamGetBody_HTTP2RetryAfterGracefulGoAway_PassThrough`、`TestUpstreamGetBody_HTTP2CannotRetryWithoutGetBody`。这一轮全量测试失败，日志保留。
- 验证脚本首次使用的 TypeScript `--build` 与 `--tsBuildInfoFile` 参数冲突，已改成 `tsc --noEmit --project tsconfig.json`，单独执行通过。首次失败不归因为业务 TypeScript 编译错误。

第一批完整复测目录：`E:\new-api-test-cache\runs\20260913-095747-d70b4e28`，脚本退出码 **0**。以上首次失败记录保留；下表为当时结果，最新结果见本文开头。

| 检查 | 最终结果 |
| --- | --- |
| 根 Go 模块 `go test -json -count=1 ./...` | 49 个含测试的包通过，0 失败包；没有排除 `relay/channel` |
| `agency_audit` 标签下的财务契约测试 | 通过 |
| `go build ./...` | 通过 |
| `go vet ./pkg/agencyhub ./cmd/agency-hub` | 通过 |
| 前端 `tsc --noEmit --project tsconfig.json` | 通过 |
| 前端 `vite build` | 通过；产物保存到本次 E 盘运行目录 |
| staged 和 unstaged 的 `git diff --check` | 通过 |

普通 Go 测试日志统计为 1299 个顶层测试通过、3 个顶层测试跳过，子用例不重复计入；另有 62 个无测试的包。这些数量不能解释成 1299 项产品需求全部验收。外部 PostgreSQL/MySQL 等用例的环境门槛仍然存在。

HTTP/2 fixture 的问题已经修复：[api_request_getbody_test.go](../relay/channel/api_request_getbody_test.go) 原来写出 GOAWAY/RST_STREAM 后立即关闭 TCP；未读控制帧导致 Windows 偶发 reset，掩盖要测试的协议行为。现在由 `t.Cleanup` 在断言完成后清理连接，保留重试次数、请求体完整性以及不可重试错误的严格断言，并补充成功响应体读取断言。没有更改生产转发逻辑、跳过用例或通过任意网络错误放宽验收。四个相关用例各重复 10 次通过，随后上述全仓复测通过；重复次数不计为额外业务功能。

`environment.json` 确认运行时临时目录为 `E:\new-api-test-cache\runtime-tmp\`；不仅仅是编译缓存位于 E 盘。`checks.json` 中 6 个检查步骤的退出码均为 0。

## 仍需完成的目标

| 范围 | 当前已确认的缺口 | 依据 |
| --- | --- | --- |
| 多组件计费与退款 | 组件结算、来源矩阵、累计退款、allocation 级债务及退款抵扣其他债务已完成；通用 Task/供应商已 finalized 账单修正已接入。组件支付拒付、MJ 和部分异步首次结算仍未完成，自动 v2 保持关闭 | `model/agency_component_settlement.go`、`model/agency_component_refund.go`、`model/agency_task_refund.go` |
| 充值事实 | 五个回调和不可变快照已接入；Epay/Stripe/Creem 实付口径及验签流程已测试。Waffo/Pancake 实际付款金额/税费/币种配对语义待供应商契约证明；历史未知数据不补造 | `model/topup.go`、`controller/topup_payment_snapshot.go` |
| 对账 | 已新增 journal/组件原结果/矩阵/allocation 身份及累计模型退款核对；历史 cutoff 明确失败，当前扫描明确其范围。完整 debt/repayment/资金账本/Token/usage 链、支付拒付交叉、单对象巨量明细和历史同截面重建仍缺 | `pkg/agencyhub/reconcile.go`、`pkg/agencyhub/reconciliation_components.go`、`pkg/agencyhub/reconciliation_service.go` |
| 对账修复 | Root 证据复核、原子审计/证明与缺失 delivery 恢复已实现；货币差异的完整受控补偿、缺失权威事件重建和未知银行结果仍不支持，不会用关闭状态代替修复 | `pkg/agencyhub/reconciliation_resolution.go` |
| 管理界面 | 管理/价格/财务、异常付款恢复、导出、客户归属和对账复核已实现，六条浏览器流程通过；全部启停、重置、历史复制、异常提现状态的浏览器矩阵尚未覆盖 | `agency-web/src/features/`、`pkg/agencyhub/app.go` |
| 查询和身份前端 | 客户有效价格、身份隔离缓存及卡片/表格/详情已完成；查询与分页、导出、客户归属、对账修复已接真实 API。生产 refresh/SSO、完整 usage 和全部报表筛选仍待验收 | `web/src/features/pricing/`、`agency-web/src/App.tsx`、`controller/agency_sso_page.go` |
| 旧用户迁移排空 | 已验证异步 Task 阻塞、取消、余额/债务开账及旧 worker 栅栏；多实例在途文本流、Realtime 和 Batch 的完整排空证明仍缺失 | `pkg/agencyhub/provisioning.go`、网关请求生命周期 |
| 命令和数据库部署边界 | 证书检查已有，但进程仍启动普通 HTTP，内部命令仍位于 Hub Router；独立网关命令监听、最小数据库权限及运行路径的兼容性尚未验收 | `cmd/agency-hub/main.go`、`pkg/agencyhub/agency_command.go`、部署目录 |
| 发布和归档 | 源码构建链和本地嵌入产物已更新；Docker 引擎未启动，最终镜像构建和容器运行未验收。完整归档上传、回读验证、冷热查询和历史删除资格尚未验收 | Docker 构建链、AgencyArchiveManifest |
| 外部数据库 | 已实测隔离 MySQL 5.7/PostgreSQL 16 的迁移、资金并发、七类证据扫描、修复并发及备份恢复；全部处理器、最低 PostgreSQL 9.6、完整故障恢复矩阵仍待验收 | `model/agency_reconciliation_migration_test.go`、`pkg/agencyhub/reconciliation_scan_test.go`、`pkg/agencyhub/reconciliation_concurrency_test.go` |
| 容量和恢复 | 隔离 PostgreSQL 整库 dump/restore、已付款状态保留及恢复后重复回调已实测；60 RPS 一小时、120 RPS 五分钟、百万 backlog、热点 P99、Redis 故障、真实银行核对、密钥恢复和生产蓝绿回滚仍未完成整体验收 | 设计第 22 节 |

优先顺序：完成真实可操作的前端与网关联调 → 完整财务组件和充值快照 → 对账与受控修复 → 独立测试数据库及部署边界 → 浏览器、容量和恢复验收 → 再评估生产能力开关。

## 数据库和生产状态边界

本地开发可使用 SQLite，但 Hub 必须指向网关同一个绝对路径的 SQLite 文件，并先由网关初始化核心表。生产应指向现有网关 PostgreSQL `newapi` 数据库；`agency-hub migrate` 对 `agency_hub_` 表执行增量迁移，不是重新创建 PostgreSQL 实例。数据库迁移前仍需验证备份和恢复，不能仅凭幂等迁移测试承诺无风险。

前一轮只读核查看到服务器已有约 40 张 Agency 表，Hub 运行旧的 `agency-live-20260911-linux` 镜像，佣金消费者、提现、导出能力关闭。本轮未重新读取生产实时状态，未在生产执行迁移、发布、切流或资金操作；本地验证不能证明本次代码已部署上线。
