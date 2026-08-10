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

read_secret PGSQL_PASSWORD /run/secrets/adp_db_password
read_secret REDIS_PASSWORD /run/secrets/redis_password
read_secret SECRET_KEY /run/secrets/adp_session_secret
read_secret WORKBENCH_SERVICE_HMAC_SECRET /run/secrets/adp_control_hmac
read_secret WORKBENCH_USAGE_EVIDENCE_KEY /run/secrets/adp_usage_evidence_key
read_secret WORKBENCH_FILE_LOCATOR_KEY /run/secrets/adp_file_locator_key
read_secret WORKBENCH_FILE_LOCATOR_PREVIOUS_KEYS_JSON /run/secrets/adp_file_locator_previous_keys
read_secret WORKBENCH_WORKSPACE_LOCATOR_KEY /run/secrets/adp_workspace_locator_key
read_secret WORKBENCH_WORKSPACE_LOCATOR_PREVIOUS_KEYS_JSON /run/secrets/adp_workspace_locator_previous_keys
case "${WORKBENCH_FILES_ENABLED:-false}" in
  true)
    read_secret WORKBENCH_FILE_COS_SECRET_ID /run/workbench/file-secrets/cos_secret_id
    read_secret WORKBENCH_FILE_COS_SECRET_KEY /run/workbench/file-secrets/cos_secret_key
    ;;
  false)
    unset WORKBENCH_FILE_COS_SECRET_ID WORKBENCH_FILE_COS_SECRET_KEY
    ;;
  *)
    echo "WORKBENCH_FILES_ENABLED must be true or false" >&2
    exit 1
    ;;
esac

python main.py --check-secret-key
# Workbench SSO uses a one-time ticket in the query string. Caddy skips this
# surface and the ADP process must do the same; application audit logs provide
# secret-free request/Turn correlation instead.
exec sanic app_factory:create_app --factory -H 0.0.0.0 -p 8000 --no-access-logs
