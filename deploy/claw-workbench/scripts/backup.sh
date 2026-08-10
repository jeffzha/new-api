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

traffic_lock="$root/state/.traffic-switch.lock"
if ! mkdir "$traffic_lock" 2>/dev/null; then
  echo "another workbench traffic switch, rollback, or backup is running" >&2
  exit 1
fi
cleanup() {
  rmdir "$traffic_lock" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -m 0700 "$target"

python3 "$root/scripts/collect_release_manifest.py" \
  --deployment-root "$root" \
  --output "$root/state/release-manifest.json"

python3 "$root/scripts/backup_manifest.py" \
  --release-manifest "$root/state/release-manifest.json" \
  --backup-created-at "$stamp" \
  --output "$target/manifest.txt"

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

{
  for service in claw-control-blue claw-control-green adp-blue adp-green; do
    container_id="$(compose ps -q "$service")"
    [ -n "$container_id" ] && echo "$service.image_id=$(docker inspect -f '{{.Image}}' "$container_id")"
  done
} >> "$target/manifest.txt"

(cd "$target" && sha256sum control-db.dump adp-db.dump redis-forensics.rdb manifest.txt > SHA256SUMS)
python3 "$root/scripts/verify_backup.py" "$target" >/dev/null
echo "backup created: $target"
echo "copy it immediately to encrypted, access-controlled off-host storage"
