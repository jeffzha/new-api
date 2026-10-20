"""Apply the authorized China channel-16 integration without restarting services.

Run on the China deployment host from an exported copy of this revision.
Secrets stay in memory; backups are private and never printed or committed.
"""

import argparse
import datetime
import hashlib
import json
import os
import pathlib
import re
import subprocess
import time
import urllib.error
import urllib.request


MODEL = "doubao-seedream-5-0-lite"
UPSTREAM = "Doubao-Seedream-5.0-lite"
BASE = "https://gateway.nexus-reach.com"
PG = "new-api-seedance-postgres-1"
EXPECTED_TARIFF = 'len <= 32000 ? tier("base", p * 0.6 + c * 3.6 + cr * 0.12) : len > 128000 ? tier("第2档", p * 1.8 + c * 10.8 + cr * 0.36) : tier("第3档", p * 0.9 + c * 5.4 + cr * 0.18)'


def query(sql):
    result = subprocess.run(
        ["docker", "exec", "-i", PG, "sh", "-c",
         'exec psql -X -v ON_ERROR_STOP=1 -At -U "$POSTGRES_USER" -d "$POSTGRES_DB"'],
        input="BEGIN READ ONLY; SET LOCAL statement_timeout=8000;\n" + sql + ";\nROLLBACK;\n",
        text=True, capture_output=True, check=True,
    )
    return [json.loads(line) for line in result.stdout.splitlines() if line.startswith("{")]


def request(path, method="GET", body=None, headers=None, timeout=40):
    req = urllib.request.Request(
        path if path.startswith("https://") else BASE + path,
        data=None if body is None else json.dumps(body).encode(), method=method,
        headers={"Content-Type": "application/json", **(headers or {})},
    )
    try:
        response = urllib.request.urlopen(req, timeout=timeout)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = json.load(response)
        if response.status >= 400:
            raise RuntimeError(f"HTTP {response.status}: " + json.dumps(data, ensure_ascii=False)[:1500])
        return data, dict(response.headers)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("phase", choices=["inspect", "apply", "smoke", "verify"])
    parser.add_argument("--state", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    state_path = pathlib.Path(args.state)
    expression_source = pathlib.Path(__file__).resolve().parents[1] / "setting/billing_setting/builtin_billing.go"
    expression = re.search(r'const szhtpSeedream50LiteExpression = `([^`]+)`', expression_source.read_text()).group(1)
    admin = query("SELECT row_to_json(t) FROM (SELECT id,access_token FROM users WHERE role=100 AND status=1 AND access_token IS NOT NULL ORDER BY id LIMIT 1) t")[0]
    auth = {"Authorization": "Bearer " + admin["access_token"], "New-Api-User": str(admin["id"])}

    def manage(path, method="GET", body=None):
        data, _ = request(path, method, body, auth)
        if data.get("success") is not True:
            raise RuntimeError(data.get("message", "Management request failed"))
        return data.get("data")

    channel = manage("/api/channel/16")
    assert channel["type"] == 1000 and channel["base_url"].rstrip("/") == "https://aimodel.szhtp.com"
    assert channel["status"] == 1 and channel["group"] == "default"
    price = manage("/api/option/model_pricing?model=" + MODEL)["entries"][0]
    if args.phase == "inspect":
        upstream, _ = request("https://aimodel.szhtp.com/api/pricing")
        tariff = next(row for row in upstream["data"] if row["model_name"] == UPSTREAM)
        assert tariff["billing_expr"] == EXPECTED_TARIFF
        assert upstream["group_ratio"]["default"] == 1
        manage("/api/option/model_pricing/preview", "POST", {
            "model_name": MODEL, "pricing": {"billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": expression}})
        print(json.dumps({"channel_id": 16, "existing_models": channel["models"], "upstream_tariff": tariff, "preview": "passed"}, ensure_ascii=False))
        return
    if args.phase == "apply":
        assert not state_path.exists(), "State already exists; inspect instead of applying twice"
        assert MODEL not in channel["models"].split(",") and not price["configured"]
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        backup = pathlib.Path("/opt/new-api/deploy/backups/seedream-lite-" + stamp)
        backup.mkdir(mode=0o700)
        channel.pop("key", None)
        state = {"backup": str(backup), "before_channel": channel, "before_price": price,
                 "expression": expression, "started_at": stamp}
        state_path.write_text(json.dumps(state, ensure_ascii=False, indent=2))
        (backup / "affected-config.json").write_text(json.dumps(state, ensure_ascii=False, indent=2))
        with (backup / "newapi.dump").open("wb") as output:
            subprocess.run(["nice", "-n", "15", "docker", "exec", PG, "sh", "-c",
                            'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -Z0'], stdout=output, check=True)
        with (backup / "newapi.dump").open("rb") as source:
            digest = hashlib.file_digest(source, "sha256").hexdigest()
        (backup / "newapi.dump.sha256").write_text(digest + "  newapi.dump\n")
        with (backup / "newapi.dump").open("rb") as source:
            subprocess.run(["docker", "exec", "-i", PG, "pg_restore", "--list"],
                           stdin=source, stdout=subprocess.DEVNULL, check=True)
        manage("/api/option/model_pricing", "PATCH", {"changes": [{
            "model_name": MODEL, "expected_version": price["version"],
            "pricing": {"billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": expression}}]})
        manage("/api/models/", "POST", {"model_name": MODEL, "vendor_id": 1, "icon": "Doubao.Color",
               "status": 1, "sync_official": 0, "name_rule": 0,
               "endpoints": json.dumps({"image-generation": {"path": "/v1/images/generations", "method": "POST"}}),
               "description": "深圳电信 Seedream 5.0 Lite 图片生成；上游按输入、输出及缓存 Token 分档计费"})
        # Publish the route last, after its billing contract is persisted.
        updated = dict(channel)
        updated.pop("status", None)
        updated["models"] += "," + MODEL
        mapping = json.loads(updated.get("model_mapping") or "{}")
        mapping[MODEL] = UPSTREAM
        updated["model_mapping"] = json.dumps(mapping)
        manage("/api/channel/", "PUT", updated)
        state["applied"] = True
        state_path.write_text(json.dumps(state, ensure_ascii=False, indent=2))
        print(json.dumps({"applied": True, "backup": str(backup), "backup_sha256": digest, "channel": 16, "model": MODEL}))
        return
    state = json.loads(state_path.read_text())
    assert state.get("applied")
    if args.phase == "verify":
        assert MODEL in channel["models"].split(",")
        assert json.loads(channel["model_mapping"])[MODEL] == UPSTREAM
        assert price["effective"]["billing_setting.billing_expr"] == expression
        old = state["before_channel"]
        assert set(channel["models"].split(",")) - {MODEL} == set(old["models"].split(","))
        for key, value in json.loads(old["model_mapping"]).items():
            assert json.loads(channel["model_mapping"])[key] == value
        public, _ = request("/api/pricing")
        row = next(row for row in public["data"] if row["model_name"] == MODEL)
        assert row["billing_expr"] == expression
        assert row["vendor_id"] == 1 and "default" in row["enable_groups"]
        print(json.dumps({"verified": True, "model_pricing": row, "existing_video_routes": "preserved"}, ensure_ascii=False))
        return
    assert "smoke_started" not in state, "Already submitted; inspect recorded request, never resubmit blindly"
    token_name = "seedream-lite-smoke-" + str(int(time.time()))
    manage("/api/token/", "POST", {"name": token_name, "expired_time": int(time.time())+900,
           "remain_quota": 100000, "unlimited_quota": False, "model_limits_enabled": True,
           "model_limits": MODEL, "group": "default"})
    tokens = manage("/api/token/search?keyword=" + token_name)["items"]
    token_id = next(token["id"] for token in tokens if token["name"] == token_name)
    key = manage(f"/api/token/{token_id}/key", "POST")["key"]
    state["smoke_started"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    state["test_token_id"] = token_id
    state_path.write_text(json.dumps(state, ensure_ascii=False, indent=2))
    try:
        started = time.monotonic()
        response, headers = request("/v1/images/generations", "POST", {
            "model": MODEL, "prompt": "A simple blue circle on a white background.",
            "n": 1, "size": "2K", "response_format": "url", "watermark": True,
        }, {"Authorization": "Bearer " + key + "-16"}, timeout=180)
        assert len(response.get("data", [])) == 1 and response["data"][0].get("url")
        state["smoke"] = {"seconds": round(time.monotonic()-started, 3), "usage": response.get("usage"),
                           "request_id": headers.get("X-Request-Id") or headers.get("X-Request-ID"),
                           "image_count": 1, "size": response["data"][0].get("size"), "response_model": response.get("model")}
        state_path.write_text(json.dumps(state, ensure_ascii=False, indent=2))
        media = urllib.request.Request(response["data"][0]["url"], headers={"Range": "bytes=0-255"})
        with urllib.request.urlopen(media, timeout=30) as image:
            prefix = image.read(256)
            state["smoke"]["media"] = {"content_type": image.headers.get("Content-Type"), "bytes": len(prefix),
                "valid_magic": prefix.startswith(b"\x89PNG\r\n\x1a\n") or prefix.startswith(b"\xff\xd8\xff") or prefix[8:12] == b"WEBP"}
        assert state["smoke"]["media"]["valid_magic"]
        for _ in range(12):
            logs = query(f"SELECT row_to_json(t) FROM (SELECT request_id,upstream_request_id,channel_id,model_name,quota,prompt_tokens,completion_tokens,other FROM logs WHERE token_id={int(token_id)} AND type=2 ORDER BY id DESC LIMIT 1) t")
            if logs:
                break
            time.sleep(5)
        assert len(logs) == 1, "Consume log not yet visible; inspect instead of resubmitting"
        log = logs[0]
        assert log["channel_id"] == 16 and log["model_name"] == MODEL
        other = json.loads(log["other"] or "{}")
        assert other.get("upstream_model_name") == UPSTREAM
        assert log["completion_tokens"] == state["smoke"]["usage"]["output_tokens"]
        assert log["prompt_tokens"] == 0, "Unexpected input usage; inspect actual tier/cache before accepting charge"
        expected = int(log["completion_tokens"] * 3.6 / 7.3 / 2 + 0.5)
        assert log["quota"] == expected, f"Quota differs from base tariff: {log['quota']} != {expected}"
        token = query(f"SELECT row_to_json(t) FROM (SELECT used_quota,remain_quota FROM tokens WHERE id={int(token_id)}) t")[0]
        assert token["used_quota"] == log["quota"] and token["remain_quota"] == 100000-log["quota"]
        state["smoke"]["billing"] = {"request_id": log["request_id"], "upstream_request_id": log["upstream_request_id"],
            "channel_id": 16, "upstream_model": other["upstream_model_name"], "quota": log["quota"],
            "amount_cny": log["quota"] / 500000 * 7.3, "expected_base_cny": log["completion_tokens"] * 3.6 / 1e6,
            "token_balance_verified": True,
            "snapshot": {k: v for k, v in other.items() if k.startswith("billing_") or k in ("group_ratio", "matched_tier")}}
        print(json.dumps(state["smoke"], ensure_ascii=False))
    finally:
        state_path.write_text(json.dumps(state, ensure_ascii=False, indent=2))
        manage(f"/api/token/{token_id}", "DELETE")


if __name__ == "__main__":
    main()
