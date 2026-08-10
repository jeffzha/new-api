# Claw Workbench 跨地域灾备

本目录在不修改应用、Compose 以及现有 `scripts/backup.sh`、`restore.sh`
的前提下，为现有备份增加 restic 加密、两个独立 S3/COS 地域、保留策略、
完整仓库校验和隔离恢复演练。Python 编排层不依赖 shell 语义；只有生产备份
步骤会按配置调用现有 `backup.sh`。

## 明确的数据范围

现有 `backup.sh` 只生成 control PostgreSQL、ADP PostgreSQL 和 Redis 取证 RDB。
本工具验证并异地保存的也是这个范围。Redis 快照不能作为业务恢复源。
`evidence_data` 卷、独立 evidence 主密钥、new-api 数据库以及对象存储业务文件
不在当前源备份内，因此不能根据本报告宣称整个系统已经满足 RPO/RTO。
启用这些能力前，仍必须取得同一恢复点快照并另行完成密钥与对象恢复演练。

## Fail-closed 约束

- 默认动作是 `plan`，不连接仓库、不执行备份。
- 主、异地目标的 endpoint、bucket、region 必须逐项不同；相同即拒绝。
- endpoint 只能是 HTTPS。`--allow-http` 仅允许 loopback fake-restic 测试。
- AK/SK、临时 Token 和 restic repository password 只能配置为 `env:NAME`
  或受限的 `file:/absolute/path` 引用，绝不能直接写入 JSON、命令行或报告。
- Windows 无法可靠用 POSIX mode 验证 secret 文件 ACL，因此 secret 文件引用
  在 Windows 主动失败；Windows CI 使用环境变量。
- restic、现有 backup 脚本、shell、`pg_restore` 等所有绝对依赖都必须配置
  SHA-256；restic 的 `version` 首行还必须精确匹配。tag、PATH 中的浮动 restic、
  symlink 或 hash 不一致全部拒绝。
- backup 子进程不会继承操作员 `PATH`，而是只使用已固定执行文件所在目录；
  `environment_allowlist` 明确禁止加入 `PATH`。
- 现有备份只有在 `SHA256SUMS` 精确覆盖四个预期文件且逐一匹配后才上传。
- 每个目标上传后必须通过 `restic check --read-data`，否则不会标记为双地域验证。
- restore drill 只能写入与所有 `protected_roots` 完全隔离的临时目录，且结束
  后删除。工具从不调用现有生产 `restore.sh`，也不接受生产目标目录参数。
- 初始化、真实写入/清理、只读检查和恢复演练分别使用不同的显式确认串。
- 报告不记录 secret、restic 环境、原始 stdout/stderr 或完整 snapshot ID。

## 配置

部署 overlay 的 `COMPOSE_PROJECT_NAME` 强制固定为 `claw-workbench`，因此示例
`protected_roots` 中的 `claw-workbench_control_db_data`、
`claw-workbench_adp_db_data` 和 `claw-workbench_evidence_data` 才与 Docker
实际 volume 名一致。不得只改 Compose project name 而沿用本配置；preflight
会拒绝该变化。

把 `config.example.json` 复制到 Git 工作区之外，例如
`/etc/claw-workbench/dr.json`，并替换所有全零 hash：

```sh
sha256sum /usr/local/bin/restic /usr/bin/dash /usr/bin/docker \
  /usr/bin/python3 /usr/bin/sha256sum /usr/bin/date /usr/bin/sed \
  /opt/new-api/deploy/claw-workbench/scripts/backup.sh \
  /opt/new-api/deploy/claw-workbench/scripts/compose.sh \
  /opt/new-api/deploy/claw-workbench/scripts/strict_dotenv.py \
  /opt/new-api/deploy/claw-workbench/scripts/verify_backup.py \
  /usr/bin/pg_restore
/usr/local/bin/restic version
```

版本字符串必须完整复制首行，不能写 `latest`、范围或只写 `0.18`。两个存储
账户都只授予各自 bucket/prefix 所需的列举、读、写、删除权限；不要跨地域
复用凭据或 repository password。示例的远端保留策略是 14 日、8 周、12 月、
3 年，本机只保留最近 3 份已验证明文备份。本机清理仅在两地 prune 后完整
check 都成功时执行；发现未知目录、symlink 或未验证文件会停止且不删除任何项。

生产建议通过 root-only `/etc/claw-workbench/dr.env` 注入：

```text
CLAW_DR_PRIMARY_ACCESS_KEY_ID=...
CLAW_DR_PRIMARY_SECRET_ACCESS_KEY=...
CLAW_DR_PRIMARY_RESTIC_PASSWORD=...
CLAW_DR_SECONDARY_ACCESS_KEY_ID=...
CLAW_DR_SECONDARY_SECRET_ACCESS_KEY=...
CLAW_DR_SECONDARY_RESTIC_PASSWORD=...
```

该文件设为 root 所有、`0600`，不得提交、复制进聊天或由 systemd 输出。
也可把 JSON 中引用改成 POSIX `0600` secret 文件。

## 操作顺序

先做无网络计划：

```sh
python3 dr/dr.py plan --config /etc/claw-workbench/dr.json \
  --output /var/lib/claw-workbench-dr-reports/plan
```

首次初始化两个全新仓库：

```sh
python3 dr/dr.py init --config /etc/claw-workbench/dr.json \
  --output /var/lib/claw-workbench-dr-reports/init \
  --write --confirm INIT_BOTH_REPOSITORIES
```

执行现有备份、生成 `dr-manifest.json`/`DR_MANIFEST.sha256`、分别上传并
完整校验两地仓库：

```sh
python3 dr/dr.py backup --config /etc/claw-workbench/dr.json \
  --output /var/lib/claw-workbench-dr-reports/backup \
  --write --confirm WRITE_BOTH_REGIONS
```

独立只读完整检查：

```sh
python3 dr/dr.py check --config /etc/claw-workbench/dr.json \
  --output /var/lib/claw-workbench-dr-reports/check \
  --execute --confirm CHECK_BOTH_REPOSITORIES
```

应用 retention 并再次完整检查：

```sh
python3 dr/dr.py retention --config /etc/claw-workbench/dr.json \
  --output /var/lib/claw-workbench-dr-reports/retention \
  --write --confirm APPLY_RETENTION_BOTH
```

默认从异地 `secondary` 最新 snapshot 做隔离恢复，校验 manifest、所有源
checksum，并对两个 PostgreSQL custom dump 执行固定 hash 的 `pg_restore --list`：

```sh
python3 dr/dr.py restore-drill --config /etc/claw-workbench/dr.json \
  --output /var/lib/claw-workbench-dr-reports/restore-secondary \
  --target secondary --snapshot latest \
  --restore --confirm RESTORE_ISOLATED
```

单次成功备份只能记录当次 snapshot freshness，不能证明长期 RPO 已达标；还要
根据 timer 历史监测连续成功备份的最大间隔，并对漏跑和失败报警。恢复演练中
“演练时刻减去实际恢复点时刻”才是该次场景的观测 RPO。

恢复演练报告里的 RTO 仅是“下载 + hash + dump 可读性验证”的耗时，状态固定
为 `partial_artifact_drill_only`。只有另行完成隔离数据库启动、应用启动、健康
检查、流量切换和客户验证，才可以评价完整服务 RTO 是否达标。dry-run、失败
检查或未实际恢复时，RPO/RTO 一律是 `not_exercised`/`not_verified`。

## systemd timer

`systemd/` 提供每日备份和每周 `--read-data` 校验示例。安装前按实际路径调整
unit，创建 root-only 配置、环境与报告目录，然后：

```sh
install -m 0644 dr/systemd/claw-workbench-dr-*.service /etc/systemd/system/
install -m 0644 dr/systemd/claw-workbench-dr-*.timer /etc/systemd/system/
install -d -m 0700 /var/lib/claw-workbench-dr-reports /var/lib/claw-workbench-drills
systemctl daemon-reload
systemctl enable --now claw-workbench-dr-backup.timer claw-workbench-dr-check.timer
systemctl list-timers 'claw-workbench-dr-*'
```

不建议自动调度 retention 或 restore drill；前者会删除 pack，后者需要隔离环境
容量和人工核对。报告输出 `dr-results.json` 和 `dr-report.md`，POSIX 下以 `0600`
原子写入；Windows 应使用仅操作员可访问的 NTFS 目录。

## 离线测试

测试只使用临时目录、loopback URL 和 fake-restic/fake-backup，不访问生产或
对象存储：

```powershell
python -m unittest discover -s deploy/claw-workbench/dr/tests -v
python -m compileall -q deploy/claw-workbench/dr
```
