#!/bin/sh
set -eu
umask 077

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
compose() { sh "$root/scripts/compose.sh" "$@"; }
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
target="${1:-$root/backups/$stamp}"
if [ -e "$target" ] || [ -L "$target" ]; then
  echo "backup target must not already exist: $target" >&2
  exit 1
fi
mkdir -m 0700 "$target"

compose exec -T workbench-control-db sh -ec \
  'PGPASSWORD="$(cat /run/secrets/control_db_password)" pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom --no-owner --no-acl' \
  > "$target/control-db.dump"

compose exec -T workbench-adp-db sh -ec \
  'PGPASSWORD="$(cat /run/secrets/adp_db_password)" pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom --no-owner --no-acl' \
  > "$target/adp-db.dump"

redis_id="$(compose ps -q workbench-redis)"
[ -n "$redis_id" ] || { echo "workbench-redis is not running" >&2; exit 1; }
compose exec -T workbench-redis sh -ec \
  'rm -f /tmp/workbench-backup.rdb; REDISCLI_AUTH="$(cat /run/secrets/redis_password)" redis-cli --rdb /tmp/workbench-backup.rdb >/dev/null'
docker cp "$redis_id:/tmp/workbench-backup.rdb" "$target/redis-forensics.rdb" >/dev/null
compose exec -T workbench-redis rm -f /tmp/workbench-backup.rdb

NEW_API_REVISION="$(python3 "$root/scripts/strict_dotenv.py" --file "$root/.env" --get NEW_API_REVISION)"
CLAW_CONTROL_REVISION="$(python3 "$root/scripts/strict_dotenv.py" --file "$root/.env" --get CLAW_CONTROL_REVISION)"
ADP_REVISION="$(python3 "$root/scripts/strict_dotenv.py" --file "$root/.env" --get ADP_REVISION)"
{
  echo "created_at=$stamp"
  echo "new_api_revision=${NEW_API_REVISION:-unknown}"
  echo "claw_control_revision=${CLAW_CONTROL_REVISION:-unknown}"
  echo "adp_revision=${ADP_REVISION:-unknown}"
  echo "active_color=$(cat "$root/state/active-color" 2>/dev/null || echo blue)"
  for service in claw-control-blue claw-control-green adp-blue adp-green; do
    container_id="$(compose ps -q "$service")"
    [ -n "$container_id" ] && echo "$service.image=$(docker inspect -f '{{.Image}}' "$container_id")"
  done
} > "$target/manifest.txt"

(cd "$target" && sha256sum control-db.dump adp-db.dump redis-forensics.rdb manifest.txt > SHA256SUMS)
python3 "$root/scripts/verify_backup.py" "$target" >/dev/null
echo "backup created: $target"
echo "copy it immediately to encrypted, access-controlled off-host storage"
