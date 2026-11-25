# 索引变更与发布规则

本规则覆盖聚合产品 codex/seedance-cn-adapter、聚合项目 codex/project-integration-uac-20261029、OPS 产品 main 和 OPS 新项目 codex/project-uac-go-migration-20261114。四条分支的根 AGENTS.md 均要求执行本规则；通过 Git 交付，不依赖某个聊天的记忆。

## 新增索引必须一起交付

1. 在拥有该表的仓库提交结构定义/迁移或专用准备工具；记录稳定名称、数据库归属、引擎、表/字段/表达式/过滤条件、用途及引入版本。
2. 把该索引加入所属服务的必需索引校验清单，并在对应操作记录登记。迁移创建的索引按原迁移版本管理；独立在线索引必须由专用工具管理，不能只有服务器手工 SQL。
3. 在真实支持的数据库中验证初次创建、既有库升级、连续执行、查询结果/金额不变及错误定义处理。聚合保持既有多引擎验证要求；OPS Go 使用 PostgreSQL。
4. 公共索引能力按已有分支同步方向传递；保留各分支的认证、钱包和运行配置差异。不能为了同步索引而合并整条项目分支。

## 每次部署都检查，缺失才创建

1. 确认环境、服务、数据库/引擎、主库/日志库、表 owner 和维护权限，按已有规则处理备份与资源保障。
2. 新数据库先初始化正确的基础表结构。对本版本要求的索引逐一读取数据库实际目录；同名、同定义且有效就绪的直接跳过。历史执行记录不能证明数据库当前仍有索引。
3. 缺少时创建并复核。禁止每次发布重建已有索引，禁止因为切换容器或分支重复 DROP/CREATE 或 REINDEX。同库的多实例发布串行执行或取得维护锁。
4. 冲突、无效、权限不足、超时、执行失败、缺少回执或未就绪时，停止切流/实例替换。保留旧服务，不自动删除索引，也不绕过流程继续发布。
5. 每次保留回执：目标环境和非敏感数据库标识、源码版本、工具/定义哈希、检查时间、新建/跳过列表、有效/就绪/定义匹配状态。不得记录 DSN、密码、密钥。

## 聚合和 OPS 的职责

- 聚合只处理所属主库、日志库及同区域共享代理商库；使用现有 ORM/迁移与数据库引擎兼容机制。OPS 索引不属于聚合数据库。
- OPS Go 当前四项在线索引由 internal/db/feeindexes/ 的 SQL、注册清单和 cmd/fee-indexes 管理。部署前从本次 OPS 版本构建工具，通过 scripts/release/prepare_fee_indexes.py 生成 ready=true 的当次回执；详见 OPS 仓库 docs/operations/FEE_INDEX_RELEASE_GATE.md。
- 聚合与 OPS 一起发布时，先通过 OPS 门禁；仅发布聚合时核对对应 OPS 的依赖及就绪证据。不能将另一个环境执行成功当作本环境完成，不能在聚合数据库执行 OPS SQL。
- 对不受该变更影响的数据库，记录明确的“不适用”依据；不能把产品或联通假定为同库。共享 OPS 数据库遵守项目扩展准备规则，禁止项目全量迁移前移产品结构注册表。
- 在线索引准备独立于 HTTP 容器启动和普通迁移事务。正常镜像更新/服务重启不会删除数据库索引；换库、重建表或部分备份恢复后，以新的目录校验为准。

## 自动校验与约束范围

- scripts/release/check_index_policy.py 验证本规则和根 AGENTS.md 均受版本管理，并检查四分支共用规则完整；OPS 还核对独立索引 SQL、工具注册和索引说明是否一一对应。
- .github/workflows/index-policy.yml 对提交/拉取请求运行上述源码检查。远程分支保护是否要求该检查，由仓库实际配置决定；没有设置的不能声称已强制保护。
- OPS 发布包装器会在数据库准备失败时阻断后续部署命令。它只有在发布者使用规定入口时才生效；有权限手工绕过脚本的人或智能体，不能仅靠 Markdown 从技术上绝对禁止。
- 后续智能体必须先读根 AGENTS.md、使用规定发布入口、报告真实状态。源码检查通过不替代数据库就绪、蓝绿切流和业务接口验收；不得为文档更新重启业务服务。

## 钱包预占开发记录（2026-11-25，尚未发布）

- 通用能力先在产品基础开发：OPS 持久化原子占款，聚合仅维护独立 API Key 额度及恢复凭据，不重复扣用户钱包。默认关闭；启用须同时设置 `OPS_WALLET_RESERVATIONS=true`、`OPS_WALLET_AUTHORITY=ops` 并安装已验证的钱包归属适配器。产品默认没有该适配器，不能仅靠设置开关切换钱包。
- 聚合主库新增 `ops_wallet_hold_intents`，复合主键 `(gateway,request)`；`idx_ops_hold_release(gateway,cancel_pending,reconciled,updated)` 用于按凭证分批恢复。定义在 `model/ops_hold_token.go`，使用 ORM 支持 SQLite/MySQL/PostgreSQL。日志库不存预占凭据。准备使用相同模型 AutoMigrate；实际目录必须确认索引存在、字段顺序正确后才可启用候选实例。
- 本地真实 SQLite 3.50.4、MySQL 8.4.6、PostgreSQL 16.16 覆盖新表、原 token 表升级、连续迁移两次、同 Key 并发、重放、释放失败重启、过期未派发释放、已派发保留、异步终态及人工证据恢复。测试入口：`go test ./service/opsreserve -run TestDurableReservationRecoveryAcrossDatabases -count=1`；两个外部数据库用 `OPS_HOLD_TEST_MYSQL_DSN`、`OPS_HOLD_TEST_POSTGRES_DSN` 指向独立可销毁测试库。
- `cmd/ops-hold-operator` 仅读取显式 SQL_DSN 的既有主库，不迁移。默认列出最多100条未核对记录；变更必须显式提供 request、decision、evidence、reviewer。`confirmed_no_charge` 仍须远端确认 released 才退 Key；`posted_charge` 须远端已 settled，并传经复核的 Key quota。证据引用及复核人保留在原记录，不能用超时或年龄代替上游终态凭据。成功任务崩溃后持久化 quota 可能仍为估计值，禁止自动把该值当最终费用。
- 当前预占按估计值及 OPS 报价保底，不能声称覆盖所有上游实际费用上限。实际费用超过可用资金时保留待结算责任，不能生成负钱包或把未过账显示为已扣款。异步项目映射、端到端、容量、滚动启用及联通发布仍需独立验证。
- 本记录仅源码和本地测试交付；联通供应商配置另有已执行操作，不能把配置完成等同钱包部署。尚无联通预占索引门禁回执、应用发布或性能结论。

- 续测：新增四列恢复索引定义验证，同名错误字段顺序立即拒绝；真实三库均验证缺失补建、重复准备和冲突定义。任务408/409/5xx或丢失响应停止再次付费派发，保留占款；400明确拒绝释放。Midjourney占款关联、不可变用量/退款outbox及旧轮询结果不覆盖退款额度。Responses WebSocket非终态断开保留占款，不把不完整用量当最终结算。
- 2026-11-25本地继续验证：service/relay/controller完整套件通过（controller须从包目录执行以加载既有测试夹具），产品前端Bun构建与go build ./...通过。容量、所有模态和滚动启用尚未完成，当前提交为开发检查点，禁止据此直接启用生产。


## 2026-11-25 原子钱包预占：本机验收与发布准备（未部署）

- 最新用户范围：产品基础先实现并本地提交，产品两分支暂不推送、不部署；只推送项目两分支并发布联通测试119.39.149.67。国内、海外、联通正式服务器不操作。供应商2条/模型绑定4条已独立配置；不等于钱包上线。
- OPS持久预占及实际扣款同事务；调额不能花掉在途占款；关闭准入开关仍保护、查询、释放和结算原占款。费用超出估计且余额不足时保留待结算责任、阻止后续准入，回执不得假称已扣款，充值后幂等补扣。预占不是所有模态的费用上限承诺；不限制输出、工具、动态价格等仍可能产生超估计消费。
- Gateway独立Key额度及不可变用量日志在主库同事务，独立显示日志库故障不丢结算凭据。受限异步队列与批量上报共用Redis发送预算；保留扫描补漏和重放去重。任务未知响应不自动重发付费调用；已派发占款不按年龄释放。WS不完整用量、派发后进程崩溃但没有最终凭据等仍需人工核对，不能把留置占款冒充全部自动恢复。
- Gateway真实SQLite3.50.4/MySQL8.4.6/PostgreSQL16.16验证预占意图、并发Key预算、迁移重复准备、错误索引定义拒绝、恢复、任务及显式owner交接。最新project-reserve40全三库通过；project-service35全套、project-push31全套/三库、project-push37真实Redis预算、project-e2e31真实签名journal到OPS项目套件通过。OPS project-station37全套、project-billing2、真实PG索引维护及构建/vet通过。首次验证中发现的夹具隔离、CRLF及脚本问题已修复，原失败日志保留。
- OPS本地两个服务实例、32并发共256次占款，精确核对占款3840微元，约59.5次/秒、p95约618ms；不代表模型总延迟、任意并发1秒或CPU上限保证。现有全局数据库写锁仍限制容量。并发限流验证独立reservation预算，不挤占GET读取配额。
- Gateway新增ops_wallet_hold_intents及idx_ops_hold_release(gateway,cancel_pending,reconciled,updated)，主库专属。ops-hold-operator -prepare实际本地CLI连续执行两次通过；-handoff-from/-handoff-to保留游标和原待发送批次，拒绝活动任务和错误owner。OPS发布必须用本版本fee-indexes及prepare_fee_indexes.py准备第7项ops_wallet_reservations_open_user并独立验证；现网尚未执行，不得声称已完成。
- 上线必须先升级全部OPS资金writer和唯一后台worker，保留旧登录owner；Gateway扫描owner和async/MJ结算owner经排空显式交接后才能开启预占。回退只用兼容占款版本，不能让旧writer忽略占款；不停止现有调用、不强制注销会话、不设维护拦截。
- 临时UAC发布适配仅项目分支：显式SID摘要到旧owner映射，仅本地family缺失且为切换前会话时请求owner正常鉴权；本地撤销/过期不回退，不复制UAC凭证。真实旧版b8be256a7与候选middleware/controller两进程验证会话、刷新轮换和撤销通过，原owner登出路由须保留。依据OWASP Authentication/Session Management Cheat Sheets与ASVS5.0.0校验服务端会话、撤销、时限、拒绝和无敏感日志；不是全面合规认证。
- 当前仅源码本机验收；未启用联通预占、未部署wallet镜像、未推送项目。本记录需在实际发布后追加目标库索引、版本、切流和接口/分销同步实测结果，不能把本地结果写成线上通过。
