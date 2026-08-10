#!/bin/sh
set -eu

load_secret() {
  name="$1"
  path="$2"
  [ -f "$path" ] && [ ! -L "$path" ] || {
    echo "required workbench secret is unavailable" >&2
    exit 1
  }
  size="$(wc -c < "$path" | tr -d ' ')"
  [ "$size" -ge 32 ] && [ "$size" -le 4096 ] || {
    echo "required workbench secret has an invalid size" >&2
    exit 1
  }
  value="$(sed -n '1p' "$path")"
  [ -n "$value" ] && [ -z "$(sed -n '2p' "$path")" ] || {
    echo "required workbench secret has an invalid format" >&2
    exit 1
  }
  export "$name=$value"
}

load_secret WORKBENCH_CONTROL_HMAC_SECRET /run/secrets/new_api_control_hmac
load_secret WORKBENCH_SERVICE_HMAC_SECRET /run/secrets/new_api_identity_hmac

exec /new-api "$@"
