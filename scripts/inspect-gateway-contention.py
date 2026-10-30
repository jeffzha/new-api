"""Bounded, read-only gateway probes and PostgreSQL blocker metadata capture."""

import argparse
import concurrent.futures
from datetime import datetime, timezone
import json
from pathlib import Path
import subprocess
import time
import urllib.request


LOCK_SQL = r'''BEGIN READ ONLY; SET LOCAL statement_timeout='2s';
SELECT coalesce(json_agg(t),'[]'::json) FROM (
 SELECT a.pid,a.client_addr::text AS client,a.state,a.wait_event_type,a.wait_event,
 round(extract(epoch FROM clock_timestamp()-a.xact_start),3) AS transaction_age_s,
 pg_blocking_pids(a.pid) AS blockers,
 substring(a.query from '^[[:space:]]*([[:alpha:]]+)') AS statement,
 ARRAY(SELECT (r)[1] FROM regexp_matches(a.query,
 '(?:FROM|UPDATE|INTO|JOIN)[[:space:]]+"?([[:alnum:]_]+)','gi') r LIMIT 6) AS tables
 FROM pg_stat_activity a WHERE a.datname=current_database() AND a.pid<>pg_backend_pid()
 AND (a.wait_event_type='Lock' OR cardinality(pg_blocking_pids(a.pid))>0
 OR a.pid IN (SELECT unnest(pg_blocking_pids(b.pid)) FROM pg_stat_activity b
             WHERE b.datname=current_database() AND b.wait_event_type='Lock'))
 ORDER BY a.xact_start NULLS LAST LIMIT 32) t; COMMIT;'''


def command(args, *, input=None):
    result = subprocess.run(args, input=input, capture_output=True, text=True, timeout=5)
    if result.returncode:
        raise RuntimeError("read_only_command_failed")
    return result.stdout


def probe(target):
    name, base, path = target
    start = time.monotonic()
    result = {"service": name, "path": path}
    try:
        request = urllib.request.Request(base + path, headers={"Cache-Control": "no-cache"})
        with urllib.request.urlopen(request, timeout=5) as response:
            data = json.loads(response.read(262144))
        result["ok"] = data.get("success") is True if path == "/api/status" else data.get("status") == "ready"
    except Exception as error:
        result.update(ok=False, error=type(error).__name__)
    result["milliseconds"] = round((time.monotonic() - start) * 1000, 2)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--slot", action="append", required=True, help="Existing container name; repeat for each slot")
    parser.add_argument("--network", required=True, help="Existing shared container network")
    parser.add_argument("--public-url", required=True)
    parser.add_argument("--database-container", required=True)
    parser.add_argument("--database-user", required=True)
    parser.add_argument("--database-name", required=True)
    parser.add_argument("--samples", type=int, default=8)
    parser.add_argument("--interval", type=float, default=5)
    parser.add_argument("--output", type=Path, help="New JSONL evidence file; existing files are never overwritten")
    args = parser.parse_args()
    if not 1 <= args.samples <= 60 or not 2 <= args.interval <= 60:
        parser.error("samples must be 1..60 and interval 2..60 seconds")
    if len(args.slot) > 4:
        parser.error("at most four existing slots can be inspected")
    targets = [("public", args.public_url.rstrip("/"))]
    for name in args.slot:
        info = json.loads(command(["docker", "inspect", name]))[0]
        ip = info["NetworkSettings"]["Networks"][args.network]["IPAddress"]
        targets.append((name, "http://" + ip + ":3000"))
    output = args.output.open("x", encoding="utf-8") if args.output else None
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            for index in range(args.samples):
                requests = [(name, base, path) for name, base in targets for path in ("/healthz", "/api/status")]
                row = {"at": datetime.now(timezone.utc).isoformat(), "probes": list(pool.map(probe, requests))}
                try:
                    raw = command(["docker", "exec", "-i", args.database_container, "psql", "-X", "-qAt",
                                   "-v", "ON_ERROR_STOP=1", "-U", args.database_user, "-d", args.database_name], input=LOCK_SQL)
                    row["lockChain"] = next(json.loads(line) for line in raw.splitlines() if line.startswith("["))
                except Exception as error:
                    row["databaseError"] = type(error).__name__
                line = json.dumps(row)
                print(line, flush=True)
                if output:
                    output.write(line + "\n")
                    output.flush()
                if index + 1 < args.samples:
                    time.sleep(args.interval)
    finally:
        if output:
            output.close()


if __name__ == "__main__":
    main()
