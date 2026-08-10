#!/bin/sh
set -eu
umask 077

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

if [ "$#" -ne 0 ]; then
  echo "usage: $0" >&2
  exit 2
fi

exec python3 "$root/scripts/collect_release_manifest.py" \
  --deployment-root "$root" \
  --output "$root/state/release-manifest.json"
