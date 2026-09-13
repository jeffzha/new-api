# Agency Hub 内部命令通道部署

本文件对应 new-api 网关与 Agency Hub 的独立 mTLS 命令通道。资金冲正由 Hub 提交，网关验证签名及当前 Root 会话后执行。网关把业务变更和永久命令回执放在同一数据库事务中。此配置不代表代理系统全部生产验收完成，也不开放组件支付拒付或提现。

## 1. 前提与文件

- 两个网关槽位的镜像均包含 `service/agency_command_server.go`，Hub 镜像包含远端命令客户端。
- 保留现有共享 PostgreSQL 数据库、数据卷、Compose 项目名和全部蓝绿覆盖文件。按现有发布流程先备份再做增量迁移；本覆盖文件不定义 PostgreSQL 服务或卷。
- 已有 Root SSO 公钥及 Hub 服务签名密钥继续使用；mTLS 证书是另一套凭证，不能代替业务签名。
- 将 [compose.commands.override.yml](compose.commands.override.yml) 和 [commands.env.example](commands.env.example) 放入部署目录。将示例变量合入现有私有 `.env`，不要覆盖原有数据库密码等配置。

先在 Linux 服务器检查网段：

```sh
ip route
docker network ls
docker network inspect $(docker network ls -q) --format '{{.Name}} {{json .IPAM.Config}}'
```

示例 `172.30.250.0/29` 必须与 VPC、宿主和已有 Docker 网段均不冲突。确认后为 Blue、Green、Hub 分配 `.2`、`.3`、`.4`。所有三个地址必须属于同一指定网段且互不相同。

## 2. 证书

通过组织证书流程签发以下材料，PEM 格式：

| 文件 | 要求 | 使用端 |
| --- | --- | --- |
| `ca.crt` | 签发本通道证书的 CA 公钥证书 | 双方 |
| `server.crt`、`server.key` | `serverAuth` EKU；DNS SAN 为 `agency-command.gateway.internal` | 两个网关槽位 |
| `client.crt`、`client.key` | `clientAuth` EKU；URI SAN 为 `spiffe://nexight/agency-hub` | Hub |

当前模板使用同一专用 CA 验证双方。网关额外严格校验客户端 SAN 身份；同 CA 下的其他客户端身份也会被拒绝。服务端名称验证由 `AGENCY_HUB_COMMAND_SERVER_NAME` 指定，连接地址仍为目标槽位的固定私有 IP。所有连接要求 TLS 1.3。

Linux 上核验材料，无需输出私钥：

```sh
openssl verify -CAfile /opt/new-api/deploy/agency-command-secrets/ca.crt -purpose sslserver /opt/new-api/deploy/agency-command-secrets/server.crt
openssl verify -CAfile /opt/new-api/deploy/agency-command-secrets/ca.crt -purpose sslclient /opt/new-api/deploy/agency-command-secrets/client.crt
openssl x509 -in /opt/new-api/deploy/agency-command-secrets/server.crt -noout -dates -ext subjectAltName,extendedKeyUsage
openssl x509 -in /opt/new-api/deploy/agency-command-secrets/client.crt -noout -dates -ext subjectAltName,extendedKeyUsage
```

Compose 的文件型 Secret 保留宿主文件权限，不能只依赖 YAML 的 uid/mode 重映射。让实际容器 UID/GID 有权读取各自私钥；当前发行镜像使用 `10001:10001`。例如将本通道两份私钥设为 `root:10001`、`0640`，公钥证书设为 `0644`，并核验父目录访问权限。CA 私钥离线保管，不进入服务容器。

## 3. 合并配置与发布顺序

以当前服务器文件名为例，先只验证合并配置：

```sh
cd /opt/new-api/deploy
docker compose --env-file .env -p new-api-seedance \
  -f compose.yml \
  -f compose.blue.override.yml \
  -f compose.green.override.yml \
  -f compose.commands.override.yml \
  config --quiet
```

若现有服务由其他覆盖文件定义，应将这些原文件全部保留，命令通道覆盖文件放最后。三个服务的原 backend/edge 网络会保留并增加独立 `agency_commands` 网络。不要为端口 3443 添加 `ports`，也不要在 Caddy 添加 `/internal/agency` 的反代。

当前 `scripts/deploy-gateway-slot.ps1` 及其 Workbench 包装脚本只加载基础 Compose 和目标槽位覆盖文件，**不会自动加载 `compose.commands.override.yml`**。因此不能只运行原发布脚本再更新 Hub：这会使 Hub 指向没有内部监听的网关。即使本通道之前已经启用，后续用原脚本重建槽位也会丢失额外的环境、Secret 和网络配置。

发布必须按槽位完成以下顺序：原脚本备份数据库并构建/部署非活动槽位 → 保持该槽位流量为 0，用完整覆盖链补上命令通道 → 验证应用和内部通道 → 切换流量。切流量后另一槽位变为非活动槽位，再对其重复。每次后续发布也必须保留这一步，直到发布脚本正式支持附加覆盖文件。不要仅为接入本通道同时重建两个正在提供服务的槽位。

例如，**只有 Green 当前流量为 0，且原脚本已经完成 Green 镜像部署**，才能执行：

```sh
cd /opt/new-api/deploy
docker compose --env-file .env -p new-api-seedance \
  -f compose.yml \
  -f compose.blue.override.yml \
  -f compose.green.override.yml \
  -f compose.commands.override.yml \
  up -d --no-deps new-api-green

docker exec new-api-seedance-new-api-green-1 /new-api --version
docker exec new-api-seedance-new-api-green-1 wget -q -O - http://127.0.0.1:3000/api/status
docker inspect new-api-seedance-new-api-green-1 --format '状态={{.State.Status}} 健康={{if .State.Health}}{{.State.Health.Status}}{{end}}'
```

`--version` 必须仍为刚部署的候选版本，`/api/status` 返回成功，并等待健康状态为 `healthy`；还需按第 4 节核验命令通道。之后才使用现有切流量脚本。切换后 **Blue 流量为 0** 时，按原脚本部署 Blue，再使用完全相同的 `-f` 链将末尾服务名改为 `new-api`，并检查 `new-api-seedance-new-api-1`。

内部命令目标与公网蓝绿权重是两个独立配置。后续升级任一槽位前，若 Hub 当前指向它，应先将 Hub 指向另一个已经兼容且健康的槽位并验证；公网权重为 0 不代表没有内部命令。不要使用 `down -v`、`--remove-orphans` 或重新初始化数据库。

两个兼容槽位已就绪后，设置 `AGENCY_COMMAND_TARGET_IP` 指向选定槽位，再只更新 Hub：

```sh
docker compose --env-file .env -p new-api-seedance \
  -f compose.yml \
  -f compose.blue.override.yml \
  -f compose.green.override.yml \
  -f compose.commands.override.yml \
  up -d --no-deps agency-hub
```

这是待实际发布时执行的命令，不是本次开发已经执行的操作。目标槽位停止后，Hub 会报告通道不可用，不会回退直接写库。切换内部目标时更新变量并重建 Hub；失败请求重试必须保留原 `command_id` 和原签名正文。

## 4. 发布验收

- 三个容器的 `agency_commands` 地址符合配置；3443 没有宿主端口映射。
- 普通 Hub HTTP 路由下 `/internal/agency/v1/commands` 返回 404。公网网关可能有 SPA 兜底，不能仅凭公网 HTTP 200/404 判定命令监听暴露；同时检查反代配置、监听地址和 Docker 端口映射。
- 无客户端证书、错误 CA、错误身份、缺失业务签名或失效 Root 会话均不能接受命令。仅 mTLS 握手成功不代表资金操作已授权。
- 先在预发布数据库执行一条批准的测试命令，轮询内部 GET 回执；确认成功、重复提交返回同一结果、资金及 outbox 只变一次。
- 内部 GET 必须携带原命令服务签名；普通 `curl` 没有签名而得到拒绝是预期行为。不要使用真实资金冲正作为健康探测。

本地已覆盖真实 TLS 客户端/服务端、签名和撤权边界，另有三库命令事务测试入口 `TestAgencyCommandAtomicExecutionAcrossDialects`。生产网段、证书部署与轮换、最小数据库授权、真实 SSO 联调仍需要各自验收。

现有旧用户 provisioning 的浏览器入口和执行所有权仍有迁移工作；当前共享数据库权限不等于已经实现最小权限隔离。上线审计以 [剩余工作清单](../../docs/agency-hub-remaining-work.md) 为准。
