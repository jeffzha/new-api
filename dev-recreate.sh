#!/usr/bin/env bash
# Recreate the local dev containers (new-api-dev-run + agency-hub-dev-run) with
# agency SSO enabled. Binaries must exist under bin-dev/ first (see dev-backend-rebuild.sh).
set -euo pipefail
cd "$(dirname "$0")"

NET=new-api-cn_dev-network

echo ">>> removing existing dev-run containers (data lives in postgres/redis, safe)..."
docker rm -f new-api-dev-run agency-hub-dev-run >/dev/null 2>&1 || true

echo ">>> starting new-api-dev-run (:3000)..."
docker run -d --name new-api-dev-run \
  --restart unless-stopped \
  --network "$NET" \
  -p 3000:3000 \
  -v "$PWD/bin-dev:/app" \
  -v dev_data:/data \
  -w /data \
  -e SQL_DSN=postgresql://root:123456@postgres:5432/new-api \
  -e REDIS_CONN_STRING=redis://redis \
  -e SESSION_COOKIE_SECURE=false \
  -e BATCH_UPDATE_ENABLED=true \
  -e TZ=Asia/Shanghai \
  -e AGENCY_SSO_PRIVATE_KEY_FILE=/app/keys/sso.priv.pem \
  -e AGENCY_SSO_KEY_ID=agency-sso-v1 \
  -e AGENCY_SSO_ALLOWED_ORIGIN=http://127.0.0.1:3201,http://localhost:3201 \
  debian:bookworm-slim /app/new-api

echo ">>> starting agency-hub-dev-run (:3201)..."
docker run -d --name agency-hub-dev-run \
  --restart unless-stopped \
  --network "$NET" \
  -p 3201:3201 \
  -v "$PWD/bin-dev:/app" \
  -e REDIS_CONN_STRING=redis://redis \
  -e AGENCY_PAYOUT_KEY_FILE=/app/keys/payout.key \
  -e AGENCY_HUB_AUTO_MIGRATE=false \
  -e AGENCY_COMMISSION_PROCESSING_ENABLED=false \
  -e AGENCY_HUB_COMMAND_SERVICE_PUBLIC_KEY_FILE=/app/keys/cmdservice.pub.pem \
  -e AGENCY_HUB_COMMAND_SERVICE_PRIVATE_KEY_FILE=/app/keys/cmdservice.priv.pem \
  -e AGENCY_HUB_DELIVERY_KEY_FILE=/app/keys/delivery.key \
  -e AGENCY_HUB_COMMAND_REQUIRE_TLS=false \
  -e AGENCY_WITHDRAWALS_ENABLED=false \
  -e TZ=Asia/Shanghai \
  -e AGENCY_HUB_PORT=3201 \
  -e AGENCY_HUB_PUBLIC_BASE_URL=http://localhost:3201 \
  -e SQL_DSN=postgresql://root:123456@postgres:5432/new-api \
  -e AGENCY_HUB_BASE_PATH=/agency \
  -e AGENCY_HUB_SSO_PUBLIC_KEY_FILE=/app/keys/sso.pub.pem \
  -e AGENCY_HUB_PLATFORM_BASE_URL=http://127.0.0.1:3000 \
  -e AGENCY_HUB_COOKIE_SECURE=false \
  debian:bookworm-slim /app/agency-hub serve

echo ">>> done; waiting for readiness..."
