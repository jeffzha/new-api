#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
color="${1:-}"
case "$color" in
  blue|green) ;;
  *) echo "usage: $0 blue|green" >&2; exit 2 ;;
esac

sh "$root/scripts/preflight.sh"
compose() { sh "$root/scripts/compose.sh" "$@"; }
build_local="$(python3 "$root/scripts/strict_dotenv.py" --file "$root/.env" --get CLAW_BUILD_LOCAL_IMAGES)"

# Dependencies are started first and must be healthy before --no-deps is used
# for the selected application color. App services remain unreachable from the
# host. ClamAV is required even when customer file upload is disabled because
# claw-control evidence ingestion is always fail-closed through the scanner.
compose up -d --wait --wait-timeout 600 workbench-control-db workbench-adp-db workbench-redis workbench-clamav new-api-identity-proxy
if [ "$build_local" = true ]; then
  compose build "claw-control-$color" "adp-$color"
else
  compose pull "claw-control-$color" "adp-$color"
fi
unset build_local
compose up -d --no-deps --wait --wait-timeout 300 "claw-control-$color" "adp-$color"

if ! compose exec -T "claw-control-$color" wget -q -O - http://127.0.0.1:8090/readyz >/dev/null; then
  if compose exec -T "claw-control-$color" wget -q -O - http://127.0.0.1:8090/healthz >/dev/null; then
    echo "$color is alive but not ready. After migration 0015, review the mounted provider Secret versions and run:" >&2
    echo "  sh ./scripts/reenroll-provider-fingerprints.sh $color RE-ENROLL-CURRENT-RUNTIME-SECRETS" >&2
  fi
  exit 1
fi
compose exec -T "adp-$color" python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/system/config', timeout=3).read()"
echo "$color is healthy and still receives no traffic; run switch-active.sh $color after E2E"
