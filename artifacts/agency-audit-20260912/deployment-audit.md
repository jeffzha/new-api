# Agency Hub 部署、恢复、容量与数据库验收审查

审查基线：`pkg/doc/05-agency-hub-sidecar-design.md` 第 15–19 节、22.4–22.5 节；本机 Windows 工作区；2026-09-12 23:16–23:24（Asia/Shanghai）。本次为检查与隔离测试，没有修改生产配置、容器、数据库，没有真实付款或创建业务对象。

## 结论

**尚未达到文档上线门槛。服务在生产运行并通过公网路由可达，但佣金消费者、提现、导出均未启用；运行镜像不能证明与当前工作区版本一致。** 本机验收新增 3 个按设计合同编写的回归测试，全部暴露真实缺口：无客户端证书的 TLS 命令被接受、缺关键字段的 schema 仍 ready、崩溃遗留导出任务永久 processing。现有常规测试通过不能覆盖这些缺口。

## 生产只读结果

使用既有 SSH 密钥、`BatchMode=yes`、`StrictHostKeyChecking=yes` 成功查询 47.97.97.89，没有改变主机信任或服务器状态。未读取或输出密钥、完整环境变量、数据库连接串。

| 项目 | 实际结果 | 能证明的范围 |
| --- | --- | --- |
| gateway Blue 容器 | `new-api-seedance-new-api-1`，`new-api-seedance:openaiseedance-1000-20260912`，healthy | 一个 gateway 实例在运行；不能代替全部 agency 能力验收 |
| gateway Green 容器 | `new-api-seedance-new-api-green-1`，`new-api-seedance:v1.0.0-rc.22.gateway.20260910T103411Z.gce11a2c498e6`，healthy | Green 仍是不同版本 |
| agency 容器 | `new-api-seedance-agency-hub-1`，`new-api-seedance:agency-live-20260911-linux`，healthy | Agency 服务已经部署，创建时间 2026-09-11T00:15:52Z |
| 公网 Caddy | `/opt/new-api/deploy/Caddyfile:97` 匹配 `/agency /agency/*`，第 99 行代理 `agency-hub:3201` | 网页路由已连接 |
| Gateway 分流 | Caddy 第 7–10 行 Blue=100、Green=0，保留 cookie affinity | 默认新流量 Blue；仅权重不证明 Green 后台任务已排空 |
| `GET https://gateway.nexus-reach.com/agency/readyz` | HTTP/2 200，`ready:true`，`missing_tables:[]` | 探针仅检查表存在及积压，见下文局限 |
| 生产能力开关 | `commission_worker:false`、`withdrawals:false`、`exports:false` | 不能说佣金处理、提现和导出已完整上线 |
| 生产积压 | pending=4、retry=0、poison=0、claimed=0、exports_in_progress=0、open_reconciliation_issues=0 | 存在 4 条待投递事件；消费者关闭时不会自动消费，零 issue 不等于金额全部正确 |
| 镜像来源 | Blue 及 hub OCI revision 均为 `6b1e4235c9c547abe7ca3f8370db28bdfd5dc5a0`；Green 无 Labels 字段 | 镜像可能继承基础标签；不能由这一个 revision 判定实际二进制内容与本次改动一致 |

生产探针原始字段摘要：

```json
{
  "status": "ok",
  "ready": true,
  "schema": {"missing_tables": [], "ready": true, "schema_version": "agency-hub-v1"},
  "capabilities": {
    "agency_durable_v1": true,
    "pricing_snapshot_v1": true,
    "outbox_v1": true,
    "commission_worker": false,
    "withdrawals": false,
    "exports": false
  },
  "backlog": {
    "deliveries": {"claimed": 0, "pending": 4, "poison": 0, "retry": 0},
    "exports_in_progress": 0,
    "open_reconciliation_issues": 0
  }
}
```

## 要求逐项结果

| 要求 | 状态 | 实现与证据 | 缺口及上线含义 |
| --- | --- | --- | --- |
| §15.1 不可变 policy version L1/L2、TTL、通知 | 部分实现 | `pkg/agencypricing/resolver.go:12` 是按 agency_id 存内存 map；`service/agency_gateway.go:276` 每次读绑定、机构、政策并解析；`model/agency_funding.go:705` 预扣事务验证快照 | 未发现版本键 Redis L2、TTL10分钟、Pub/Sub 接入；实际主路径直接 DB 查询。不能以 resolver 的存在声称达成缓存吞吐目标 |
| §15.1 不依赖 sidecar 同步查询、DB权威核对 | 有实现，需端到端故障验证 | gateway quote 查询主库；预扣校验版本；sidecar消费者扫描 outbox | 30 分钟停机、Redis 故障与在途主库故障综合场景未实际运行；需要独立故障演练 |
| §15.2 实际事件放大 k、日增长、保留预算 | 未验收 | 已有资金/消费微基准；设计明确 k>1 | 当前无本机完整 reserve→finalize→refund 混合事件宽度和存储实测，100 万事件追平与预算审批无证据 |
| §15.3 60RPS一小时、120RPS五分钟、三类热点 | 未验收 | `model/agency_funding_concurrency_test.go:269` reserve 单点微基准；`pkg/agencyhub/consumer_bench_test.go:30` 预置5000事件消费微基准 | 微基准不是持续负载、端到端事务延迟或后台混合写入。没有基线/新增 P99<20ms、事务 P99<50ms 的本轮证据 |
| §15.3 消费批次与补算资源限额 | 部分实现 | `worker.go:71` 每tick最多20×500事件；`RunConsumerOnce` 每条独立处理 | `TestDrainConsumerBudgetSustainsAbove60RPS` 只检查单次排空700事件，不断言真实速率；资源预算未验证 |
| §15.3 sidecar连接池/CPU低于网关保留额度 | 未实现部署配置 | `model/main.go:193` 共用默认 idle100/open1000；compose 未配置 SQL_MAX_*、CPU、内存额度 | 旁路补算仍可挤占共享DB；需专属池限额和负载预算 |
| §16.1 消费重复、低ID晚提交、栅栏、冲正先到 | 已有隔离测试通过 | 本轮重新运行4个consumer相关合同测试；`worker.go:94` 按 delivery 状态重扫 | 证明这些局部机制；不证明停机30分钟或百万积压验收 |
| §16.2 全部对账公式、同一截面、每日02:30 | 未完成 | 新 `reconcile.go` 只有4种扫描；worker按间隔调用 | 主审查另详。每日自然日调度、全套lot/journal/receipt/withdrawal/refund/usage核验未覆盖 |
| §16.2 ignored差异不能从健康消失 | 不符合 | `app.go:267` 健康只 COUNT status=open | ignored 不计入健康中的待核验总数，无剩余财务差异摘要 |
| §16.3 同主库一致性备份、密钥/归档备份、恢复演练 | 未验收 | 部署README仅要求独立迁移账号、功能保持关闭 | 没有可执行全库写入冻结、支付回调恢复协议、密钥恢复、银行对账与恢复顺序手册/演练证据 |
| §17 永久历史及归档上传→回读→manifest→删除 | 仅结构，不可声称已支持归档 | `model/agency_models.go:629` 只有 AgencyArchiveManifest 定义和迁移注册，全仓未发现读写流程 | 设计允许第一期无归档且不删除历史。该例外可接受，但必须明确容量预算尚未通过，禁止开启历史删除 |
| §17 异步CSV、会话绑定、文件hash与防注入 | 已有后端功能与测试 | `report_service.go` job创建/下载/授权；本轮文件篡改与过期清理测试通过 | 不代表前端可操作；生产 `exports:false`，compose无目录配置/持久卷 |
| §17 导出worker崩溃后恢复、24小时临时清理 | 实测失败 | `report_service.go:40` 只领queued；第79行清理只含queued/ready/failed | processing任务无lease/heartbeat，进程在claim后崩溃会永久卡住；本轮失败测试见下 |
| §17 百万行导出与明确行数限制 | 部分实现 | `report_service.go:183` 先COUNT限额，再一次性 `.Find(&rows)`；usage/topup/ledger三分支相同 | 一次读取百万行到内存，共享DB压力/并发时COUNT与读取一致性没有容量验收 |
| §18/19 数据库最小权限、白名单视图 | 不符合设计边界 | 未发现agency专用GRANT/VIEW部署脚本；`pkg/agencyhub/provisioning.go:116,291,311` 直接UPDATE users.billing_mode，worker主动执行provisioning | 旁路需要写用户核心表，无法按文档仅授予核心表只读白名单权限运行；需重构权限边界并用真实受限账号测试 |
| §19 Secret、密钥轮转 | 部分实现 | compose挂5个secret；payout读取旧key版本；加密轮转有现有测试 | README说3个secret，漏 command private/delivery；未提供整套mTLS证书与恢复流程 |
| §19 内部mTLS命令监听在gateway，不经公网 | 不符合 | `pkg/agencyhub/app.go:104` 把internal路由挂在hub；`cmd/agency-hub/main.go:108` 普通HTTP ListenAndServe；`funding_reversal_service.go:214` hub直接插入共享commands表；`service/agency_command_worker.go:22` gateway轮询DB | 实际采用共享DB命令而非设计mTLS服务；不存在配置完整的gateway内部TLS监听。不能仅关闭CommandRequireTLS声称满足要求 |
| §19 mTLS身份校验 | 实测失败 | `agency_command.go:467,669` 只验证Request.TLS非nil；不验证PeerCertificates/VerifiedChains | 拥有合法应用签名与Root proof但没有客户端证书的TLS请求仍202。应用签名仍在，不能误报为任意匿名资金操作；缺的是明确要求的传输身份边界 |
| §19 migrate独立命令、超时 | 部分通过 | `main.go:40` migrate；`model/agency_migration.go:59` context timeout；SQLite幂等/索引测试通过 | 未实现migration版本锁；事务包裹AutoMigrate不等价于MySQL DDL原子性/版本排他 |
| §19 先迁移users模式列、startup仅验证schema | 不符合流程 | `MigrateAgency`只接受agency_hub_*表；`model/user.go:118`有billing_mode/funding_version但migrate不负责建列；gateway旧AutoMigrate仍负责User | 不能按文档先用独立migrate完成核心列升级；应提供受控增量迁移与滚动版本兼容流程 |
| §19 schema/capability health | 实测失败 | `app.go:228`只HasTable；`app.go:245`3个能力固定true | 删掉price_revision列仍ready200。探针没有验证关键列、schema版本或其他gateway实例能力 |
| §19 Blue/Green/后台worker能力门禁和排空 | 未实现/未验收 | gateway发布/切流脚本无agency能力检查；`main.go:148`无条件启动AgencyCommandWorker | 没有检查两个slot/task/recharge worker的三项能力或排空旧worker。仅blue=0/green=100不保证旧worker停工 |
| §19 onboarding/commission/withdrawal独立开关 | 不完整 | 后两项由config.go:56–57读取；README使用错误的AGENCY_HUB_*开关名 | 全仓未找到AGENCY_ONBOARDING_ENABLED读取，invite注册直接走路径；不能按手册只关新开通而保持durable计费 |
| §19 回滚只允许durable兼容版本 | 未验收 | 设计有约束，现有发布脚本仅版本/健康与流量 | 缺能力阻断、后台任务排空和有durable数据时禁止旧二进制回滚的可验证流程 |
| §22.4 SQLite/MySQL/PostgreSQL真实迁移与并发 | 仅SQLite本轮通过 | 本轮mysql/pg明确未配置；migration外库SKIP | 不能汇报三库通过；并发测试对SQLite有意顺序执行，实际行锁并发需要真实MySQL/PG |
| §22.5全部门槛通过才开通 | 不满足 | 上述明确失败/未验收项 | 保留生产佣金/提现关闭状态，不能从health200推导上线批准 |

## 新增回归测试（3项失败，未修改业务实现）

测试源码保存在 `deployment_regression_test.go.txt`，通过 `deployment-overlay.json` 映射为Go测试文件，只影响这次显式测试调用，不进入正常构建。

```powershell
$env:GIN_MODE='release'
go test '-overlay=artifacts/agency-audit-20260912/deployment-overlay.json' ./pkg/agencyhub -run '^TestAuditDeployment' -count=1 -v
```

| 测试 | 期望 | 实际 |
| --- | --- | --- |
| TestAuditDeploymentRejectsTLSWithoutVerifiedClientCertificate | 无verified客户端证书拒绝403 | 202，合法应用签名命令已排队 |
| TestAuditDeploymentReadinessRejectsMissingFinancialColumn | 删除agency.price_revision后503 | 200，schema.ready=true |
| TestAuditDeploymentExpiredCrashedExportDoesNotRemainProcessingForever | 48小时前过期的crash遗留任务可恢复或到期 | 执行worker及清理后仍processing |

本轮现有测试命令与结果：

```powershell
go test ./model -run 'TestMigrateAgencyExternalDatabaseCompatibility|TestMigrateAgencySQLiteIsIdempotentAndIndexesReportingFacts|TestAgencyFundingConcurrentReserveIsConservedAcrossDialects' -count=1 -v

go test ./pkg/agencyhub -run 'TestAgencyHealthAndReadinessProbes|TestAgencyOperationalStatusIncludesBacklog|TestInternalCommandAcceptsAndIdempotentlyReplays|TestConsumerLeaseFencePreventsStaleWorkerFinancialCommit|TestConsumerProcessesLateLowIDDeliveryAfterHigherIDDone|TestConsumerRetriesReversalBeforeOriginalAndLaterSettles|TestDownloadExportRejectsTamperedFile|TestCleanupExpiredExportJobsRemovesFileAndInvalidatesDownloadToken' -count=1 -v
```

- model：SQLite migration PASS；资金守恒SQLite分支PASS；mysql/postgres打印 `dialect ... not configured; skipped`；外库迁移 **SKIP**。
- hub：上述8个已有测试PASS。测试通过仅覆盖对应合同，新增测试说明测试集缺少关键约束。
- 本机Docker daemon不可用，未运行容器构建/本机真实三数据库；未借用生产库跑迁移或压力测试。

## 旧验收报告不能作为本轮已完成证据

`docs/AGENCY_GO_NOGO_AUDIT.md:62` 写“本机可闭合的验收项全部通过且本轮已重跑证据”，第21、57行写三库全PASS；它引用MySQL8.2:13306、PG15:15432和其他环境历史数据。当前Windows实测为两外库未配置，且新增3个本机可测合同明确失败。这些历史声明不能替代本轮结果，Phase A–D全完成、Phase E只剩生产测试的结论需要撤回。

`deploy/agency-hub/verify.sh:18` 外库默认不测；即使开总开关但漏某DSN，外库测试仍可SKIP；最后第27行仍打印verification passed。必须以实际每库执行记录与SKIP明细判定，不能只看退出码0。
