#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
color="${1:-}"
confirmation="${2:-}"
case "$color" in
  blue|green) ;;
  *) echo "usage: $0 blue|green RE-ENROLL-CURRENT-RUNTIME-SECRETS" >&2; exit 2 ;;
esac
[ "$confirmation" = "RE-ENROLL-CURRENT-RUNTIME-SECRETS" ] || {
  echo "refusing legacy fingerprint re-enrollment without the exact confirmation" >&2
  exit 2
}

compose() { sh "$root/scripts/compose.sh" "$@"; }

# The target color may be alive but intentionally unready after migration 0015.
# Call only its loopback bootstrap endpoint. The token is read inside the
# container and written into the request by the shell builtin; it is never
# copied to the host, command arguments, stdout, or logs.
compose exec -T "claw-control-$color" sh -eu -c '
body="{\"confirmation\":\"rebind-current-runtime-secrets\"}"
token="$(cat /run/secrets/claw_admin_token)"
length="${#body}"
response="$(
  {
    printf "POST /api/admin/workbench/secret-fingerprints/re-enroll HTTP/1.1\r\n"
    printf "Host: 127.0.0.1:8090\r\n"
    printf "Authorization: Bearer %s\r\n" "$token"
    printf "Content-Type: application/json\r\n"
    printf "Content-Length: %s\r\n" "$length"
    printf "Connection: close\r\n\r\n%s" "$body"
  } | busybox nc -w 10 127.0.0.1 8090
)"
status="$(printf "%s\n" "$response" | sed -n "1p" | tr -d "\r")"
case "$status" in
  "HTTP/1.1 200 "*) ;;
  *)
    echo "provider fingerprint re-enrollment failed: $status" >&2
    exit 1
    ;;
esac
unset token body response
'

compose exec -T "claw-control-$color" wget -q -O - http://127.0.0.1:8090/readyz >/dev/null
echo "$color provider fingerprints are canonical and readiness is healthy"
