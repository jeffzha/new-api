#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
if [ "${1:-}" != "--confirm-new-api-disabled" ]; then
  echo "First set WORKBENCH_ENABLED=false on BOTH new-api Blue and Green." >&2
  echo "Then run: $0 --confirm-new-api-disabled" >&2
  exit 2
fi

compose() { sh "$root/scripts/compose.sh" "$@"; }
active="$root/state/Caddyfile.active"
lock="$root/state/.traffic-switch.lock"
if ! mkdir "$lock" 2>/dev/null; then
  echo "another workbench traffic switch or rollback is already running" >&2
  exit 1
fi
previous="$root/state/Caddyfile.before-rollback"
next="$root/state/Caddyfile.disabled.next"
cleanup() {
  rm -f "$next" "$previous"
  rmdir "$lock" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

[ -f "$active" ] || { echo "active Caddy configuration is missing" >&2; exit 1; }
cp "$active" "$previous"
chmod 0600 "$previous"
cp "$root/caddy/Caddyfile.switch.disabled" "$next"
chmod 0644 "$next"
compose exec -T workbench-switch caddy validate --config /config-runtime/Caddyfile.disabled.next --adapter caddyfile
mv "$next" "$active"
if ! compose exec -T workbench-switch caddy reload --config /config-runtime/Caddyfile.active --adapter caddyfile; then
  cp "$previous" "$next"
  chmod 0644 "$next"
  mv "$next" "$active"
  compose exec -T workbench-switch caddy reload --config /config-runtime/Caddyfile.active --adapter caddyfile \
    || echo "CRITICAL: Caddy rejected both the disabled and restored configuration" >&2
  echo "rollback gate reload failed; the previous active configuration was restored" >&2
  exit 1
fi
echo "workbench gate disabled; databases and private services remain intact"
echo "the legacy /playground route remains available through new-api"
