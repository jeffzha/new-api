#!/usr/bin/env bash
set -euo pipefail

ENV_FILE=${ENV_FILE:-/etc/newapi-disk-monitor.env}
if [[ -r "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a
fi
exec python3 "$(dirname "$(readlink -f "$0")")/newapi-cpu-monitor.py" "$@"
