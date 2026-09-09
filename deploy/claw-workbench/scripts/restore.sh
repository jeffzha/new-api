#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
backup="${1:-}"
confirmation="${2:-}"
[ -d "$backup" ] || { echo "usage: $0 BACKUP_DIRECTORY RESTORE" >&2; exit 2; }
[ "$confirmation" = "RESTORE" ] || { echo "destructive restore requires literal RESTORE" >&2; exit 2; }

python3 "$root/scripts/verify_backup.py" "$backup"
compose() { sh "$root/scripts/compose.sh" "$@"; }

# Stop all writers and the traffic switch. Failure leaves the gate stopped.
compose stop workbench-switch adp-blue adp-green claw-control-blue claw-control-green

restore_database() {
  service="$1"
  secret="$2"
  dump="$3"
  compose exec -T "$service" sh -ec \
    "PGPASSWORD=\"\$(cat /run/secrets/$secret)\" dropdb --if-exists --force -U \"\$POSTGRES_USER\" \"\$POSTGRES_DB\" && PGPASSWORD=\"\$(cat /run/secrets/$secret)\" createdb -U \"\$POSTGRES_USER\" \"\$POSTGRES_DB\""
  compose exec -T "$service" sh -ec \
    "PGPASSWORD=\"\$(cat /run/secrets/$secret)\" pg_restore -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" --no-owner --no-acl --exit-on-error" \
    < "$dump"
}

restore_database workbench-control-db control_db_password "$backup/control-db.dump"
restore_database workbench-adp-db adp_db_password "$backup/adp-db.dump"

# A point-in-time restore must never revive a browser session or one-time ticket.
compose exec -T workbench-control-db sh -ec \
  'PGPASSWORD="$(cat /run/secrets/control_db_password)" psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<"SQL"
UPDATE claw_control_sessions SET revoked_at = CURRENT_TIMESTAMP WHERE revoked_at IS NULL;
UPDATE claw_admin_sessions SET revoked_at = CURRENT_TIMESTAMP WHERE revoked_at IS NULL;
UPDATE claw_entry_tickets SET consumed_at = CURRENT_TIMESTAMP WHERE consumed_at IS NULL;
UPDATE claw_sso_tickets SET consumed_at = CURRENT_TIMESTAMP WHERE consumed_at IS NULL;
SQL'

# Redis contains only reconstructible rate-limit/revocation cache. Never revive
# that cache from a point-in-time backup after the database restore.
compose exec -T workbench-redis sh -ec \
  'REDISCLI_AUTH="$(cat /run/secrets/redis_password)" redis-cli FLUSHALL >/dev/null'

compose up -d claw-control-blue claw-control-green adp-blue adp-green new-api-identity-proxy
echo "database restore completed; database sessions/tickets and Redis cache were intentionally invalidated"
echo "run E2E checks, then use switch-active.sh and only afterward re-enable new-api"
