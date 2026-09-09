#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
adp_entrypoint="$root/docker/entrypoint-adp.sh"
required_secrets="
control_db_password
adp_db_password
redis_password
claw_admin_token
new_api_control_hmac
adp_control_hmac
new_api_identity_hmac
evidence_master_key
provider_vault_master_key
adp_usage_evidence_key
adp_file_locator_key
adp_file_locator_previous_keys.json
adp_workspace_locator_key
adp_workspace_locator_previous_keys.json
adp_connector_token_key
adp_connector_token_previous_keys.json
adp_oauth_state_key
adp_oauth_state_previous_keys.json
adp_session_secret
internal_ca.crt
workbench_control.crt
workbench_control.key
new_api_identity.crt
new_api_identity.key
"

fail() {
  echo "preflight: $*" >&2
  exit 1
}

command -v python3 >/dev/null 2>&1 || fail "python3 is required for strict deployment validation"
command -v base64 >/dev/null 2>&1 || fail "base64 is required for strict deployment validation"
[ -f "$root/.env" ] || fail "copy .env.example to .env and review it"
umask 077
dotenv_exports="$(mktemp)"
trap 'rm -f "$dotenv_exports"' 0
python3 "$root/scripts/strict_dotenv.py" --file "$root/.env" > "$dotenv_exports" \
  || fail "strict dotenv validation failed"
dotenv_tab="$(printf '\t')"
while IFS="$dotenv_tab" read -r dotenv_name dotenv_encoded; do
  [ -n "$dotenv_name" ] || continue
  dotenv_value="$(printf '%s' "$dotenv_encoded" | base64 -d)" \
    || fail "strict dotenv decoder failed for $dotenv_name"
  export "$dotenv_name=$dotenv_value"
done < "$dotenv_exports"
rm -f "$dotenv_exports"
trap - 0
unset dotenv_exports dotenv_tab dotenv_name dotenv_encoded dotenv_value

[ -f "$adp_entrypoint" ] || fail "missing ADP entrypoint"
grep -Fq -- '--no-access-logs' "$adp_entrypoint" \
  || fail "ADP access logs must be disabled because SSO tickets use query strings"

require_exact_assignment() {
  assignment_file="$1"
  assignment_key="$2"
  assignment_expected="$3"
  assignment_count="$(grep -Fxc "$assignment_key=$assignment_expected" "$assignment_file" || true)"
  [ "$assignment_count" -eq 1 ] \
    || fail "$assignment_file must contain exactly one $assignment_key=$assignment_expected assignment"
}

require_positive_integer() {
  value="$1"
  name="$2"
  case "$value" in
    ''|*[!0-9]*|0) fail "$name must be a positive integer" ;;
  esac
}

require_immutable_release_ref() {
  value="$1"
  name="$2"
  printf '%s\n' "$value" \
    | grep -Eq '^.+(@sha256:[0-9a-fA-F]{64}|:[A-Za-z0-9._-]*[0-9a-fA-F]{12,}[A-Za-z0-9._-]*)$' \
    || fail "$name must use a digest or a tag containing at least 12 hexadecimal release characters"
}

require_secure_top_level_file() {
  path="$1"
  name="$(basename "$path")"
  [ -f "$path" ] && [ ! -L "$path" ] \
    || fail "secrets/$name must be a regular non-symlink file"
  size="$(wc -c < "$path" | tr -d ' ')"
  [ "$size" -gt 0 ] && [ "$size" -le 65536 ] \
    || fail "secrets/$name must contain between 1 and 65536 bytes"
  owner_uid="$(stat -c '%u' "$path")"
  mode="$(stat -c '%a' "$path")"
  case "$name" in
    *.crt)
      [ $((0$mode & 022)) -eq 0 ] \
        || fail "secrets/$name must not be group/world writable"
      ;;
    workbench_control.key|new_api_identity.key)
      case "$owner_uid" in 0|10001) ;; *) fail "secrets/$name must be owned by root or container UID 10001" ;; esac
      [ $((0$mode & 077)) -eq 0 ] \
        || fail "secrets/$name must not be group/world accessible"
      ;;
    *)
      [ "$owner_uid" = "10001" ] \
        || fail "secrets/$name must be owned by container UID 10001"
      [ $((0$mode & 077)) -eq 0 ] \
        || fail "secrets/$name must not be group/world accessible"
      ;;
  esac
  [ $((0$mode & 0400)) -ne 0 ] \
    || fail "secrets/$name must be readable by its owner"
}

require_secure_secret_dir() {
  path="$1"
  label="$2"
  required_owner="${3:-root-or-container}"
  [ -d "$path" ] && [ ! -L "$path" ] \
    || fail "$label must be a non-symlink directory"
  owner_uid="$(stat -c '%u' "$path")"
  mode="$(stat -c '%a' "$path")"
  case "$required_owner" in
    container)
      [ "$owner_uid" = "10001" ] \
        || fail "$label must be owned by container UID 10001 so the bind-mounted directory is searchable"
      ;;
    root-or-container)
      case "$owner_uid" in 0|10001) ;; *) fail "$label must be owned by root or container UID 10001" ;; esac
      ;;
    *) fail "internal error: unsupported secret directory owner policy" ;;
  esac
  [ $((0$mode & 077)) -eq 0 ] \
    || fail "$label must not be accessible by group/other"
  [ $((0$mode & 0500)) -eq $((0500)) ] \
    || fail "$label must be owner-readable and searchable"
}

require_secure_secret_dir "$root/secrets" "secrets/"
for directory in provider oauth billing files sandbox; do
  require_secure_secret_dir "$root/secrets/$directory" "secrets/$directory/" container
done

for name in $required_secrets; do
  path="$root/secrets/$name"
  require_secure_top_level_file "$path"
done
provider_secret_count=0
for path in "$root"/secrets/provider/*; do
  [ -f "$path" ] || continue
  name="$(basename "$path")"
  case "$name" in .*) continue ;; esac
  case "$name" in WORKBENCH_PROVIDER_*) ;; *) fail "provider secret $name lacks WORKBENCH_PROVIDER_ prefix" ;; esac
  case "$name" in *[!A-Z0-9_]*) fail "provider secret $name is not a valid uppercase env name" ;; esac
  [ -s "$path" ] || fail "provider secret $name is empty"
  [ "$(wc -c < "$path" | tr -d ' ')" -le 4096 ] || fail "provider secret $name exceeds 4096 bytes"
  owner_uid="$(stat -c '%u' "$path")"
  mode="$(stat -c '%a' "$path")"
  [ "$owner_uid" = "10001" ] || fail "provider secret $name must be owned by container UID 10001"
  [ $((0$mode & 077)) -eq 0 ] || fail "provider secret $name must not be group/world accessible"
  [ $((0$mode & 0400)) -ne 0 ] || fail "provider secret $name must be readable by its owner"
  provider_secret_count=$((provider_secret_count + 1))
done
[ "$provider_secret_count" -gt 0 ] || fail "at least one WORKBENCH_PROVIDER_* secret is required"
python3 "$root/scripts/provider_fingerprint.py" --secrets-dir "$root/secrets/provider" validate \
  || fail "provider secret file validation failed"
python3 "$root/scripts/validate_secret_keys.py" \
  --secrets-dir "$root/secrets" \
  --file-active-kid "${WORKBENCH_FILE_LOCATOR_KEY_ID:-v1}" \
  --workspace-active-kid "${WORKBENCH_WORKSPACE_LOCATOR_KEY_ID:-v1}" \
  --connector-active-kid "${WORKBENCH_CONNECTOR_TOKEN_KEY_ID:-v1}" \
  --oauth-state-active-kid "${WORKBENCH_OAUTH_STATE_KEY_ID:-v1}" \
  || fail "secret key validation failed"
unset owner_uid mode size path name directory label
for name in control_db_password adp_db_password redis_password; do
  python3 "$root/scripts/validate_runtime_secret.py" \
    --minimum-length 16 "$root/secrets/$name" \
    || fail "runtime validation failed for secrets/$name"
done
new_api_control="$(cat "$root/secrets/new_api_control_hmac")"
adp_control="$(cat "$root/secrets/adp_control_hmac")"
identity="$(cat "$root/secrets/new_api_identity_hmac")"
[ "$new_api_control" != "$adp_control" ] || fail "control HMAC secrets must be distinct"
[ "$new_api_control" != "$identity" ] || fail "control and identity HMAC secrets must be distinct"
[ "$adp_control" != "$identity" ] || fail "ADP and identity HMAC secrets must be distinct"
unset new_api_control adp_control identity

control_db_password="$(cat "$root/secrets/control_db_password")"
adp_db_password="$(cat "$root/secrets/adp_db_password")"
redis_password="$(cat "$root/secrets/redis_password")"
[ "$control_db_password" != "$adp_db_password" ] || fail "control and ADP database passwords must be distinct"
[ "$control_db_password" != "$redis_password" ] || fail "control database and Redis passwords must be distinct"
[ "$adp_db_password" != "$redis_password" ] || fail "ADP database and Redis passwords must be distinct"
unset control_db_password adp_db_password redis_password

case "${CLAW_DB_NAME:-}" in *[!A-Za-z0-9_]*) fail "CLAW_DB_NAME contains unsafe characters";; esac
case "${ADP_DB_NAME:-}" in *[!A-Za-z0-9_]*) fail "ADP_DB_NAME contains unsafe characters";; esac
[ "${WORKBENCH_CANONICAL_ORIGIN#https://}" != "$WORKBENCH_CANONICAL_ORIGIN" ] || fail "WORKBENCH_CANONICAL_ORIGIN must use HTTPS"
[ -n "${CLAW_PROVIDER_VERIFICATION_TIMEOUT:-}" ] || fail "CLAW_PROVIDER_VERIFICATION_TIMEOUT is required"
[ "${COMPOSE_PROJECT_NAME:-}" = "claw-workbench" ] || fail "COMPOSE_PROJECT_NAME is fixed to claw-workbench so DR protected volume paths remain authoritative"
case "${CLAW_BUILD_LOCAL_IMAGES:-false}" in true|false) ;; *) fail "CLAW_BUILD_LOCAL_IMAGES must be true or false" ;; esac
if [ "${CLAW_ENVIRONMENT:-prod}" = prod ]; then
  [ "$CLAW_BUILD_LOCAL_IMAGES" = false ] || fail "production cannot build mutable application images on the target host"
  for assignment in \
    "${CLAW_CONTROL_IMAGE_BLUE:-}|CLAW_CONTROL_IMAGE_BLUE" \
    "${CLAW_CONTROL_IMAGE_GREEN:-}|CLAW_CONTROL_IMAGE_GREEN" \
    "${ADP_WORKBENCH_IMAGE_BLUE:-}|ADP_WORKBENCH_IMAGE_BLUE" \
    "${ADP_WORKBENCH_IMAGE_GREEN:-}|ADP_WORKBENCH_IMAGE_GREEN"; do
    printf '%s\n' "${assignment%%|*}" | grep -Eq '^.+@sha256:[0-9a-fA-F]{64}$' \
      || fail "${assignment#*|} must use an immutable registry digest in production"
  done
fi
require_immutable_release_ref "${CLAW_CONTROL_IMAGE_BLUE:-}" "CLAW_CONTROL_IMAGE_BLUE"
require_immutable_release_ref "${CLAW_CONTROL_IMAGE_GREEN:-}" "CLAW_CONTROL_IMAGE_GREEN"
require_immutable_release_ref "${ADP_WORKBENCH_IMAGE_BLUE:-}" "ADP_WORKBENCH_IMAGE_BLUE"
require_immutable_release_ref "${ADP_WORKBENCH_IMAGE_GREEN:-}" "ADP_WORKBENCH_IMAGE_GREEN"
[ "$CLAW_CONTROL_IMAGE_BLUE" != "$CLAW_CONTROL_IMAGE_GREEN" ] \
  || fail "Blue and Green claw-control images must use distinct immutable refs"
[ "$ADP_WORKBENCH_IMAGE_BLUE" != "$ADP_WORKBENCH_IMAGE_GREEN" ] \
  || fail "Blue and Green ADP images must use distinct immutable refs"
if [ "${CLAW_ENVIRONMENT:-prod}" = prod ]; then
  printf '%s\n' "${CLAMAV_IMAGE:-}" \
    | grep -Eq '^.+@sha256:[0-9a-fA-F]{64}$' \
    || fail "production CLAMAV_IMAGE must be pinned to an immutable sha256 digest"
fi
case "${WORKBENCH_FILES_ENABLED:-false}" in
  true|false) ;;
  *) fail "WORKBENCH_FILES_ENABLED must be true or false" ;;
esac
case "${WORKBENCH_APP_MIGRATION_WORKER_ENABLED:-false}" in
  true|false) ;;
  *) fail "WORKBENCH_APP_MIGRATION_WORKER_ENABLED must be true or false" ;;
esac
if [ "${CLAW_ENVIRONMENT:-prod}" = prod ] && [ "${WORKBENCH_APP_MIGRATION_WORKER_ENABLED:-false}" != true ]; then
  fail "production requires WORKBENCH_APP_MIGRATION_WORKER_ENABLED=true so verified migrations cannot stall"
fi
require_positive_integer "${WORKBENCH_APP_MIGRATION_POLL_SECONDS:-}" "WORKBENCH_APP_MIGRATION_POLL_SECONDS"
require_positive_integer "${WORKBENCH_APP_MIGRATION_LEASE_SECONDS:-}" "WORKBENCH_APP_MIGRATION_LEASE_SECONDS"
if [ "${WORKBENCH_APP_MIGRATION_POLL_SECONDS:-0}" -gt 300 ]; then
  fail "WORKBENCH_APP_MIGRATION_POLL_SECONDS must be at most 300"
fi
if [ "${WORKBENCH_APP_MIGRATION_LEASE_SECONDS:-0}" -lt 10 ] || [ "${WORKBENCH_APP_MIGRATION_LEASE_SECONDS:-0}" -gt 120 ]; then
  fail "WORKBENCH_APP_MIGRATION_LEASE_SECONDS must be between 10 and 120"
fi
case "${WORKBENCH_SCHEDULED_TASKS_ENABLED:-false}" in
  true|false) ;;
  *) fail "WORKBENCH_SCHEDULED_TASKS_ENABLED must be true or false" ;;
esac
case "${WORKBENCH_INTEGRATIONS_ENABLED:-false}" in
  true|false) ;;
  *) fail "WORKBENCH_INTEGRATIONS_ENABLED must be true or false" ;;
esac
case "${WORKBENCH_SANDBOX_ENABLED:-false}" in
  true|false) ;;
  *) fail "WORKBENCH_SANDBOX_ENABLED must be true or false" ;;
esac
case "${WORKBENCH_SANDBOX_CODE_ENABLED:-false}" in
  true|false) ;;
  *) fail "WORKBENCH_SANDBOX_CODE_ENABLED must be true or false" ;;
esac
case "${WORKBENCH_SANDBOX_PTY_ENABLED:-false}" in
  true|false) ;;
  *) fail "WORKBENCH_SANDBOX_PTY_ENABLED must be true or false" ;;
esac
if [ "${WORKBENCH_SANDBOX_ENABLED:-false}" != true ] && {
  [ "${WORKBENCH_SANDBOX_CODE_ENABLED:-false}" = true ] ||
  [ "${WORKBENCH_SANDBOX_PTY_ENABLED:-false}" = true ];
}; then
  fail "sandbox code/PTY features require WORKBENCH_SANDBOX_ENABLED=true"
fi
case "${CLAW_TENCENT_BILLING_IMPORT_ENABLED:-false}" in
  true|false) ;;
  *) fail "CLAW_TENCENT_BILLING_IMPORT_ENABLED must be true or false" ;;
esac
for assignment in \
  "${WORKBENCH_FILE_SCANNER_PORT:-}:WORKBENCH_FILE_SCANNER_PORT" \
  "${WORKBENCH_FILE_SCANNER_MAX_BYTES:-}:WORKBENCH_FILE_SCANNER_MAX_BYTES" \
  "${WORKBENCH_FILE_ABSOLUTE_MAX_BYTES:-}:WORKBENCH_FILE_ABSOLUTE_MAX_BYTES" \
  "${WORKBENCH_FILE_QUARANTINE_CAPACITY_BYTES:-}:WORKBENCH_FILE_QUARANTINE_CAPACITY_BYTES" \
  "${WORKBENCH_FILE_MAX_CONCURRENT_UPLOADS:-}:WORKBENCH_FILE_MAX_CONCURRENT_UPLOADS" \
  "${WORKBENCH_FILE_SCANNER_TIMEOUT_SECONDS:-}:WORKBENCH_FILE_SCANNER_TIMEOUT_SECONDS" \
  "${WORKBENCH_FILE_URL_EXPIRE_SECONDS:-}:WORKBENCH_FILE_URL_EXPIRE_SECONDS" \
  "${CLAMAV_STREAM_MAX_BYTES:-}:CLAMAV_STREAM_MAX_BYTES" \
  "${CLAMAV_MAX_FILE_BYTES:-}:CLAMAV_MAX_FILE_BYTES" \
  "${CLAMAV_MAX_SCAN_BYTES:-}:CLAMAV_MAX_SCAN_BYTES"; do
  require_positive_integer "${assignment%%:*}" "${assignment#*:}"
done
require_positive_integer "${CLAW_EVIDENCE_MAX_BYTES:-}" "CLAW_EVIDENCE_MAX_BYTES"
WORKBENCH_SCHEDULE_REAUTH_SECONDS="${WORKBENCH_SCHEDULE_REAUTH_SECONDS:-300}"
WORKBENCH_SCHEDULE_WORKER_INTERVAL_SECONDS="${WORKBENCH_SCHEDULE_WORKER_INTERVAL_SECONDS:-5}"
WORKBENCH_SCHEDULE_LEASE_SECONDS="${WORKBENCH_SCHEDULE_LEASE_SECONDS:-90}"
WORKBENCH_SCHEDULE_BATCH_SIZE="${WORKBENCH_SCHEDULE_BATCH_SIZE:-20}"
WORKBENCH_SCHEDULE_MIN_INTERVAL_MINUTES="${WORKBENCH_SCHEDULE_MIN_INTERVAL_MINUTES:-15}"
WORKBENCH_SCHEDULE_MAX_TASKS_PER_USER="${WORKBENCH_SCHEDULE_MAX_TASKS_PER_USER:-20}"
WORKBENCH_SCHEDULE_MAX_DAILY_RUNS="${WORKBENCH_SCHEDULE_MAX_DAILY_RUNS:-24}"
WORKBENCH_SCHEDULE_MAX_PROMPT_CHARS="${WORKBENCH_SCHEDULE_MAX_PROMPT_CHARS:-16000}"
WORKBENCH_SCHEDULE_MAX_ATTACHMENTS="${WORKBENCH_SCHEDULE_MAX_ATTACHMENTS:-10}"
WORKBENCH_SCHEDULE_DELEGATION_DAYS="${WORKBENCH_SCHEDULE_DELEGATION_DAYS:-30}"
WORKBENCH_SCHEDULE_MISFIRE_GRACE_SECONDS="${WORKBENCH_SCHEDULE_MISFIRE_GRACE_SECONDS:-300}"
for assignment in \
  "$WORKBENCH_SCHEDULE_REAUTH_SECONDS:WORKBENCH_SCHEDULE_REAUTH_SECONDS" \
  "$WORKBENCH_SCHEDULE_WORKER_INTERVAL_SECONDS:WORKBENCH_SCHEDULE_WORKER_INTERVAL_SECONDS" \
  "$WORKBENCH_SCHEDULE_LEASE_SECONDS:WORKBENCH_SCHEDULE_LEASE_SECONDS" \
  "$WORKBENCH_SCHEDULE_BATCH_SIZE:WORKBENCH_SCHEDULE_BATCH_SIZE" \
  "$WORKBENCH_SCHEDULE_MIN_INTERVAL_MINUTES:WORKBENCH_SCHEDULE_MIN_INTERVAL_MINUTES" \
  "$WORKBENCH_SCHEDULE_MAX_TASKS_PER_USER:WORKBENCH_SCHEDULE_MAX_TASKS_PER_USER" \
  "$WORKBENCH_SCHEDULE_MAX_DAILY_RUNS:WORKBENCH_SCHEDULE_MAX_DAILY_RUNS" \
  "$WORKBENCH_SCHEDULE_MAX_PROMPT_CHARS:WORKBENCH_SCHEDULE_MAX_PROMPT_CHARS" \
  "$WORKBENCH_SCHEDULE_MAX_ATTACHMENTS:WORKBENCH_SCHEDULE_MAX_ATTACHMENTS" \
  "$WORKBENCH_SCHEDULE_DELEGATION_DAYS:WORKBENCH_SCHEDULE_DELEGATION_DAYS" \
  "$WORKBENCH_SCHEDULE_MISFIRE_GRACE_SECONDS:WORKBENCH_SCHEDULE_MISFIRE_GRACE_SECONDS"; do
  require_positive_integer "${assignment%%:*}" "${assignment#*:}"
done
[ "$WORKBENCH_SCHEDULE_REAUTH_SECONDS" -le 900 ] || fail "WORKBENCH_SCHEDULE_REAUTH_SECONDS must not exceed 900"
[ "$WORKBENCH_SCHEDULE_WORKER_INTERVAL_SECONDS" -le 60 ] || fail "WORKBENCH_SCHEDULE_WORKER_INTERVAL_SECONDS must not exceed 60"
[ "$WORKBENCH_SCHEDULE_LEASE_SECONDS" -ge 30 ] && [ "$WORKBENCH_SCHEDULE_LEASE_SECONDS" -le 600 ] \
  || fail "WORKBENCH_SCHEDULE_LEASE_SECONDS must be between 30 and 600"
[ "$WORKBENCH_SCHEDULE_BATCH_SIZE" -le 100 ] || fail "WORKBENCH_SCHEDULE_BATCH_SIZE must not exceed 100"
[ "$WORKBENCH_SCHEDULE_MIN_INTERVAL_MINUTES" -ge 5 ] && [ "$WORKBENCH_SCHEDULE_MIN_INTERVAL_MINUTES" -le 1440 ] \
  || fail "WORKBENCH_SCHEDULE_MIN_INTERVAL_MINUTES must be between 5 and 1440"
[ "$WORKBENCH_SCHEDULE_MAX_TASKS_PER_USER" -le 100 ] || fail "WORKBENCH_SCHEDULE_MAX_TASKS_PER_USER must not exceed 100"
[ "$WORKBENCH_SCHEDULE_MAX_DAILY_RUNS" -le 96 ] || fail "WORKBENCH_SCHEDULE_MAX_DAILY_RUNS must not exceed 96"
[ "$WORKBENCH_SCHEDULE_MAX_PROMPT_CHARS" -le 100000 ] || fail "WORKBENCH_SCHEDULE_MAX_PROMPT_CHARS must not exceed 100000"
[ "$WORKBENCH_SCHEDULE_MAX_ATTACHMENTS" -le 32 ] || fail "WORKBENCH_SCHEDULE_MAX_ATTACHMENTS must not exceed 32"
[ "$WORKBENCH_SCHEDULE_DELEGATION_DAYS" -le 90 ] || fail "WORKBENCH_SCHEDULE_DELEGATION_DAYS must not exceed 90"
[ "$WORKBENCH_SCHEDULE_MISFIRE_GRACE_SECONDS" -le 86400 ] || fail "WORKBENCH_SCHEDULE_MISFIRE_GRACE_SECONDS must not exceed 86400"
WORKBENCH_OAUTH_STATE_TTL_SECONDS="${WORKBENCH_OAUTH_STATE_TTL_SECONDS:-300}"
WORKBENCH_INTEGRATION_REAUTH_SECONDS="${WORKBENCH_INTEGRATION_REAUTH_SECONDS:-300}"
WORKBENCH_OAUTH_HTTP_TIMEOUT_SECONDS="${WORKBENCH_OAUTH_HTTP_TIMEOUT_SECONDS:-15}"
WORKBENCH_OAUTH_MAX_RESPONSE_BYTES="${WORKBENCH_OAUTH_MAX_RESPONSE_BYTES:-262144}"
for assignment in \
  "$WORKBENCH_OAUTH_STATE_TTL_SECONDS:WORKBENCH_OAUTH_STATE_TTL_SECONDS" \
  "$WORKBENCH_INTEGRATION_REAUTH_SECONDS:WORKBENCH_INTEGRATION_REAUTH_SECONDS" \
  "$WORKBENCH_OAUTH_HTTP_TIMEOUT_SECONDS:WORKBENCH_OAUTH_HTTP_TIMEOUT_SECONDS" \
  "$WORKBENCH_OAUTH_MAX_RESPONSE_BYTES:WORKBENCH_OAUTH_MAX_RESPONSE_BYTES"; do
  require_positive_integer "${assignment%%:*}" "${assignment#*:}"
done
[ "$WORKBENCH_OAUTH_STATE_TTL_SECONDS" -le 900 ] || fail "WORKBENCH_OAUTH_STATE_TTL_SECONDS must not exceed 900"
[ "$WORKBENCH_INTEGRATION_REAUTH_SECONDS" -le 900 ] || fail "WORKBENCH_INTEGRATION_REAUTH_SECONDS must not exceed 900"
[ "$WORKBENCH_OAUTH_HTTP_TIMEOUT_SECONDS" -le 60 ] || fail "WORKBENCH_OAUTH_HTTP_TIMEOUT_SECONDS must not exceed 60"
[ "$WORKBENCH_OAUTH_MAX_RESPONSE_BYTES" -le 1048576 ] || fail "WORKBENCH_OAUTH_MAX_RESPONSE_BYTES must not exceed 1048576"
python3 "$root/scripts/validate_integration_config.py" \
  --allowlist "$root/config/integration-allowlist.json" \
  --providers "$root/config/oauth-providers.json" \
  --secret-dir "$root/secrets/oauth" \
  --enabled "${WORKBENCH_INTEGRATIONS_ENABLED:-false}" \
  || fail "integration configuration validation failed"
WORKBENCH_SANDBOX_LEASE_SECONDS="${WORKBENCH_SANDBOX_LEASE_SECONDS:-30}"
WORKBENCH_SANDBOX_STARTS_PER_USER_MINUTE="${WORKBENCH_SANDBOX_STARTS_PER_USER_MINUTE:-6}"
WORKBENCH_SANDBOX_STARTS_PER_CUSTOMER_MINUTE="${WORKBENCH_SANDBOX_STARTS_PER_CUSTOMER_MINUTE:-60}"
WORKBENCH_SANDBOX_TERMINAL_RETENTION_DAYS="${WORKBENCH_SANDBOX_TERMINAL_RETENTION_DAYS:-30}"
WORKBENCH_SANDBOX_DEFAULT_TIMEOUT_SECONDS="${WORKBENCH_SANDBOX_DEFAULT_TIMEOUT_SECONDS:-600}"
WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS="${WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS:-3600}"
WORKBENCH_SANDBOX_MAX_COMMAND_SECONDS="${WORKBENCH_SANDBOX_MAX_COMMAND_SECONDS:-120}"
WORKBENCH_SANDBOX_PROVIDER_TIMEOUT_SECONDS="${WORKBENCH_SANDBOX_PROVIDER_TIMEOUT_SECONDS:-30}"
WORKBENCH_SANDBOX_MAX_CODE_CHARS="${WORKBENCH_SANDBOX_MAX_CODE_CHARS:-65536}"
WORKBENCH_SANDBOX_MAX_COMMAND_CHARS="${WORKBENCH_SANDBOX_MAX_COMMAND_CHARS:-8192}"
WORKBENCH_SANDBOX_MAX_OUTPUT_BYTES="${WORKBENCH_SANDBOX_MAX_OUTPUT_BYTES:-1048576}"
WORKBENCH_SANDBOX_HARD_MAX_FILE_BYTES="${WORKBENCH_SANDBOX_HARD_MAX_FILE_BYTES:-10485760}"
WORKBENCH_SANDBOX_CODE_WORKER_MEMORY_BYTES="${WORKBENCH_SANDBOX_CODE_WORKER_MEMORY_BYTES:-536870912}"
WORKBENCH_SANDBOX_PTY_TICKET_TTL_SECONDS="${WORKBENCH_SANDBOX_PTY_TICKET_TTL_SECONDS:-30}"
WORKBENCH_SANDBOX_PTY_MAX_RUNTIME_SECONDS="${WORKBENCH_SANDBOX_PTY_MAX_RUNTIME_SECONDS:-600}"
WORKBENCH_SANDBOX_PTY_REAUTH_SECONDS="${WORKBENCH_SANDBOX_PTY_REAUTH_SECONDS:-15}"
WORKBENCH_SANDBOX_PTY_HEARTBEAT_SECONDS="${WORKBENCH_SANDBOX_PTY_HEARTBEAT_SECONDS:-10}"
WORKBENCH_SANDBOX_PTY_STALE_SECONDS="${WORKBENCH_SANDBOX_PTY_STALE_SECONDS:-45}"
WORKBENCH_SANDBOX_PTY_REAPER_SECONDS="${WORKBENCH_SANDBOX_PTY_REAPER_SECONDS:-15}"
WORKBENCH_SANDBOX_PTY_GLOBAL_CAPACITY="${WORKBENCH_SANDBOX_PTY_GLOBAL_CAPACITY:-100}"
WORKBENCH_SANDBOX_PTY_CUSTOMER_CAPACITY="${WORKBENCH_SANDBOX_PTY_CUSTOMER_CAPACITY:-10}"
WORKBENCH_SANDBOX_PTY_USER_CAPACITY="${WORKBENCH_SANDBOX_PTY_USER_CAPACITY:-2}"
WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAME_BYTES="${WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAME_BYTES:-16384}"
WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_FRAME_BYTES="${WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_FRAME_BYTES:-32768}"
WORKBENCH_SANDBOX_PTY_MAX_INPUT_BYTES="${WORKBENCH_SANDBOX_PTY_MAX_INPUT_BYTES:-1048576}"
WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_BYTES="${WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_BYTES:-8388608}"
WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAMES_PER_SECOND="${WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAMES_PER_SECOND:-30}"
WORKBENCH_SANDBOX_PTY_OUTPUT_QUEUE_FRAMES="${WORKBENCH_SANDBOX_PTY_OUTPUT_QUEUE_FRAMES:-32}"
for assignment in \
  "$WORKBENCH_SANDBOX_LEASE_SECONDS:WORKBENCH_SANDBOX_LEASE_SECONDS" \
  "$WORKBENCH_SANDBOX_STARTS_PER_USER_MINUTE:WORKBENCH_SANDBOX_STARTS_PER_USER_MINUTE" \
  "$WORKBENCH_SANDBOX_STARTS_PER_CUSTOMER_MINUTE:WORKBENCH_SANDBOX_STARTS_PER_CUSTOMER_MINUTE" \
  "$WORKBENCH_SANDBOX_TERMINAL_RETENTION_DAYS:WORKBENCH_SANDBOX_TERMINAL_RETENTION_DAYS" \
  "$WORKBENCH_SANDBOX_DEFAULT_TIMEOUT_SECONDS:WORKBENCH_SANDBOX_DEFAULT_TIMEOUT_SECONDS" \
  "$WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS:WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS" \
  "$WORKBENCH_SANDBOX_MAX_COMMAND_SECONDS:WORKBENCH_SANDBOX_MAX_COMMAND_SECONDS" \
  "$WORKBENCH_SANDBOX_PROVIDER_TIMEOUT_SECONDS:WORKBENCH_SANDBOX_PROVIDER_TIMEOUT_SECONDS" \
  "$WORKBENCH_SANDBOX_MAX_CODE_CHARS:WORKBENCH_SANDBOX_MAX_CODE_CHARS" \
  "$WORKBENCH_SANDBOX_MAX_COMMAND_CHARS:WORKBENCH_SANDBOX_MAX_COMMAND_CHARS" \
  "$WORKBENCH_SANDBOX_MAX_OUTPUT_BYTES:WORKBENCH_SANDBOX_MAX_OUTPUT_BYTES" \
  "$WORKBENCH_SANDBOX_HARD_MAX_FILE_BYTES:WORKBENCH_SANDBOX_HARD_MAX_FILE_BYTES"; do
  require_positive_integer "${assignment%%:*}" "${assignment#*:}"
done
for assignment in \
  "$WORKBENCH_SANDBOX_CODE_WORKER_MEMORY_BYTES:WORKBENCH_SANDBOX_CODE_WORKER_MEMORY_BYTES" \
  "$WORKBENCH_SANDBOX_PTY_TICKET_TTL_SECONDS:WORKBENCH_SANDBOX_PTY_TICKET_TTL_SECONDS" \
  "$WORKBENCH_SANDBOX_PTY_MAX_RUNTIME_SECONDS:WORKBENCH_SANDBOX_PTY_MAX_RUNTIME_SECONDS" \
  "$WORKBENCH_SANDBOX_PTY_REAUTH_SECONDS:WORKBENCH_SANDBOX_PTY_REAUTH_SECONDS" \
  "$WORKBENCH_SANDBOX_PTY_HEARTBEAT_SECONDS:WORKBENCH_SANDBOX_PTY_HEARTBEAT_SECONDS" \
  "$WORKBENCH_SANDBOX_PTY_STALE_SECONDS:WORKBENCH_SANDBOX_PTY_STALE_SECONDS" \
  "$WORKBENCH_SANDBOX_PTY_REAPER_SECONDS:WORKBENCH_SANDBOX_PTY_REAPER_SECONDS" \
  "$WORKBENCH_SANDBOX_PTY_GLOBAL_CAPACITY:WORKBENCH_SANDBOX_PTY_GLOBAL_CAPACITY" \
  "$WORKBENCH_SANDBOX_PTY_CUSTOMER_CAPACITY:WORKBENCH_SANDBOX_PTY_CUSTOMER_CAPACITY" \
  "$WORKBENCH_SANDBOX_PTY_USER_CAPACITY:WORKBENCH_SANDBOX_PTY_USER_CAPACITY" \
  "$WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAME_BYTES:WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAME_BYTES" \
  "$WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_FRAME_BYTES:WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_FRAME_BYTES" \
  "$WORKBENCH_SANDBOX_PTY_MAX_INPUT_BYTES:WORKBENCH_SANDBOX_PTY_MAX_INPUT_BYTES" \
  "$WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_BYTES:WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_BYTES" \
  "$WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAMES_PER_SECOND:WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAMES_PER_SECOND" \
  "$WORKBENCH_SANDBOX_PTY_OUTPUT_QUEUE_FRAMES:WORKBENCH_SANDBOX_PTY_OUTPUT_QUEUE_FRAMES"; do
  require_positive_integer "${assignment%%:*}" "${assignment#*:}"
done
[ "$WORKBENCH_SANDBOX_LEASE_SECONDS" -ge 15 ] && [ "$WORKBENCH_SANDBOX_LEASE_SECONDS" -le 300 ] \
  || fail "WORKBENCH_SANDBOX_LEASE_SECONDS must be between 15 and 300"
[ "$WORKBENCH_SANDBOX_STARTS_PER_USER_MINUTE" -le 600 ] \
  || fail "WORKBENCH_SANDBOX_STARTS_PER_USER_MINUTE must not exceed 600"
[ "$WORKBENCH_SANDBOX_STARTS_PER_CUSTOMER_MINUTE" -le 6000 ] \
  || fail "WORKBENCH_SANDBOX_STARTS_PER_CUSTOMER_MINUTE must not exceed 6000"
[ "$WORKBENCH_SANDBOX_TERMINAL_RETENTION_DAYS" -le 3650 ] \
  || fail "WORKBENCH_SANDBOX_TERMINAL_RETENTION_DAYS must not exceed 3650"
[ "$WORKBENCH_SANDBOX_DEFAULT_TIMEOUT_SECONDS" -ge 30 ] && [ "$WORKBENCH_SANDBOX_DEFAULT_TIMEOUT_SECONDS" -le 86400 ] \
  || fail "WORKBENCH_SANDBOX_DEFAULT_TIMEOUT_SECONDS must be between 30 and 86400"
[ "$WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS" -ge 30 ] && [ "$WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS" -le 86400 ] \
  || fail "WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS must be between 30 and 86400"
[ "$WORKBENCH_SANDBOX_DEFAULT_TIMEOUT_SECONDS" -le "$WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS" ] \
  || fail "WORKBENCH_SANDBOX_DEFAULT_TIMEOUT_SECONDS must not exceed the hard runtime maximum"
[ "$WORKBENCH_SANDBOX_MAX_COMMAND_SECONDS" -le 3600 ] && [ "$WORKBENCH_SANDBOX_MAX_COMMAND_SECONDS" -le "$WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS" ] \
  || fail "WORKBENCH_SANDBOX_MAX_COMMAND_SECONDS exceeds its hard ceiling"
[ "$WORKBENCH_SANDBOX_PROVIDER_TIMEOUT_SECONDS" -le 300 ] || fail "WORKBENCH_SANDBOX_PROVIDER_TIMEOUT_SECONDS must not exceed 300"
[ "$WORKBENCH_SANDBOX_MAX_CODE_CHARS" -le 1048576 ] || fail "WORKBENCH_SANDBOX_MAX_CODE_CHARS must not exceed 1048576"
[ "$WORKBENCH_SANDBOX_MAX_COMMAND_CHARS" -le 65536 ] || fail "WORKBENCH_SANDBOX_MAX_COMMAND_CHARS must not exceed 65536"
[ "$WORKBENCH_SANDBOX_MAX_OUTPUT_BYTES" -le 16777216 ] || fail "WORKBENCH_SANDBOX_MAX_OUTPUT_BYTES must not exceed 16777216"
[ "$WORKBENCH_SANDBOX_HARD_MAX_FILE_BYTES" -le 104857600 ] || fail "WORKBENCH_SANDBOX_HARD_MAX_FILE_BYTES must not exceed 104857600"
[ "$WORKBENCH_SANDBOX_CODE_WORKER_MEMORY_BYTES" -ge 134217728 ] && [ "$WORKBENCH_SANDBOX_CODE_WORKER_MEMORY_BYTES" -le 2147483648 ] \
  || fail "WORKBENCH_SANDBOX_CODE_WORKER_MEMORY_BYTES must be between 134217728 and 2147483648"
[ "$WORKBENCH_SANDBOX_PTY_TICKET_TTL_SECONDS" -le 60 ] || fail "WORKBENCH_SANDBOX_PTY_TICKET_TTL_SECONDS must not exceed 60"
[ "$WORKBENCH_SANDBOX_PTY_MAX_RUNTIME_SECONDS" -le 3600 ] && [ "$WORKBENCH_SANDBOX_PTY_MAX_RUNTIME_SECONDS" -le "$WORKBENCH_SANDBOX_HARD_MAX_RUNTIME_SECONDS" ] \
  || fail "WORKBENCH_SANDBOX_PTY_MAX_RUNTIME_SECONDS exceeds its hard ceiling"
[ "$WORKBENCH_SANDBOX_PTY_HEARTBEAT_SECONDS" -lt "$WORKBENCH_SANDBOX_PTY_STALE_SECONDS" ] \
  || fail "WORKBENCH_SANDBOX_PTY_HEARTBEAT_SECONDS must be less than stale timeout"
[ "$WORKBENCH_SANDBOX_PTY_USER_CAPACITY" -le "$WORKBENCH_SANDBOX_PTY_CUSTOMER_CAPACITY" ] && \
  [ "$WORKBENCH_SANDBOX_PTY_CUSTOMER_CAPACITY" -le "$WORKBENCH_SANDBOX_PTY_GLOBAL_CAPACITY" ] \
  || fail "PTY capacity must satisfy user <= customer <= global"
[ "$WORKBENCH_SANDBOX_PTY_MAX_INPUT_FRAME_BYTES" -le "$WORKBENCH_SANDBOX_PTY_MAX_INPUT_BYTES" ] && \
  [ "$WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_FRAME_BYTES" -le "$WORKBENCH_SANDBOX_PTY_MAX_OUTPUT_BYTES" ] \
  || fail "PTY frame limits must not exceed aggregate byte limits"
python3 "$root/scripts/validate_sandbox_config.py" \
  --enabled "${WORKBENCH_SANDBOX_ENABLED:-false}" \
  --provider "${WORKBENCH_SANDBOX_PROVIDER:-tencent_agsx}" \
  --region "${WORKBENCH_AGSX_REGION:-ap-guangzhou}" \
  --domain "${WORKBENCH_AGSX_DOMAIN:-ap-guangzhou.tencentags.com}" \
  --control-endpoint "${WORKBENCH_AGSX_CONTROL_ENDPOINT:-ags.tencentcloudapi.com}" \
  --tool-id "${WORKBENCH_AGSX_TOOL_ID:-}" \
  --tool-name "${WORKBENCH_AGSX_TOOL_NAME:-}" \
  --network-mode "${WORKBENCH_SANDBOX_NETWORK_MODE:-SANDBOX}" \
  --auth-mode "${WORKBENCH_SANDBOX_AUTH_MODE:-TOKEN}" \
  --secret-dir "$root/secrets/sandbox" \
  || fail "managed sandbox configuration validation failed"
billing_page_size="${CLAW_TENCENT_BILLING_PAGE_SIZE:-300}"
billing_max_pages="${CLAW_TENCENT_BILLING_MAX_PAGES:-100}"
billing_max_records="${CLAW_TENCENT_BILLING_MAX_RECORDS:-30000}"
billing_max_attempts="${CLAW_TENCENT_BILLING_MAX_ATTEMPTS:-3}"
billing_max_response_bytes="${CLAW_TENCENT_BILLING_MAX_RESPONSE_BYTES:-4194304}"
for assignment in \
  "$billing_page_size:CLAW_TENCENT_BILLING_PAGE_SIZE" \
  "$billing_max_pages:CLAW_TENCENT_BILLING_MAX_PAGES" \
  "$billing_max_records:CLAW_TENCENT_BILLING_MAX_RECORDS" \
  "$billing_max_attempts:CLAW_TENCENT_BILLING_MAX_ATTEMPTS" \
  "$billing_max_response_bytes:CLAW_TENCENT_BILLING_MAX_RESPONSE_BYTES"; do
  require_positive_integer "${assignment%%:*}" "${assignment#*:}"
done
[ "$billing_page_size" -le 300 ] || fail "CLAW_TENCENT_BILLING_PAGE_SIZE must not exceed Tencent's 300-row limit"
[ "$billing_max_pages" -le 1000 ] || fail "CLAW_TENCENT_BILLING_MAX_PAGES must not exceed 1000"
[ "$billing_max_records" -ge "$billing_page_size" ] && [ "$billing_max_records" -le 200000 ] \
  || fail "CLAW_TENCENT_BILLING_MAX_RECORDS must cover one page and not exceed 200000"
[ "$billing_max_attempts" -le 10 ] || fail "CLAW_TENCENT_BILLING_MAX_ATTEMPTS must not exceed 10"
[ "$billing_max_response_bytes" -ge 1024 ] && [ "$billing_max_response_bytes" -le 16777216 ] \
  || fail "CLAW_TENCENT_BILLING_MAX_RESPONSE_BYTES must be between 1024 and 16777216"
if [ "$CLAW_TENCENT_BILLING_IMPORT_ENABLED" = true ]; then
  case "${CLAW_TENCENT_BILLING_PAYER_UIN:-}" in ''|*[!0-9]*) fail "CLAW_TENCENT_BILLING_PAYER_UIN must be numeric when import is enabled" ;; esac
  python3 "$root/scripts/validate_runtime_secret.py" \
    --minimum-length 8 "$root/secrets/billing/tencent_secret_id" \
    || fail "runtime validation failed for secrets/billing/tencent_secret_id"
  python3 "$root/scripts/validate_runtime_secret.py" \
    --minimum-length 16 "$root/secrets/billing/tencent_secret_key" \
    || fail "runtime validation failed for secrets/billing/tencent_secret_key"
  billing_secret_id="$(cat "$root/secrets/billing/tencent_secret_id")"
  billing_secret_key="$(cat "$root/secrets/billing/tencent_secret_key")"
  [ "$billing_secret_id" != "$billing_secret_key" ] || fail "Tencent Billing SecretId and SecretKey must be distinct"
  unset billing_secret_id billing_secret_key
fi
[ "$WORKBENCH_FILE_SCANNER_PORT" -le 65535 ] \
  || fail "WORKBENCH_FILE_SCANNER_PORT must not exceed 65535"
[ "$WORKBENCH_FILE_SCANNER_TIMEOUT_SECONDS" -le 120 ] \
  || fail "WORKBENCH_FILE_SCANNER_TIMEOUT_SECONDS must not exceed 120"
[ "$WORKBENCH_FILE_URL_EXPIRE_SECONDS" -ge 30 ] && [ "$WORKBENCH_FILE_URL_EXPIRE_SECONDS" -le 900 ] \
  || fail "WORKBENCH_FILE_URL_EXPIRE_SECONDS must be between 30 and 900"
[ "$WORKBENCH_FILE_ABSOLUTE_MAX_BYTES" -le 52428800 ] \
  || fail "WORKBENCH_FILE_ABSOLUTE_MAX_BYTES must not exceed 52428800"
[ "$WORKBENCH_FILE_MAX_CONCURRENT_UPLOADS" -le 16 ] \
  || fail "WORKBENCH_FILE_MAX_CONCURRENT_UPLOADS must not exceed 16"
[ "$WORKBENCH_FILE_SCANNER_MAX_BYTES" -ge "$WORKBENCH_FILE_ABSOLUTE_MAX_BYTES" ] \
  || fail "scanner max bytes must cover the process file ceiling"
[ "$CLAMAV_STREAM_MAX_BYTES" -ge "$WORKBENCH_FILE_ABSOLUTE_MAX_BYTES" ] \
  || fail "ClamAV StreamMaxLength must cover the process file ceiling"
[ "$CLAMAV_MAX_FILE_BYTES" -ge "$WORKBENCH_FILE_ABSOLUTE_MAX_BYTES" ] \
  || fail "ClamAV MaxFileSize must cover the process file ceiling"
[ "$CLAMAV_MAX_SCAN_BYTES" -ge "$WORKBENCH_FILE_ABSOLUTE_MAX_BYTES" ] \
  || fail "ClamAV MaxScanSize must cover the process file ceiling"
[ "$CLAMAV_STREAM_MAX_BYTES" -ge "$CLAW_EVIDENCE_MAX_BYTES" ] \
  || fail "ClamAV StreamMaxLength must cover the evidence file ceiling"
[ "$CLAMAV_MAX_FILE_BYTES" -ge "$CLAW_EVIDENCE_MAX_BYTES" ] \
  || fail "ClamAV MaxFileSize must cover the evidence file ceiling"
[ "$CLAMAV_MAX_SCAN_BYTES" -ge "$CLAW_EVIDENCE_MAX_BYTES" ] \
  || fail "ClamAV MaxScanSize must cover the evidence file ceiling"
[ -n "${CLAW_EVIDENCE_CLAMAV_TIMEOUT:-}" ] || fail "CLAW_EVIDENCE_CLAMAV_TIMEOUT is required"
required_quarantine_bytes=$((WORKBENCH_FILE_ABSOLUTE_MAX_BYTES * WORKBENCH_FILE_MAX_CONCURRENT_UPLOADS))
[ "$WORKBENCH_FILE_QUARANTINE_CAPACITY_BYTES" -ge "$required_quarantine_bytes" ] \
  || fail "quarantine capacity must cover absolute max bytes multiplied by concurrent uploads"
[ "$WORKBENCH_FILE_QUARANTINE_CAPACITY_BYTES" -le 104857600 ] \
  || fail "declared usable quarantine capacity exceeds the fixed 128 MiB tmpfs safety budget"
if [ "$WORKBENCH_FILES_ENABLED" = true ]; then
  [ -n "${WORKBENCH_FILE_COS_REGION:-}" ] || fail "WORKBENCH_FILE_COS_REGION is required when files are enabled"
  [ -n "${WORKBENCH_FILE_COS_BUCKET:-}" ] || fail "WORKBENCH_FILE_COS_BUCKET is required when files are enabled"
  [ -n "${WORKBENCH_WORKSPACE_HOST_SUFFIXES:-}" ] || fail "WORKBENCH_WORKSPACE_HOST_SUFFIXES is required when files are enabled"
  for name in cos_secret_id cos_secret_key; do
    require_secure_top_level_file "$root/secrets/files/$name"
  done
  python3 "$root/scripts/validate_runtime_secret.py" \
    --minimum-length 8 "$root/secrets/files/cos_secret_id" \
    || fail "runtime validation failed for secrets/files/cos_secret_id"
  python3 "$root/scripts/validate_runtime_secret.py" \
    --minimum-length 16 "$root/secrets/files/cos_secret_key" \
    || fail "runtime validation failed for secrets/files/cos_secret_key"
fi
if [ -n "${WORKBENCH_WORKSPACE_HOST_SUFFIXES:-}" ]; then
  old_ifs="$IFS"
  IFS=','
  for suffix in $WORKBENCH_WORKSPACE_HOST_SUFFIXES; do
    case "$suffix" in ''|.*|*..*|*[!A-Za-z0-9.-]*) fail "WORKBENCH_WORKSPACE_HOST_SUFFIXES contains an unsafe suffix" ;; esac
    case "$suffix" in *.*) ;; *) fail "each WORKBENCH_WORKSPACE_HOST_SUFFIXES entry must contain at least one dot" ;; esac
    printf '%s' "$suffix" | grep -Eq '^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$' \
      || fail "WORKBENCH_WORKSPACE_HOST_SUFFIXES contains an unsafe suffix"
    if printf '%s' "$suffix" | grep -Eq '(^|\.)-|-($|\.)'; then
      fail "WORKBENCH_WORKSPACE_HOST_SUFFIXES labels must not start or end with a hyphen"
    fi
  done
  IFS="$old_ifs"
fi
case "${NEW_API_INTERNAL_UPSTREAM:-}" in
  http://REPLACE_*|*example.invalid*|"") fail "set NEW_API_INTERNAL_UPSTREAM to the real private new-api upstream" ;;
esac
[ -d "$root/$ADP_SOURCE_DIR/server" ] || [ -d "$ADP_SOURCE_DIR/server" ] || fail "ADP_SOURCE_DIR does not contain server/"

new_api_contract="$root/new-api.env.example"
require_exact_assignment "$new_api_contract" WORKBENCH_CONTROL_URL https://workbench-control.internal:8443
require_exact_assignment "$new_api_contract" WORKBENCH_ALLOW_INSECURE_CONTROL_HTTP false
require_exact_assignment "$new_api_contract" SSL_CERT_FILE /run/secrets/internal_ca
if grep -Eq '^WORKBENCH_CONTROL_URL=http://|^WORKBENCH_ALLOW_INSECURE_CONTROL_HTTP=true$' "$new_api_contract"; then
  fail "new-api workbench control transport must never permit plaintext HTTP"
fi

compose_source="$root/compose.yml"
if grep -Eq '^[[:space:]]+ports:' "$compose_source"; then
  fail "workbench overlay must not publish any container port to the host"
fi
[ "$(grep -Fxc '  CLAW_METRICS_ADDR: :9090' "$compose_source" || true)" -eq 1 ] \
  || fail "claw-control metrics must use exactly one internal-only :9090 listener"
[ "$(grep -Fxc '  WORKBENCH_METRICS_ENABLED: "true"' "$compose_source" || true)" -eq 1 ] \
  || fail "ADP internal metrics must be explicitly enabled once in the overlay"
[ "$(grep -Fxc '  WORKBENCH_METRICS_HOST: 0.0.0.0' "$compose_source" || true)" -eq 1 ] \
  || fail "ADP metrics host must be declared once inside the container network"
[ "$(grep -Fxc '  WORKBENCH_METRICS_PORT: "9100"' "$compose_source" || true)" -eq 1 ] \
  || fail "ADP metrics port must be declared once as 9100"
blue_instance_count="$(grep -Fxc '      WORKBENCH_INSTANCE_ID: adp-blue' "$compose_source" || true)"
green_instance_count="$(grep -Fxc '      WORKBENCH_INSTANCE_ID: adp-green' "$compose_source" || true)"
[ "$blue_instance_count" -eq 1 ] && [ "$green_instance_count" -eq 1 ] \
  || fail "ADP Blue/Green must have distinct stable WORKBENCH_INSTANCE_ID values"
event_channel_count="$(grep -Fxc '  WORKBENCH_CONTROL_EVENT_CHANNEL: ${CLAW_REDIS_EVENT_CHANNEL:-claw:control:events}' "$compose_source" || true)"
[ "$event_channel_count" -eq 1 ] \
  || fail "ADP and control must share CLAW_REDIS_EVENT_CHANNEL"
grep -Fq '  workbench-clamav:' "$compose_source" \
  || fail "compose must include the internal ClamAV scanner"
grep -Fq '      test: ["CMD", "clamdcheck.sh"]' "$compose_source" \
  || fail "ClamAV must expose a clamd PING/database-ready healthcheck"
quarantine_mount_count="$(grep -Fc '      - /var/lib/workbench-quarantine:rw,noexec,nosuid,nodev,size=128m,uid=10001,gid=10001,mode=0700' "$compose_source" || true)"
[ "$quarantine_mount_count" -eq 2 ] \
  || fail "both ADP colors must use the isolated 128 MiB quarantine tmpfs"
sandbox_mount_count="$(grep -Fxc '      - ./secrets/sandbox:/run/secrets/workbench-sandbox:ro' "$compose_source" || true)"
[ "$sandbox_mount_count" -eq 2 ] \
  || fail "both ADP colors must mount the same read-only managed-sandbox secret directory"
file_secret_mount_count="$(grep -Fxc '      - ./secrets/files:/run/workbench/file-secrets:ro' "$compose_source" || true)"
[ "$file_secret_mount_count" -eq 2 ] \
  || fail "both ADP colors must mount the same read-only optional file credential directory"
grep -Fq 'read_secret WORKBENCH_FILE_COS_SECRET_ID /run/workbench/file-secrets/cos_secret_id' "$root/docker/entrypoint-adp.sh" \
  || fail "ADP entrypoint must load the dedicated workbench COS secret id"
grep -Fq 'read_secret WORKBENCH_FILE_COS_SECRET_KEY /run/workbench/file-secrets/cos_secret_key' "$root/docker/entrypoint-adp.sh" \
  || fail "ADP entrypoint must load the dedicated workbench COS secret key"
grep -Fq 'read_secret WORKBENCH_USAGE_EVIDENCE_KEY /run/secrets/adp_usage_evidence_key' "$root/docker/entrypoint-adp.sh" \
  || fail "ADP entrypoint must load the independent usage evidence key"
grep -Fq 'read_secret WORKBENCH_FILE_LOCATOR_KEY /run/secrets/adp_file_locator_key' "$root/docker/entrypoint-adp.sh" \
  || fail "ADP entrypoint must load the independent private file locator key"
grep -Fq 'read_secret WORKBENCH_WORKSPACE_LOCATOR_KEY /run/secrets/adp_workspace_locator_key' "$root/docker/entrypoint-adp.sh" \
  || fail "ADP entrypoint must load the independent provider Workspace locator key"
if grep -Eq '^read_secret TC_SECRET_(ID|KEY) ' "$root/docker/entrypoint-adp.sh"; then
  fail "workbench deployment must not expose private file credentials through legacy TC_SECRET variables"
fi
awk '
  /^  workbench-switch:$/ { in_switch = 1; next }
  in_switch && /^  [A-Za-z0-9_-]+:$/ { exit }
  in_switch && /^      workbench_edge:$/ { in_edge = 1; next }
  in_edge && /^          - workbench-control\.internal$/ { found = 1 }
  END { exit(found ? 0 : 1) }
' "$compose_source" || fail "workbench-switch must expose TLS alias workbench-control.internal on workbench_edge"

admin_asset_dir_count="$(grep -Fxc '  CLAW_ADMIN_ASSET_DIR: /app/admin-ui' "$compose_source" || true)"
[ "$admin_asset_dir_count" -eq 1 ] \
  || fail "control environment must set CLAW_ADMIN_ASSET_DIR=/app/admin-ui exactly once"
[ -s "$root/../../claw-control/admin-ui/index.html" ] || fail "claw-control admin UI index.html is missing"
[ -s "$root/../../claw-control/admin-ui/package.json" ] || fail "claw-control admin UI package.json is missing"
grep -Fq ' /src/claw-control/admin-ui/dist/ /app/admin-ui/' "$root/docker/Dockerfile.claw-control" \
  || fail "control image must copy the built admin UI into /app/admin-ui"
evidence_mount_count="$(grep -Fxc '      - evidence_data:/var/lib/claw-control/evidence' "$compose_source" || true)"
[ "$evidence_mount_count" -eq 2 ] || fail "both claw-control colors must share the encrypted evidence volume"
billing_mount_count="$(grep -Fxc '      - ./secrets/billing:/run/workbench/billing-secrets:ro' "$compose_source" || true)"
[ "$billing_mount_count" -eq 2 ] || fail "both claw-control colors must mount the same read-only Billing credential directory"
grep -Fq 'read_secret CLAW_EVIDENCE_MASTER_KEY /run/secrets/evidence_master_key' "$root/docker/entrypoint-control.sh" \
  || fail "control entrypoint must load the independent evidence master key"
grep -Fq 'read_secret CLAW_PROVIDER_VAULT_MASTER_KEY /run/secrets/provider_vault_master_key' "$root/docker/entrypoint-control.sh" \
  || fail "control entrypoint must load the provider vault master key"
grep -Fq 'read_secret CLAW_TENCENT_BILLING_SECRET_ID /run/workbench/billing-secrets/tencent_secret_id' "$root/docker/entrypoint-control.sh" \
  || fail "control entrypoint must inject Tencent Billing SecretId from the server-side secret directory"
grep -Fq 'read_secret CLAW_TENCENT_BILLING_SECRET_KEY /run/workbench/billing-secrets/tencent_secret_key' "$root/docker/entrypoint-control.sh" \
  || fail "control entrypoint must inject Tencent Billing SecretKey from the server-side secret directory"

public_caddy="$root/caddy/Caddyfile.public.snippet"
grep -Fq '/api/admin/workbench/step-up-ticket' "$public_caddy" \
  || fail "public Caddy snippet must keep the new-api administrator step-up endpoint outside the claw-control wildcard"
if grep -Fq '/internal/metrics' "$public_caddy"; then
  fail "internal Prometheus endpoints must never be routed through public Caddy"
fi
admin_route_line="$(awk '/^@workbenchAdmin / { print NR; exit }' "$public_caddy")"
adp_route_line="$(awk '/^@workbench \{$/ { print NR; exit }' "$public_caddy")"
[ -n "$admin_route_line" ] || fail "public Caddy snippet is missing the workbench admin route"
[ -n "$adp_route_line" ] || fail "public Caddy snippet is missing the generic workbench ADP route"
[ "$admin_route_line" -lt "$adp_route_line" ] \
  || fail "workbench admin route must appear before the generic workbench ADP route"
fingerprint_reenroll_line="$(awk '/^@workbenchFingerprintReenroll / { print NR; exit }' "$public_caddy")"
bootstrap_admin_line="$(awk '/^@workbenchBootstrapAdmin / { print NR; exit }' "$public_caddy")"
[ -n "$fingerprint_reenroll_line" ] && [ -n "$bootstrap_admin_line" ] && [ "$fingerprint_reenroll_line" -lt "$bootstrap_admin_line" ] \
  || fail "provider fingerprint re-enrollment must be blocked before the public admin wildcard"
fingerprint_reenroll_block="$(awk '
  /^handle @workbenchFingerprintReenroll \{$/ { capture = 1 }
  capture { print }
  capture && /^}$/ { exit }
' "$public_caddy")"
printf '%s\n' "$fingerprint_reenroll_block" | grep -Fq 'respond 404' \
  || fail "provider fingerprint re-enrollment must return 404 at the public edge"
admin_route_block="$(awk '
  /^handle @workbenchAdmin \{$/ { capture = 1 }
  capture { print }
  capture && /^}$/ { exit }
' "$public_caddy")"
printf '%s\n' "$admin_route_block" | grep -Fq 'reverse_proxy claw-control-active:8080' \
  || fail "workbench admin route must proxy to claw-control"
if printf '%s\n' "$admin_route_block" | grep -Fq 'forward_auth'; then
  fail "workbench admin static route must not use forward_auth"
fi

command -v docker >/dev/null 2>&1 || fail "docker is not installed"
command -v openssl >/dev/null 2>&1 || fail "openssl is required for certificate validation"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required for backup manifests"
python3 "$root/scripts/lint_overlay_security.py" --root "$root" \
  || fail "deployment overlay security lint failed"
python3 "$root/scripts/validate_caddy_contract.py" --root "$root" \
  || fail "Caddy workbench security contract failed"
docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is not available"
compose_version="$(docker compose version --short | sed 's/^v//')"
compose_major="$(printf '%s' "$compose_version" | cut -d. -f1)"
compose_minor="$(printf '%s' "$compose_version" | cut -d. -f2)"
case "$compose_major:$compose_minor" in *[!0-9:]*) fail "cannot parse Docker Compose version: $compose_version" ;; esac
if [ "$compose_major" -lt 2 ] || { [ "$compose_major" -eq 2 ] && [ "$compose_minor" -lt 17 ]; }; then
  fail "Docker Compose >=2.17 is required for additional build contexts"
fi
if [ "$CLAW_BUILD_LOCAL_IMAGES" = true ]; then
  docker buildx version >/dev/null 2>&1 || fail "Docker Buildx/BuildKit is required for local image builds"
fi
docker network inspect "$WORKBENCH_EDGE_NETWORK" >/dev/null 2>&1 || fail "external network $WORKBENCH_EDGE_NETWORK does not exist"

openssl verify -CAfile "$root/secrets/internal_ca.crt" "$root/secrets/workbench_control.crt" >/dev/null \
  || fail "workbench control certificate is not signed by internal_ca.crt"
openssl x509 -in "$root/secrets/workbench_control.crt" -noout -checkhost workbench-control.internal >/dev/null \
  || fail "workbench control certificate lacks workbench-control.internal SAN"
openssl x509 -in "$root/secrets/workbench_control.crt" -noout -checkend 2592000 >/dev/null \
  || fail "workbench control certificate expires within 30 days"
control_cert_pub="$(openssl x509 -in "$root/secrets/workbench_control.crt" -pubkey -noout)"
control_key_pub="$(openssl pkey -in "$root/secrets/workbench_control.key" -pubout)"
[ "$control_cert_pub" = "$control_key_pub" ] || fail "workbench control certificate and private key do not match"
openssl verify -CAfile "$root/secrets/internal_ca.crt" "$root/secrets/new_api_identity.crt" >/dev/null \
  || fail "identity certificate is not signed by internal_ca.crt"
openssl x509 -in "$root/secrets/new_api_identity.crt" -noout -checkhost new-api-identity.internal >/dev/null \
  || fail "identity certificate lacks new-api-identity.internal SAN"
openssl x509 -in "$root/secrets/new_api_identity.crt" -noout -checkend 2592000 >/dev/null \
  || fail "identity certificate expires within 30 days"
identity_cert_pub="$(openssl x509 -in "$root/secrets/new_api_identity.crt" -pubkey -noout)"
identity_key_pub="$(openssl pkey -in "$root/secrets/new_api_identity.key" -pubout)"
[ "$identity_cert_pub" = "$identity_key_pub" ] || fail "identity certificate and private key do not match"
unset control_cert_pub control_key_pub identity_cert_pub identity_key_pub

sh "$root/scripts/compose.sh" config --quiet
config_compare_dir="$(mktemp -d)"
trap 'rm -rf "$config_compare_dir"' 0
sh "$root/scripts/compose.sh" config claw-control-blue > "$config_compare_dir/control-blue.raw.yml"
sh "$root/scripts/compose.sh" config claw-control-green > "$config_compare_dir/control-green.raw.yml"
sh "$root/scripts/compose.sh" config adp-blue > "$config_compare_dir/adp-blue.raw.yml"
sh "$root/scripts/compose.sh" config adp-green > "$config_compare_dir/adp-green.raw.yml"
python3 "$root/scripts/normalize_compose_color.py" \
  --input "$config_compare_dir/control-blue.raw.yml" \
  --service claw-control-blue > "$config_compare_dir/control-blue.yml" \
  || fail "cannot normalize rendered claw-control Blue configuration"
python3 "$root/scripts/normalize_compose_color.py" \
  --input "$config_compare_dir/control-green.raw.yml" \
  --service claw-control-green > "$config_compare_dir/control-green.yml" \
  || fail "cannot normalize rendered claw-control Green configuration"
cmp -s "$config_compare_dir/control-blue.yml" "$config_compare_dir/control-green.yml" \
  || fail "rendered claw-control Blue and Green configuration differs"
python3 "$root/scripts/normalize_compose_color.py" \
  --input "$config_compare_dir/adp-blue.raw.yml" \
  --service adp-blue > "$config_compare_dir/adp-blue.yml" \
  || fail "cannot normalize rendered ADP Blue configuration"
python3 "$root/scripts/normalize_compose_color.py" \
  --input "$config_compare_dir/adp-green.raw.yml" \
  --service adp-green > "$config_compare_dir/adp-green.yml" \
  || fail "cannot normalize rendered ADP Green configuration"
cmp -s "$config_compare_dir/adp-blue.yml" "$config_compare_dir/adp-green.yml" \
  || fail "rendered ADP Blue and Green configuration differs outside the allowed instance/image/log-volume fields"
docker run --rm --entrypoint caddy \
  --user 10001:10001 \
  --cap-drop ALL --cap-add NET_BIND_SERVICE \
  --security-opt no-new-privileges:true \
  -v "$root/caddy:/etc/caddy:ro" \
  "$CADDY_IMAGE" validate --config /etc/caddy/Caddyfile.public.validate --adapter caddyfile >/dev/null
docker run --rm --entrypoint caddy \
  --user 10001:10001 \
  --cap-drop ALL --cap-add NET_BIND_SERVICE \
  --security-opt no-new-privileges:true \
  -e NEW_API_INTERNAL_UPSTREAM="$NEW_API_INTERNAL_UPSTREAM" \
  -v "$root/caddy:/etc/caddy:ro" \
  "$CADDY_IMAGE" adapt --config /etc/caddy/Caddyfile.identity-proxy --adapter caddyfile >/dev/null
docker run --rm --entrypoint caddy \
  --user 10001:10001 \
  --cap-drop ALL --cap-add NET_BIND_SERVICE \
  --security-opt no-new-privileges:true \
  -v "$root/state:/config-runtime:ro" \
  "$CADDY_IMAGE" adapt --config /config-runtime/Caddyfile.active --adapter caddyfile >/dev/null
echo "preflight: TLS contracts, admin routes/assets, Blue/Green parity, network, secrets, and certificates passed"
