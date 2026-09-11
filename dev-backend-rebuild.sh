#!/usr/bin/env bash
# 本地后端一键重新编译 + 重启（利用持久化模块缓存 gomodcache，无需联网）
# 用法: ./dev-backend-rebuild.sh
set -e
cd "$(dirname "$0")"

VER="$(cat VERSION 2>/dev/null || echo dev-local)"
echo ">>> 用缓存 recompile 后端 (version=$VER) ..."
docker run --rm -e CGO_ENABLED=0 -e GOFLAGS=-mod=mod -e GOPROXY=off -e GOSUMDB=off \
  -v gomodcache:/go/pkg/mod \
  -v "$PWD:/src" -v "$PWD/bin-dev:/out" -w /src \
  golang:1.26.1-alpine \
  sh -c "go build -ldflags \"-s -w -X github.com/QuantumNous/new-api/common.Version=$VER\" -o /out/new-api . > /tmp/b.log 2>&1; e=\$?; echo \"exit=\$e\"; tail -20 /tmp/b.log; exit \$e"

echo ">>> 重启后端容器 new-api-dev-run ..."
docker restart new-api-dev-run >/dev/null

sleep 4
echo ">>> 就绪检查: http://localhost:3000/api/status"
curl -s --max-time 8 -o /dev/null -w "backend=%{http_code}\n" http://localhost:3000/api/status
echo "完成。"
