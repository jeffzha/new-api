"""Start a binding-only sidecar from a committed, locally tested binary.
No HTTP service replacement, migrations or old wallet workers are performed.
Run with explicit overseas source container, immutable binary and SHA256.
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
        raise RuntimeError("provisioning release command failed: " + args[0])
    return result.stdout.strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--network", required=True)
    parser.add_argument("--name", required=True)
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
            env.get("AGENCY_ONBOARDING_ENABLED") != "true" or
            args.network not in source["NetworkSettings"]["Networks"]):
        raise RuntimeError("overseas OPS binding worker scope mismatch")
    # The worker does not need payment, email, relay keys, export mounts or HTTP.
    values = {name: env[name] for name in ["SQL_DSN", "OPS_WALLET_AUTHORITY",
              "OPS_PROJECT_SCOPE", "AGENCY_ONBOARDING_ENABLED"] if name in env}
    if not values.get("SQL_DSN") or not values.get("OPS_PROJECT_SCOPE"):
        raise RuntimeError("binding database or scope missing")
    values.update(GOMAXPROCS="1", SQL_MAX_OPEN_CONNS="1", SQL_MAX_IDLE_CONNS="1",
                  AGENCY_HUB_AUTO_MIGRATE="false", AGENCY_HUB_BACKGROUND_DISABLED="true",
                  GIN_MODE="release")
    fd, name = tempfile.mkstemp(prefix="binding-worker-", suffix=".env", dir=str(binary.parent))
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w") as stream:
            stream.write("".join(key + "=" + value + "\n" for key, value in values.items()))
        base = ["docker", "run", "--read-only", "--network", args.network,
                "--cpus", "0.1", "--memory", "128m", "--pids-limit", "32",
                "--no-healthcheck", "--env-file", name,
                "--tmpfs", "/tmp:rw,nosuid,size=16m",
                "--tmpfs", "/logs:rw,nosuid,size=8m",
                "--mount", "type=bind,src=" + str(binary) + ",dst=/provision-worker,readonly",
                "--entrypoint", "/provision-worker"]
        if not args.apply:
            print(json.dumps({"binarySha256": args.sha256, "scopeChecked": True,
                              "workerStarted": False, "databaseWritten": False}))
            return
        # Readiness executes the already tested queue logic in a short-lived
        # process; existing business safeguards remain in force. Never starts HTTP.
        run(base + ["--rm", source["Image"], "provision-once"])
        run(base + ["-d", "--restart", "unless-stopped", "--name", args.name,
                    "--log-opt", "max-size=5m", "--log-opt", "max-file=2",
                    source["Image"], "provision-worker"])
        print(json.dumps({"binarySha256": args.sha256, "bindingOnly": True,
                          "candidatePassed": True, "workerStarted": args.apply,
                          "httpServicesReplaced": False, "portsPublished": False}))
    finally:
        pathlib.Path(name).unlink(missing_ok=True)


if __name__ == "__main__":
    main()
