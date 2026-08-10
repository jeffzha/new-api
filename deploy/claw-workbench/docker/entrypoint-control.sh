#!/bin/sh
set -eu

read_secret() {
  variable="$1"
  path="$2"
  if [ ! -r "$path" ]; then
    echo "required secret is not readable: $path" >&2
    exit 1
  fi
  value="$(cat "$path")"
  if [ -z "$value" ]; then
    echo "required secret is empty: $path" >&2
    exit 1
  fi
  export "$variable=$value"
}

read_secret CLAW_DB_PASSWORD /run/secrets/control_db_password
read_secret CLAW_REDIS_PASSWORD /run/secrets/redis_password
read_secret CLAW_ADMIN_TOKEN /run/secrets/claw_admin_token
read_secret WORKBENCH_CONTROL_HMAC_SECRET /run/secrets/new_api_control_hmac
read_secret WORKBENCH_ADP_HMAC_SECRET /run/secrets/adp_control_hmac
read_secret WORKBENCH_SERVICE_HMAC_SECRET /run/secrets/new_api_identity_hmac
read_secret CLAW_EVIDENCE_MASTER_KEY /run/secrets/evidence_master_key

case "${CLAW_TENCENT_BILLING_IMPORT_ENABLED:-false}" in
  true)
    read_secret CLAW_TENCENT_BILLING_SECRET_ID /run/workbench/billing-secrets/tencent_secret_id
    read_secret CLAW_TENCENT_BILLING_SECRET_KEY /run/workbench/billing-secrets/tencent_secret_key
    ;;
  false)
    unset CLAW_TENCENT_BILLING_SECRET_ID CLAW_TENCENT_BILLING_SECRET_KEY
    ;;
  *)
    echo "CLAW_TENCENT_BILLING_IMPORT_ENABLED must be true or false" >&2
    exit 1
    ;;
esac

export CLAW_DB_DSN="host=${CLAW_DB_HOST} port=${CLAW_DB_PORT} user=${CLAW_DB_USER} password=${CLAW_DB_PASSWORD} dbname=${CLAW_DB_NAME} sslmode=disable TimeZone=UTC"
export CLAW_INTERNAL_HMAC_KEYS="${CLAW_NEW_API_SERVICE_NAME}=${WORKBENCH_CONTROL_HMAC_SECRET},${CLAW_ADP_SERVICE_NAME}=${WORKBENCH_ADP_HMAC_SECRET}"
export CLAW_NEW_API_IDENTITY_STATUS_HMAC_SECRET="${WORKBENCH_SERVICE_HMAC_SECRET}"
unset CLAW_DB_PASSWORD WORKBENCH_CONTROL_HMAC_SECRET WORKBENCH_ADP_HMAC_SECRET WORKBENCH_SERVICE_HMAC_SECRET

if [ -d /run/workbench/provider-secrets ]; then
  for path in /run/workbench/provider-secrets/*; do
    [ -f "$path" ] || continue
    name="$(basename "$path")"
    case "$name" in .*) continue ;; esac
    case "$name" in
      WORKBENCH_PROVIDER_*) ;;
      *) echo "provider secret must use the WORKBENCH_PROVIDER_ prefix: $name" >&2; exit 1 ;;
    esac
    case "$name" in
      *[!A-Z0-9_]*) echo "invalid provider secret filename: $name" >&2; exit 1 ;;
    esac
    read_secret "$name" "$path"
  done
fi

exec /app/claw-control
