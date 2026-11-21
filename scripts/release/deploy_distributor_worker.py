"""Run only durable non-wallet distributor delivery using a tested artifact.
Existing HTTP services and legacy money workers are never replaced or started.
"""
import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import tempfile


def run(args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=45)
    if result.returncode:
        raise RuntimeError("distributor release command failed: " + args[0])
    return result.stdout.strip()


def main():
    parser = argparse.ArgumentParser()
    for field in ["source", "binary", "sha256", "network", "name"]:
        parser.add_argument("--" + field, required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    binary = pathlib.Path(args.binary).resolve(strict=True)
    if hashlib.sha256(binary.read_bytes()).hexdigest() != args.sha256:
        raise RuntimeError("immutable binary mismatch")
    source = json.loads(run(["docker", "inspect", args.source]))[0]
    env = dict(value.split("=", 1) for value in source["Config"]["Env"] if "=" in value)
    if (not source["State"]["Running"] or
            env.get("OPS_WALLET_AUTHORITY") != "ops" or
            env.get("OPS_BILLING_CURRENCY") != "USD" or
            env.get("OPS_DISTRIBUTOR_EVENTS_ENABLED") != "true" or
            env.get("OPS_DISTRIBUTOR_NON_WALLET_ONLY") != "true" or
            env.get("OPS_DISTRIBUTOR_FINANCE_AUTHORITY") != "gateway" or
            env.get("OPS_GATEWAY_ID") != env.get("OPS_PUSH_KEY_ID") or
            env.get("OPS_BRIDGE_URL", "").rstrip("/") != env.get("OPS_PUSH_URL", "").rstrip("/") or
            len(env.get("OPS_PUSH_SECRET", "")) < 16 or
            args.network not in source["NetworkSettings"]["Networks"]):
        raise RuntimeError("overseas distributor scope mismatch")
    fields = ["SQL_DSN", "OPS_WALLET_AUTHORITY", "OPS_DISTRIBUTOR_EVENTS_ENABLED",
              "OPS_DISTRIBUTOR_NON_WALLET_ONLY", "OPS_DISTRIBUTOR_FINANCE_AUTHORITY",
              "OPS_GATEWAY_ID", "OPS_BRIDGE_URL", "OPS_PUSH_KEY_ID",
              "OPS_PUSH_URL", "OPS_PUSH_SECRET"]
    values = {name: env[name] for name in fields if name in env}
    if not values.get("SQL_DSN"):
        raise RuntimeError("source database missing")
    values.update(GOMAXPROCS="1", SQL_MAX_OPEN_CONNS="1", SQL_MAX_IDLE_CONNS="1",
                  AGENCY_HUB_AUTO_MIGRATE="false", AGENCY_HUB_BACKGROUND_DISABLED="true",
                  GIN_MODE="release")
    fd, name = tempfile.mkstemp(prefix="distributor-worker-", suffix=".env", dir=str(binary.parent))
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w") as stream:
            stream.write("".join(key + "=" + value + "\n" for key, value in values.items()))
        base = ["docker", "run", "--read-only", "--workdir", "/tmp", "--network", args.network,
                "--cpus", "0.1", "--memory", "128m", "--pids-limit", "32",
                "--no-healthcheck", "--env-file", name,
                "--tmpfs", "/tmp:rw,nosuid,size=16m",
                "--tmpfs", "/logs:rw,nosuid,size=8m",
                "--mount", "type=bind,src=" + str(binary) + ",dst=/distributor-worker,readonly",
                "--entrypoint", "/distributor-worker"]
        run(base + ["--rm", source["Image"], "distributor-check"])
        if args.apply:
            run(base + ["-d", "--restart", "unless-stopped", "--name", args.name,
                        "--log-opt", "max-size=5m", "--log-opt", "max-file=2",
                        source["Image"], "distributor-worker"])
        print(json.dumps({"sha256": args.sha256, "candidatePassed": True,
                          "workerStarted": args.apply, "httpServicesReplaced": False,
                          "walletWorkersStarted": False, "portsPublished": False}))
    finally:
        pathlib.Path(name).unlink(missing_ok=True)


if __name__ == "__main__":
    main()
