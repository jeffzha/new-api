#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
go_bin="${GO_BIN:-go}"
verify_tmp_dir="$(mktemp -d)"
trap 'rm -rf "$verify_tmp_dir"' EXIT

cd "$repo_root"

echo "== Agency Hub SQLite migration and probe tests =="
"$go_bin" test ./model -run 'TestMigrateAgencySQLiteIsIdempotentAndIndexesReportingFacts$' -count=1
"$go_bin" test ./pkg/agencyhub -run 'TestAgencyHealthAndReadinessProbes$' -count=1

echo "== Agency Hub build =="
"$go_bin" build -o "$verify_tmp_dir/agency-hub" ./cmd/agency-hub

if [[ "${AGENCY_HUB_RUN_EXTERNAL_DB_TESTS:-0}" == "1" ]]; then
  echo "== Agency Hub external database migration tests =="
  "$go_bin" test ./model -run 'TestMigrateAgencyExternalDatabaseCompatibility$' -count=1
else
  echo "Skipping MySQL/PostgreSQL tests. Set AGENCY_HUB_RUN_EXTERNAL_DB_TESTS=1 and provide:"
  echo "  AGENCY_HUB_TEST_MYSQL_DSN"
  echo "  AGENCY_HUB_TEST_POSTGRES_DSN"
fi

echo "Agency Hub verification passed."
