#!/usr/bin/env bash
set -euo pipefail

# This monitor only reports. It never stops or restarts a container.
THRESHOLD=${THRESHOLD:-80}
RECOVERY_THRESHOLD=${RECOVERY_THRESHOLD:-70}
STATE=${STATE:-/var/lib/newapi-cpu-monitor/state}
ENV_FILE=${ENV_FILE:-/etc/newapi-disk-monitor.env}
SAMPLE_SECONDS=${SAMPLE_SECONDS:-1}

if [[ -r "$ENV_FILE" ]]; then
  # shellcheck disable=SC1090
  . "$ENV_FILE"
fi
: "${WEBHOOK:?WEBHOOK is not configured}"

read_cpu() {
  awk '/^cpu / { idle=$5+$6; total=0; for (i=2; i<=NF; i++) total += $i; print total, idle; exit }' /proc/stat
}

before=$(read_cpu)
sleep "$SAMPLE_SECONDS"
after=$(read_cpu)
read -r before_total before_idle <<<"$before"
read -r after_total after_idle <<<"$after"
total_delta=$((after_total - before_total))
idle_delta=$((after_idle - before_idle))
if (( total_delta <= 0 )); then
  echo "unable to sample host CPU"
  exit 1
fi
usage=$(( (100 * (total_delta - idle_delta) + total_delta / 2) / total_delta ))

top_services=$(docker stats --no-stream --format '{{.Name}}\t{{.CPUPerc}}' 2>/dev/null \
  | awk -F '\t' '
      { value=$2; gsub(/%/, "", value); if (value+0 >= 0) print value+0 "\t" $1 }
    ' | sort -rn | head -5)
top=$(printf '%s\n' "$top_services" | head -1)
top_cpu=$(printf '%s' "$top" | cut -f1)
top_name=$(printf '%s' "$top" | cut -f2-)
if [[ -z "$top_name" ]]; then
  top_name="未读取到容器"
  top_cpu="未知"
fi

state=$(cat "$STATE" 2>/dev/null || printf 'ok')
mkdir -p "$(dirname "$STATE")"
host=$(hostname)
if (( usage >= THRESHOLD )) && [[ "$state" != alert ]]; then
  details=$(printf '%s\n' "$top_services" | awk -F '\t' '{printf "%s: %s%%; ", $2, $1}')
  advice="请先检查日志和任务，再按需执行：docker stop ${top_name}（仅示例，不会自动执行）"
  text="服务器CPU告警 [new-api] host=${host} CPU=${usage}% threshold=${THRESHOLD}% 最高容器=${top_name}(${top_cpu}%) 容器明细=${details} 处理建议=${advice}"
  payload=$(python3 -c 'import json,sys; print(json.dumps({"msg_type":"text","content":{"text":sys.argv[1]}},ensure_ascii=False))' "$text")
  response=$(curl -fsS --max-time 15 -H 'Content-Type: application/json' -d "$payload" "$WEBHOOK")
  printf '%s' "$response" | grep -q '"code":0' || { echo "Feishu rejected CPU alert" >&2; exit 1; }
  printf 'alert' > "$STATE"
  printf 'CPU alert sent: host=%s cpu=%s%% top=%s(%s%%)\n' "$host" "$usage" "$top_name" "$top_cpu"
elif (( usage < RECOVERY_THRESHOLD )) && [[ "$state" == alert ]]; then
  printf 'ok' > "$STATE"
  printf 'CPU recovered: host=%s cpu=%s%%\n' "$host" "$usage"
else
  printf '%s' "$state" > "$STATE"
  printf 'CPU sample: host=%s cpu=%s%% top=%s(%s%%) state=%s\n' "$host" "$usage" "$top_name" "$top_cpu" "$state"
fi
