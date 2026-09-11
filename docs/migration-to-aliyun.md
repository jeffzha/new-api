# new-api 系统迁移：火山云 → 阿里云（实操步骤）

> 目标：把当前部署在 `root@124.174.0.221`（火山引擎/OpenStack, Ubuntu 24.04, Docker）上的
> **完整系统**迁到阿里云新购的 `ecs.g8i.xlarge`（4C/16G/Ubuntu 24.04）上。
> 本文档为**实操清单**，请按顺序执行；涉及停机的步骤会标注【停机】。

---

## 0. 现状与拓扑（先读，别跳过）

源机上有**两个互有耦合的 Docker Compose 项目**：

| 项目 | 目录 | 内容 |
|---|---|---|
| `new-api-seedance` | `/opt/new-api/deploy/` | 主网关：`new-api`(蓝)、`new-api-green`(绿)、`postgres`、`redis`、`caddy`、`api-docs`、`hwdrama-proxy` |
| `claw-workbench` | `/opt/new-api/deploy/claw-workbench/` | 腾讯侧控：ADP 蓝/绿、claw-control 蓝/绿、workbench-switch、identity-proxy、2×postgres、redis、clamav |

**关键耦合（必须一起搬）：**
- `new-api` 容器挂载了 `/opt/new-api/deploy/claw-workbench/secrets/{internal_ca.crt,new_api_control_hmac,new_api_identity_hmac}`，**缺了 new-api 起不来**。
- 两个项目分别用 `compose.yml`（+ 蓝/绿 override）定义；配置、`.env`、Caddyfile、secrets 全在 `/opt/new-api/deploy` 下。

**待迁移的资产：**
1. **配置/代码目录**：`/opt/new-api`（deploy、backups、claw-control、releases、build、e2e）以及可能相关的 `/opt/adp-chat-client-claw`。
2. **容器镜像**：两个项目的所有镜像，其中 new-api 蓝/绿是**服务器本地构建的 tag**
   `new-api-seedance:v1.0.0-rc.25.gateway.20260822T003335Z.g8fed849a11e2`（蓝）、
   `new-api-seedance:v1.0.0-rc.25.gateway.20260821T144947Z.g03d66f4a5e0c`（绿）、`new-api-seedance:886dee829`（hwdrama），
   其余为公共镜像（postgres/redis/caddy 等）和 claw-workbench 的无 tag 镜像（按 image ID 保存）。
3. **Docker 数据卷**：
   - `new-api-seedance_*`：`postgres_data`、`redis_data`、`new_api_data`、`new_api_logs`、`caddy_config`、`caddy_data`（Caddy 自动签发的 TLS 证书在 `caddy_data`）。
   - `claw-workbench_*`：`control_db_data`、`adp_db_data`、`adp_blue_logs`、`adp_green_logs`、`evidence_data`、`redis_data`、`clamav_db`、`identity/switch` 的 caddy data/config。
4. **数据库**：PostgreSQL `newapi`（网关主库）+ claw-workbench 的 control/adp 两个库。
5. **变量/密钥**：`/opt/new-api/deploy/.env`（POSTGRES_PASSWORD、SESSION_SECRET、HWD_PROXY_ADMIN_TOKEN、WORKBENCH_ENABLED）、`hwdrama-proxy-secrets.env`、以及 `claw-workbench/secrets/*`。
6. **域名/TLS**：`gateway.nexus-reach.com` 的 DNS 需改指向新 IP；Caddyfile 中还绑定了裸 IP `124.174.0.221`（新机要改）。

> 变量约定：
> - `OLD` = `root@124.174.0.221`（源机，SSH 用 `BPlatform.pem`）
> - `NEW` = `root@<新机公网IP>`（阿里云，SSH 用你的新机密钥 `NEW_KEY`）
> - 两机都执行时，命令前缀会写清在 `[OLD]` 还是 `[NEW]` 下执行。

---

## 1. 前置准备

### 1.1 阿里云新机基础
- 地域：选离你客户近的可用区；镜像 **Ubuntu 24.04 64 位**；实例 `ecs.g8i.xlarge`；系统盘 ≥ 300 GB（建议 500 GB，源盘已用 63%）；公网带宽：不确定就先**按使用流量**（最高 100 Mbps），或固定 10 Mbps 起。
- 安全组放行：`22`（SSH）、`80`、`443`（HTTPS）。出方向默认全放（网关要访问上游供应商）。
- 用你的密钥能 `ssh root@<新机IP>` 登入。

### 1.2 域名 DNS 可改
- 确认 `gateway.nexus-reach.com` 的 DNS 记录你或对方能改，用于最后的切流量（改 A 记录指向新机公网 IP）。

### 1.3 确定维护窗口
- 迁移会有一小段停机（见 4.1）。挑低峰期，并提前告知下游客户。

### 1.4 先把钥匙准备好
- 源机密钥：`BPlatform.pem`（你本地已有）。
- 新机密钥：阿里云创建的实例密钥，保存到你本地并 `chmod 600`。
- 建议两机都允许 root SSH（阿里云默认 root 可登）。

---

## 2. 源机：打包镜像 + 一致性数据备份（可在线，不停机）

### 2.1 导出容器镜像
在执行机上（用 OLD）：
```bash
OLD="root@124.174.0.221"
# 1) 确认两个项目用到的镜像
ssh -i BPlatform.pem "$OLD" 'docker ps -a --format "{{.Image}}" | sort -u'

# 2) 把所有用到的镜像保存成一个 tar（含 new-api 本地构建 tag）
ssh -i BPlatform.pem "$OLD" \
  'docker save \
     new-api-seedance:v1.0.0-rc.25.gateway.20260822T003335Z.g8fed849a11e2 \
     new-api-seedance:v1.0.0-rc.25.gateway.20260821T144947Z.g03d66f4a5e0c \
     new-api-seedance:886dee829 \
     postgres:16-alpine redis:7-alpine caddy:2-alpine \
     a8455ab1c0bd 38968b7014e6 93ea3949d00f e70b4ec0dbe0 a73435b0fa51 \
     -o /root/images.tar && ls -lh /root/images.tar'
```
> 说明：claw-workbench 的镜像多为无 tag 镜像，用 `docker ps -a` 看到的 **image ID** 来 save（上例的 `a8455ab1c0bd` 等只是占位，务必用 2.1 第 1 步实际查到的 ID/名称替换）。若怕漏，最简单是 `docker save $(docker image ls -q) -o /root/images.tar` 全量打包（更占空间但最稳）。

### 2.2 PostgreSQL 主库一致性备份（pg_dump，在线执行，推荐）
```bash
ssh -i BPlatform.pem "$OLD" \
  'docker exec new-api-seedance-postgres-1 pg_dump -U newapi -d newapi -Fc -f /tmp/newapi.dump \
   && docker cp new-api-seedance-postgres-1:/tmp/newapi.dump /opt/new-api/backups/newapi_$(date +%F_%H%M).dump \
   && ls -lh /opt/new-api/backups/ | tail -3'
```

### 2.3 Redis（缓存/会话）备份
```bash
ssh -i BPlatform.pem "$OLD" \
  'docker exec new-api-seedance-redis-1 redis-cli SAVE \
   && docker cp new-api-seedance-redis-1:/data/dump.rdb /opt/new-api/backups/redis_$(date +%F_%H%M).rdb'
```
> Redis 是缓存，丢了会自动回源主库重建；此步是保险，非必须。

---

## 3. 新机：安装 Docker 并迁镜像/代码

### 3.1 新机装 Docker + compose（在 NEW 上）
```bash
NEW="root@<新机IP>"   # 替换
ssh -i NEW_KEY "$NEW" '
  apt-get update && apt-get install -y ca-certificates curl gnupg rsync
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" \
    | tee /etc/apt/sources.list.d/docker.list >/dev/null
  apt-get update && apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
  docker --version && docker compose version
'
```

### 3.2 把镜像 tar 传到新机并加载
```bash
scp -i NEW_KEY /root/images.tar root@<新机IP>:/root/images.tar      # 从 OLD 拉下来再推，或直传
# 在新机加载：
ssh -i NEW_KEY root@<新机IP> 'docker load -i /root/images.tar'
# 校验
ssh -i NEW_KEY root@<新机IP> 'docker images | grep -E "new-api-seedance|postgres|redis|caddy" '
```

### 3.3 迁移代码/配置目录 /opt/new-api（在线 rsync 大批量）
> 先把“不变量”搬走；DB/Redis/数据卷按 4.x 单独处理一致性。
```bash
# 在执行机（或 OLD）上 rsync 到 NEW；用 NEW 的密钥
rsync -aH --info=progress2 -e "ssh -i NEW_KEY" /opt/new-api/ root@<新机IP>:/opt/new-api/
# 可选：源机还有 /opt/adp-chat-client-claw 一并搬
rsync -aH -e "ssh -i NEW_KEY" /opt/adp-chat-client-claw/ root@<新机IP>:/opt/adp-chat-client-claw/ 2>/dev/null || true
```
> 这会带上 `deploy/.env`、`deploy/hwdrama-proxy-secrets.env`、`deploy/claw-workbench/secrets/*`、Caddyfile、blue/green override、`backups/` 等，**全量一致保留**。
> ⚠️ 传输后务必校验：`ssh -i NEW_KEY root@<新机IP> 'ls -la /opt/new-api/deploy/.env /opt/new-api/deploy/*.yml /opt/new-api/deploy/claw-workbench/secrets/internal_ca.crt'` 都存在。

---

## 4. 【停机窗口】一致性迁移数据卷 + 数据库

### 4.1 源机停 front（先切断流量，保留 DB 做最终快照）【停机】
```bash
ssh -i BPlatform.pem "$OLD" '
  cd /opt/new-api/deploy
  docker compose -f compose.yml -f compose.blue.override.yml -f compose.green.override.yml stop new-api new-api-green caddy hwdrama-proxy api-docs
  # 此时 postgres/redis 仍运行，可做最终一致备份（可选，见 2.2/2.3）
'
```

### 4.2 最终一致性备份（可选，若 2.x 已做可跳过）
```bash
ssh -i BPlatform.pem "$OLD" '
  docker exec new-api-seedance-postgres-1 pg_dump -U newapi -d newapi -Fc -f /tmp/newapi_final.dump
  docker cp new-api-seedance-postgres-1:/tmp/newapi_final.dump /opt/new-api/backups/newapi_final.dump
  docker exec new-api-seedance-redis-1 redis-cli SAVE
  docker cp new-api-seedance-redis-1:/data/dump.rdb /opt/new-api/backups/redis_final.rdb
'
```

### 4.3 停 DB 并对源机最终 rsync /opt（统一收尾）【停机】
```bash
ssh -i BPlatform.pem "$OLD" '
  cd /opt/new-api/deploy
  docker compose -f compose.yml -f compose.blue.override.yml -f compose.green.override.yml stop
'
# 最后再 rsync 一次，把最终 dump 也带上
rsync -aH --delete -e "ssh -i NEW_KEY" /opt/new-api/ root@<新机IP>:/opt/new-api/
```

### 4.4 迁移 Docker 数据卷（物理拷贝，先在新机装好 Docker 并停止引擎）
> 核心数据卷要恢复到新机 Docker 数据目录。做法：**新机先停 Docker**，把卷目录放到
> `/var/lib/docker/volumes/` 下，再启动 Docker。
```bash
NEWIP="<新机IP>"
# 在 OLD 上把卷目录打 tar（每次选你要的卷；下面是 new-api-seedance 全部）
ssh -i BPlatform.pem "$OLD" '
  cd /var/lib/docker/volumes
  tar -czf /root/vols-newapi.tgz \
     new-api-seedance_postgres_data/ \
     new-api-seedance_redis_data/ \
     new-api-seedance_new_api_data/ \
     new-api-seedance_new_api_logs/ \
     new-api-seedance_caddy_config/ \
     new-api-seedance_caddy_data/
'
# 传到新机
scp -i NEW_KEY /root/vols-newapi.tgz root@$NEWIP:/root/
# 新机：停 docker → 释放卷目录 → 解包 → 起 docker
ssh -i NEW_KEY root@$NEWIP '
  systemctl stop docker
  tar -xzf /root/vols-newapi.tgz -C /var/lib/docker/volumes/
  systemctl start docker
  docker volume ls | grep new-api-seedance
'
```
> claw-workbench 的卷（`claw-workbench_*`）如需一起迁，用同样的 tar 方式多带几个即可
> （`control_db_data`、`adp_db_data`、`evidence_data`、`redis_data`、两个 caddy data/config 等）。

> **备选（更稳的 DB 恢复，二选一即可）：**
> 不用 4.4 物理卷，改在新机 `docker compose up` 起空库后，用 pg_dump 恢复：
> ```bash
> # 新机，等 postgres healthy 后：
> ssh -i NEW_KEY root@$NEWIP '
>   docker exec -i new-api-seedance-postgres-1 pg_restore -U newapi -d newapi \
>     < <(docker exec new-api-seedance-postgres-1 cat /opt/blank)
> ' #（实操：把 newapi_final.dump 拷进容器再 pg_restore）
> ```
> 物理卷方式通常更快更省事，推荐 4.4。

---

## 5. 新机：改配置并启动

### 5.1 更新 Caddyfile 的 IP 绑定【关键】
新机公网 IP 变了，源 Caddyfile 里 `124.174.0.221 { ... }` 这段要改成新机 IP（或删掉只用域名）：
```bash
NEWIP="<新机IP>"; ssh -i NEW_KEY root@$NEWIP "
  sed -i 's/^124\.174\.0\.221 {/$NEWIP {/' /opt/new-api/deploy/Caddyfile
  grep -nE 'gateway\.nexus-reach|^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+ \{' /opt/new-api/deploy/Caddyfile
"
```
> 域名 `gateway.nexus-reach.com` 这段不用改；等 DNS 切到新 IP 后 Caddy 会自动给该域名续/签证书。

### 5.2 启动 new-api-seedance 项目【停机→恢复】
```bash
ssh -i NEW_KEY root@<新机IP> '
  cd /opt/new-api/deploy
  # 与源机一致的配置文件列表
  docker compose -f compose.yml -f compose.blue.override.yml -f compose.green.override.yml up -d
  docker compose -f compose.yml -f compose.blue.override.yml -f compose.green.override.yml ps
'
```

### 5.3 启动 claw-workbench 项目（可选，若需要腾讯侧控）
```bash
ssh -i NEW_KEY root@<新机IP> '
  cd /opt/new-api/deploy/claw-workbench
  docker compose -f compose.yml up -d
  docker compose -f compose.yml ps
'
```
> 若你们暂不需要 claw-workbench：可保持其不启动，但**`secrets/` 目录必须保留**（new-api 挂载）。
> 若它引用了部署脚本里 `.env`、路径（如 `/opt/new-api/dir`），确认 rsync 后路径一致即可。

### 5.4 验证容器健康
```bash
ssh -i NEW_KEY root@<新机IP> '
  docker ps --format "table {{.Names}}\t{{.Status}}" | grep -E "new-api|caddy|postgres|redis"
  curl -s http://127.0.0.1:3000/api/status
'
```

---

## 6. 切换流量（DNS）【关键，切前务必先验证内网可达】

1. 先用**新机公网 IP**临时验证（Host 或直连）：
   ```bash
   curl -k -s -o /dev/null -w "%{http_code}\n" "https://<新机IP>/api/status"
   # 用某个有效 token 验证一下 /v1/models 与一次真实调用
   curl -s "https://<新机IP>/v1/models" -H "Authorization: Bearer sk-xxxx"
   ```
2. 确认 80/443、`/v1/chat/completions` 在**新 IP** 下都正常。
3. 把 `gateway.nexus-reach.com` 的 DNS A 记录改为新机公网 IP（TTL 设短，如 60s，等生效）。
4. 生效后验证：
   ```bash
   curl https://gateway.nexus-reach.com/api/status
   curl https://gateway.nexus-reach.com/v1/models -H "Authorization: Bearer sk-xxxx"
   ```
5. 全部正常后，才停掉源机的容器（源机保留几天作回滚）。

---

## 7. 验证清单（务必逐项过）

- [ ] `docker compose ps` 两个项目都 healthy。
- [ ] `GET https://gateway.nexus-reach.com/api/status` 成功。
- [ ] 登录新机后台（/ 首页），用管理员账号能进、用户/渠道/令牌都在（= PG 数据已迁移）。
- [ ] 用各渠道对应 token 分别调 `claude-opus-5 / claude-sonnet-5 / claude-opus-4-8 / kimi-k3 / Hunyuan/hy3` 验证上游连通（注意源机那几把上游 key 的 ccMax / 许可问题也在新机同样复现，属上游问题，不是迁移问题）。
- [ ] 新机后台“模型定价”面板看到之前那三项 claude 的 ModelRatio 已是新值（验证配置同步）。
- [ ] `gateway.nexus-reach.com` 走域名访问，HTTPS 证书有效（Caddy 已自动续签）。
- [ ] claw-workbench（若需要）各服务 healthy，能连它要连的腾讯/内部端点。

---

## 8. 回滚方案

- 若新机出问题：把 DNS `gateway.nexus-reach.com` 改回**源机公网 IP**，源机容器重新 `up -d` 即可快速回滚（源机在确认稳定前不要格式化）。
- 迁移期间保留的 `backups/newapi_*.dump`、`/root/images.tar`、`vols-newapi.tgz` 都别删，作为灾备。

---

## 9. 常见坑（一定要看）

1. **SESSION_SECRET / POSTGRES_PASSWORD 必须原值保留**：它们在 `/opt/new-api/deploy/.env`，rsync 带过去即可；改了就登录失效/连不上库。
2. **Caddy 证书**：`caddy_data` 卷带上去了，但域名换了 IP 后 Caddy 会自动重新申请；若域名不换就没事。
3. **claw-workbench secrets 不能少**：new-api 容器强挂 `secrets/internal_ca.crt` 等，缺了 new-api 起不来。
4. **compose 配置文件列表**：new-api 项目要显式带上 blue/green override，别只 `up -d` 主文件（否则只起一套）。
5. **上游 403（ccMax）等是上游侧问题**：迁移不会“治好”渠道 6/7 的上游权限问题；迁移后应先验证，不要误以为是迁移失败。
6. **磁盘**：源 300G 已用 63%，新机系统盘给够（建议 500G），并把 `/var/lib/docker/volumes`、`/opt/new-api/backups` 的增速考虑进去。
