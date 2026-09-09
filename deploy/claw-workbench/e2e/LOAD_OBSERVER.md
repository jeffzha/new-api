# 独立 SSE 生产负载采集器

`load_observer.py` 是 `load_sse.py` 的独立、只读生产观测进程。它只通过
Prometheus HTTP API 的 `GET /api/v1/query` 和 `GET /api/v1/query_range`
取数，不连接业务数据库、不调用模型接口，也不接受命令行或 JSON 中手写的
连接数、内存、请求数、时间窗口、Turn 数或延迟值。

采集器建立实时指标基线后写入 `.ready` 文件；50 或 100 个请求由另一个进程
执行。采集器从 Prometheus counter 自动识别终态数量，达到本档并发数后读取
完整时间范围，生成 `load_sse.py` 所要求的
`claw-load-observer/v1` 严格证据。任何指标缺失、空序列、NaN/Inf、counter
不一致、直方图不完整、Prometheus warning、重定向、超时或响应格式变化都会
阻止 `.ready` 或最终证据生成。

最终 JSON 还必须带独立 OpenSSH Ed25519 detached signature。Observer 独占
私钥，固定 identity 为 `claw-load-observer`、namespace 为
`claw-load-external-evidence-v1`；load runner 只能读取 pinned public
`allowed_signers`。该密钥必须与主 E2E 报告的 Ed25519 密钥不同。签名、身份、
namespace、公钥任一不匹配，或 JSON 被修改、缺少 `.sig`，均不能通过。

## 当前部署前置条件

仓库中的 `observability/prometheus.yml` 目前只是参考配置，且只声明抓取
claw-control 和 ADP。ADP 应用指标已经包含独立的终态 Turn counter
`workbench_turns_total`、成功提交的 Turn-event 行 counter
`workbench_turn_events_persisted_total`，以及直接包围每次成功 Turn-event 数据库
事务的 `workbench_turn_event_persist_db_seconds` histogram；但仓库当前仍没有声明生产
Prometheus 本身，也没有完整声明以下两个由运行平台提供的数据源：

- Edge、new-api、claw-control、ADP 的 HTTP active-request/connection gauge；
- new-api、claw-control、ADP 的进程 RSS 或容器 working-set bytes gauge；

因此，在生产 Prometheus 真正抓取这些指标前，采集器会按设计 fail-closed，
不能用 `vector(50)`、手写 JSON、Turn 总耗时或任意 HTTP 延迟冒充这些数据。
可以使用生产平台已有的 cAdvisor established-TCP/working-set series（并由
Prometheus relabel 写入稳定 `component` 标签）或语义等价的受保护 exporter；
不得为了本 overlay 自采集而向容器开放 Docker socket、host PID 或 host network。
持久化事件必须使用 `workbench_turn_events_persisted_total`，不能复用终态 Turn
counter。DB 指标必须使用 `workbench_turn_event_persist_db_seconds`，不能用
`workbench_turn_duration_seconds` 替代。Blue/Green 实例必须使用同一个 `job`
并保留不同 `instance`，采集器会按时间点跨实例求和并取峰值。

示例文件 `load-observer.config.example.json` 中的指标名是所需语义的参考名称。
上线时只应替换为生产 Prometheus 中语义等价的真实 metric/固定 label selector。
配置只接受 metric 名和 `=` label，不接受任意 PromQL 表达式，因此不能把常量
包装成查询结果。

## 配置约束

Observer JSON 使用闭集 schema：未知字段直接失败。主要配置含义如下：

| 字段 | 含义 |
|---|---|
| `prometheus_url` | Prometheus 根地址。生产必须是 HTTPS；仅自测可显式允许 loopback HTTP |
| `bearer_token` | 只读 query token 的 `env:`/`file:` secret 引用，或 `null` |
| `ca_file` | 内部 CA 的绝对 `file:` 引用，读取后以内存 PEM 建立 TLS，不跟随软链接 |
| `poll_seconds` | 等待终态 Turn counter 达到 50/100 时的查询间隔 |
| `range_step_seconds` | range query 的采样步长；必须不大于 Prometheus scrape interval |
| `evidence_signing_key_file` | Observer 独占的 OpenSSH Ed25519 私钥；只能是受限权限的绝对 `file:` 引用 |
| `active_connections_peak` | 四个组件的实时 active gauge selector |
| `memory_peak_bytes` | 三个服务的 bytes gauge selector |
| `terminal_turns` | 终态事务成功后才递增、带固定状态标签的 Turn counter；用于等待档位和计算成功数 |
| `persisted_turn_events` | 每个成功事务按实际新增 Turn-event 行数递增的独立 counter；不得与 `terminal_turns` 使用同一 metric family |
| `db_latency` | 同一 seconds histogram 的 `_bucket`/`_count` 和 `le` 标签 |

Token 只能有 Prometheus query 权限，不应具有 rule、target、admin、remote-write
或配置修改权限。远程 HTTP、URL 内凭据、URL path/query/fragment、重定向以及
非 JSON 响应都会失败。

## 执行顺序

先准备专用于负载端的 E2E 配置：只能含
`acceptance_report_allowed_signers_file` 公钥 allowed-signers 引用，不能含主验收
使用的私钥。它的 `load.external_evidence.50/100` 同时决定采集器唯一允许写入的
输出路径。另在 `load` 中配置独立 Observer 公钥：

```json
{
  "acceptance_report_allowed_signers_file": "file:/secure/keys/main-e2e-allowed_signers",
  "load": {
    "external_evidence": {
      "50": "file:/secure/evidence/claw-load-50.json",
      "100": "file:/secure/evidence/claw-load-100.json"
    },
    "external_evidence_allowed_signers_file": "file:/secure/keys/load-observer-allowed_signers"
  }
}
```

Observer 的独立配置才允许出现私钥：

```json
"evidence_signing_key_file": "file:/run/secrets/load-observer-ed25519"
```

`load-observer-allowed_signers` 必须恰好一条：

```text
claw-load-observer ssh-ed25519 <observer-public-key>
```

load runner 的运行时校验与 JSON Schema 都拒绝
`external_evidence_signing_key_file`；它无法生成自己的外部观测证据。Observer
启动时还会证明私钥匹配上述公钥，且与
`claw-workbench-e2e` 主报告公钥不同。

每个档位使用独立采集器，必须先看到 `READY`，再启动对应档位；不要同时运行
两个指向同一 Prometheus counter 的档位：

```powershell
$env:TEMP='D:\codex\.tmp'
$env:TMP='D:\codex\.tmp'

python deploy/claw-workbench/e2e/load_observer.py `
  --config C:\secure\claw-load-e2e.json `
  --prometheus-config C:\secure\load-observer.json `
  --acceptance-results C:\secure\claw-e2e-report\results.json `
  --concurrency 50 --timeout-seconds 1800
```

采集器完成基线后输出：

```text
READY C:\secure\evidence\claw-load-50.json.ready
```

此时在另一个终端启动单档负载。`--confirm-task-count` 必须等于本档位：

```powershell
python deploy/claw-workbench/e2e/load_sse.py --execute --allow-provider-cost `
  --concurrency 50 --confirm-task-count 50 `
  --acceptance-results C:\secure\claw-e2e-report\results.json `
  --config C:\secure\claw-load-e2e.json `
  --output C:\secure\claw-load-report-50
```

采集器观察到 50 个持久化终态后，会原子写入
`load.external_evidence.50.sig`，最后才发布
`load.external_evidence.50`，因此 load runner 不会看见“已发布但尚未签名”的
窗口。负载进程先按固定 observer identity/namespace 验签，再将自己的请求计数
和精确时间窗口与 JSON 绑定校验。50 档完成后，对 100 档重复相同步骤。

不要在采集器 `READY` 前启动负载。证据窗口在 baseline 完成后、READY 写入前开始；
READY 后才发请求可以确保窗口覆盖完整负载。采集器启动时会安全删除自己目标
路径上旧的普通文件，避免 `load_sse.py` 误读上一次运行的证据；遇到软链接、
目录或其他特殊文件会拒绝继续。

## 证据计算

- `acceptance_run_id`、`release_manifest`：来自经过 OpenSSH Ed25519 验签并完成
  严格重算的主 E2E `results.json`；调用者不能单独传入。
- 外部 JSON 的完整字节由 Observer 专用 Ed25519 私钥签名；`collector_id` 只是
  schema 字段，不能替代密码学来源证明。
- `window_started_at`：所有只读 preflight/baseline 查询成功后、写入 READY 前的 UTC
  时间；
  `window_finished_at` 是观察到目标终态并开始最终 range snapshot 的时间；
  `collected_at` 在所有查询完成后生成。
- `planned`：只可能是本次选择的 50 或 100；`terminal` 和 `successful` 分别是
  `workbench_turns_total` 全状态与 `completed` 状态的 counter increase。counter
  reset 会按 Prometheus counter 语义累计，新增状态序列的 baseline 为零。
- active connection：同一时间点将所有实例相加，然后取整个窗口最大值；
  `sample_count` 是七组 active/memory range 中最小的采样点数。
- memory：同一服务各实例 bytes 之和的窗口峰值。
- persisted Turn events：`before` 是独立 event-row counter 的 baseline 总值，
  `delta` 是窗口内成功提交的实际新增 `WorkbenchTurnEvent` 行数，
  `after = before + delta`；它必须不少于终态 Turn 数，不能由终态 counter 推导。
- DB latency：从真实 histogram 的 bucket/count counter increase 在本地重建
  P50/P95/P99，并由 seconds 转为 milliseconds；每个至少写入一行 event 的成功事务
  观测一次，批量 recovery 事务仍只观测一次，因此 histogram count 不得大于 event-row
  delta；`+Inf` 必须精确等于 count。

写文件前采集器自身会调用与 `load_sse.py` 相同的
`validate_external_evidence()`。负载进程仍会使用自己的真实请求窗口和请求结果
再验证一次，所以后台无关流量、遗漏请求或错误 release/run 绑定都不能通过。

## 自测

测试只启动 loopback fake Prometheus，不访问生产：

```powershell
$env:TEMP='D:\codex\.tmp'
$env:TMP='D:\codex\.tmp'
python -m unittest deploy/claw-workbench/e2e/tests/test_load_observer.py -v
```
