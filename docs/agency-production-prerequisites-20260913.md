# 代理商正式发布前置修复与演练记录

日期：2026-09-13。目标：修复会话限流串扰、补齐生产 SSO 密钥配置、验证原 PostgreSQL 的增量升级。

## 当前完成边界

| 项目 | 已完成 | 尚未生效的部分 |
| --- | --- | --- |
| 登录限流 | 源码修复；内存和 Redis 路由回归通过 | 需提交并发布新主站版本；没有替换当前正式主站镜像 |
| SSO | 校验原 Ed25519 密钥对匹配；正式 Compose 基础服务补充两份只读挂载，Green 继承后也具备挂载；应用 UID 10001 读取测试通过 | 现有主站容器未重建，旧运行容器不会自动获得新增挂载 |
| 数据库 | 完整备份和异机副本；隔离恢复、连续两次迁移、原数据指纹与新 Hub readiness 验证通过 | 正式数据库未执行本次 DDL，仍需维护窗口停旧 Hub 写入器并协调新 Hub 上线 |

未切换公网流量，未开启新客户代理归属、佣金消费者或提现；未修改主站管理员密码。没有把准备和演练描述为生产功能已上线。

## 限流行为

- 登录、注册、2FA 等原 CT 密码尝试额度保持不变。
- `POST /api/user/auth/refresh` 和 `POST /api/user/auth/logout` 改用各自独立的 IP 桶，默认各 120 次/60 秒。
- 新配置为 `AUTH_SESSION_RATE_LIMIT` 和 `AUTH_SESSION_RATE_LIMIT_DURATION`；非正数回退默认值，时间窗口不超过现有内存桶保留期。
- 代理商 SSO 签票在 Root 鉴权后按用户限流，不再消耗 IP 的登录额度。
- 代理商 verify 与 command-proof 都验证 Root 密码，因此共用一个用户级验证额度，避免交替调用或换 IP 增加猜测次数。
- 不清空 Redis 现有 CT 记录；原限流状态按 TTL 到期。此次修改不重置任何密码或登录会话。

回归使用真实路由、隔离 SQLite、有效 JWT 和刷新 Cookie；覆盖内存/Miniredis、Origin 拒绝、刷新轮换、退出撤销、各桶隔离，以及不同 Root 与不同 IP 的边界。

## 正式配置修改

服务器：`47.97.97.89`；Compose 项目：`new-api-seedance`。

`/opt/new-api/deploy/compose.yml` 的 `new-api` 服务增加：

```yaml
volumes:
  - /opt/new-api/deploy/agency-secrets/agency_sso_private.pem:/run/secrets/agency_sso_private_key:ro
  - /opt/new-api/deploy/agency-secrets/agency_sso_public.pem:/run/secrets/agency_sso_public_key:ro
```

这是对原 `volumes` 列表的追加，不是替换原日志、数据、Workbench 密钥挂载。完整合并链为基础配置、Blue 覆盖文件和 Green 覆盖文件；已经验证两服务的有效配置均包含上述挂载。既有 SSO 环境变量不变。

原 SSO 私钥、临时密码交付加密密钥、提现加密密钥原为 `root:root 0600`，已调整为 `root:10001 0640`，以便实际运行用户读取。密钥内容未修改，未重新生成或轮换。公钥不变。

读取探针使用原主站镜像、UID/GID `10001:10001`、`--network none`、只读根文件系统和只读 bind mount；仅检查文件可读，没有请求生产 SSO 接口或创建登录会话。

## 备份与恢复演练

服务器备份目录：

```text
/opt/new-api/deploy/backups/agency-prereq-20260913-LxEPR3
```

包含原 Compose 三文件、Caddyfile、私有 `.env`、代理商密钥归档、私有容器配置快照、PostgreSQL custom-format dump 和 SHA256。目录由受限 `umask 077` 创建；其中包含秘密，不要上传到 Git、工单或公开聊天。

数据库备份为 10,157,303 bytes，SHA256：

```text
d409b7a371e5a4c5dccb7591bac0045a4f0154f999a4a586cfe87eda637ea88b
```

数据库另有本机 E 盘副本：`E:/new-api-test-cache/agency-release-prep/backup-20260913/newapi.dump`。该目录已移除继承权限，仅当前 Windows 用户和 SYSTEM 可访问；下载后的 SHA256 与服务器一致。

隔离容器 `agency-prereq-rehearsal-20260913` 使用当前生产 PostgreSQL 镜像的精确 image ID，`--network none`，无宿主端口，限制 512 MiB 和 1 CPU。只恢复到独立的 `agency_restore` 数据库和独立卷，未挂载生产数据卷。测试后容器已停止，保留恢复数据便于复核，没有删除生产或演练数据。

备份恢复出的快照包含 138 个用户、13 个渠道、24 个充值记录、1 个代理商；这是备份时点，不代表生产实时计数。

执行顺序：

1. `pg_restore --exit-on-error --no-owner --no-privileges` 恢复到隔离数据库。
2. 对原有 97 张表，保存原始列集合的行数和有序数据指纹。
3. 执行 PostgreSQL 专用的 nullable 核心列增量 SQL，再执行本次源码构建的 Linux `agency-hub migrate`。
4. 完整重复第 3 步，验证幂等。
5. 再次使用原列集合计算指纹：97 张表全部保持一致。新列回填不混入原字段比较。
6. 在同一隔离容器中以 UID 10001 启动新 Hub，保持开通/佣金/提现关闭，验证 `/agency/readyz`。

结果：两次迁移成功；Agency 表为 44 张；`missing_tables`、`missing_columns`、`missing_indexes` 均为空；`ready=true`。不把未开启的佣金处理及真实 SSO 登录计入这项 readiness 验收。

本次 Linux Hub 二进制 SHA256：`79f4521a92adf8e3ac31ce269daf509b62359ae08c2e34f032ce37cf183b2690`。这是演练构建，不是已部署的新正式镜像。

具体证据在服务器备份目录的 `rehearsal-result.json`、`rehearsal-ready.json`、两次迁移日志和前后指纹文件中。

## 正式执行前仍需确认

1. 安排维护窗口。主站新增挂载需重建容器；不能假定配置写入后已生效。新限流逻辑还必须随新镜像发布。
2. 正式执行前再做新备份，避免把早先演练快照当成最新回退点。
3. 暂停旧 Hub、独立 worker 和对账任务；新旧 Hub 写入器不能跨 schema 回填混跑。
4. 用演练过的核心字段 SQL和新版本 `agency-hub migrate` 执行增量升级。核心 SQL不替代主站其他认证、业务表的正常版本迁移。
5. 以新 Hub、原有持久密钥和正确挂载启动并验证 readiness；与所有主站槽位及后台执行节点兼容后才能开放新代理用户。
6. 有 durable 用户后，不得简单切回不支持该计费语义的旧镜像。禁止 `down -v`、`--remove-orphans`、重建数据库或直接覆盖真实余额。

本轮收尾检查：Blue、Green、旧 Hub、PostgreSQL 均保持健康，容器创建时间未变；Caddy SHA256 与备份前相同，Blue 100% / Green 0% 未改动。
