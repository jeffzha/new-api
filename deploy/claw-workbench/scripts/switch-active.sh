#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
color="${1:-}"
case "$color" in
  blue|green) ;;
  *) echo "usage: $0 blue|green" >&2; exit 2 ;;
esac

control="claw-control-$color"
adp="adp-$color"
compose() { sh "$root/scripts/compose.sh" "$@"; }
lock="$root/state/.traffic-switch.lock"
if ! mkdir "$lock" 2>/dev/null; then
  echo "another workbench traffic switch or rollback is already running" >&2
  exit 1
fi

next="$root/state/Caddyfile.next"
active="$root/state/Caddyfile.active"
previous="$root/state/Caddyfile.before-switch"
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

compose exec -T "$control" wget -q -O - http://127.0.0.1:8090/readyz >/dev/null
compose exec -T "$adp" python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/system/config', timeout=3).read()"

sed \
  -e "s/__CONTROL_UPSTREAM__/$control:8090/g" \
  -e "s/__ADP_UPSTREAM__/$adp:8000/g" \
  "$root/caddy/Caddyfile.switch.template" > "$next"
chmod 0644 "$next"

compose up -d workbench-switch
compose exec -T workbench-switch caddy validate --config /config-runtime/Caddyfile.next --adapter caddyfile
mv "$next" "$active"
if ! compose exec -T workbench-switch caddy reload --config /config-runtime/Caddyfile.active --adapter caddyfile; then
  cp "$previous" "$next"
  chmod 0644 "$next"
  mv "$next" "$active"
  compose exec -T workbench-switch caddy reload --config /config-runtime/Caddyfile.active --adapter caddyfile \
    || echo "CRITICAL: Caddy rejected both the candidate and restored configuration" >&2
  echo "traffic switch failed; the previous active configuration was restored" >&2
  exit 1
fi

if ! compose exec -T workbench-switch wget -q -O - http://127.0.0.1:8080/readyz >/dev/null \
  || ! compose exec -T workbench-switch wget -q -O - http://127.0.0.1:8000/system/config >/dev/null; then
  cp "$previous" "$next"
  chmod 0644 "$next"
  mv "$next" "$active"
  if ! compose exec -T workbench-switch caddy reload --config /config-runtime/Caddyfile.active --adapter caddyfile; then
    echo "CRITICAL: post-switch health failed and Caddy could not restore the previous route" >&2
  fi
  echo "post-switch health failed; the previous active configuration was restored" >&2
  exit 1
fi

echo "$color" > "$root/state/active-color"
chmod 0600 "$root/state/active-color"
echo "active workbench color: $color"
