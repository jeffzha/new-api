#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
env_file="$root/.env"
state_file="$root/state/Caddyfile.active"

if [ ! -f "$env_file" ]; then
  echo "missing $env_file; copy .env.example and review every value" >&2
  exit 1
fi

if [ ! -f "$state_file" ]; then
  sed \
    -e 's/__CONTROL_UPSTREAM__/claw-control-blue:8090/g' \
    -e 's/__ADP_UPSTREAM__/adp-blue:8000/g' \
    "$root/caddy/Caddyfile.switch.template" > "$state_file"
  chmod 0644 "$state_file"
fi

exec docker compose \
  --project-directory "$root" \
  --env-file "$env_file" \
  -f "$root/compose.yml" \
  --profile claw-workbench \
  "$@"
