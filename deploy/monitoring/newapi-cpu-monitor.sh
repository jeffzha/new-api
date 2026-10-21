#!/usr/bin/env bash
set -euo pipefail

# This monitor only reports. It never stops or restarts a container.
THRESHOLD=${THRESHOLD:-80}
RECOVERY_THRESHOLD=${RECOVERY_THRESHOLD:-70}
STATE=${STATE:-/var/lib/newapi-cpu-monitor/state}
ENV_FILE=${ENV_FILE:-/etc/newapi-disk-monitor.env}
SAMPLE_SECONDS=${SAMPLE_SECONDS:-5}
test_only=false
case "${1:-}" in
  --test) test_only=true ;;
  '') ;;
  *) echo "usage: $0 [--test]" >&2; exit 2 ;;
esac

if [[ -r "$ENV_FILE" ]]; then
  # shellcheck disable=SC1090
  . "$ENV_FILE"
fi
: "${WEBHOOK:?WEBHOOK is not configured}"

mkdir -p "$(dirname "$STATE")"
exec 9>"${STATE}.lock"
flock -n 9 || exit 0

read_cpu() {
  # Guest time is already included in user/nice; do not count it twice.
  awk '/^cpu / { idle=$5+$6; total=0; for (i=2; i<=9; i++) total += $i; printf "%.0f %.0f\n", total, idle; exit }' /proc/stat
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

top_services=$( { timeout 15 docker stats --no-stream --format '{{.Name}}\t{{.CPUPerc}}' 2>/dev/null || true; } \
  | awk -F '\t' '
      { value=$2; gsub(/%/, "", value); if (value+0 >= 0) print value+0 "\t" $1 }
    ' | sort -rn | sed -n '1,5p')
top=$(printf '%s\n' "$top_services" | sed -n '1p')
top_cpu=$(printf '%s' "$top" | cut -f1)
top_name=$(printf '%s' "$top" | cut -f2-)
if [[ -z "$top_name" ]]; then
  top_name="未读取到容器"
  top_cpu="未知"
fi

state=$(cat "$STATE" 2>/dev/null || printf 'ok')
host=$(hostname)
if { (( usage >= THRESHOLD )) && [[ "$state" != alert ]]; } || "$test_only"; then
  cores=$(nproc)
  details=$(printf '%s\n' "$top_services" | awk -F '\t' -v cores="$cores" 'NF==2 {printf "%s：Docker %s%%，约整机 %.1f%%\n", $2, $1, $1/cores}')
  processes=$(ps -eo pid,comm,pcpu --sort=-pcpu | sed -n '1,6p')
  advice="先执行 docker stats --no-stream 查看实时占用。数据库高占用应检查慢查询、连接数和批处理；网关高占用应检查请求量、日志和任务轮询，先限制作业并发再评估扩容。不要直接停止数据库、Redis、网关、代理或未知服务。"
  case "$top_name" in
    tokenops|tokenops-worker|tokenops-cn-collector-1)
      advice="${advice}
最高占用为运营平台组件。如确认可以暂停运营平台对应功能，经负责人同意后可执行：
docker stop ${top_name}
恢复命令：docker start ${top_name}
影响：对应运营页面、后台任务或数据采集将中断，恢复后须检查补采及账务同步；不要在业务进行中盲目停止。" ;;
  esac
  title="【服务器 CPU 超标】new-api"
  if "$test_only"; then
    title="【CPU 告警通路测试】不是超标告警；下方为实时采样，不改变告警状态"
  fi
  text="${title}
服务器：${host}
时间：$(date -Is)
整机 CPU：${usage}%（阈值 ${THRESHOLD}%，${cores} 核，采样 ${SAMPLE_SECONDS} 秒）

容器占用排名（Docker 100% = 约一个核，不是整机 100%）：
${details:-未读取到容器占用，请检查 Docker 状态}

进程参考（CPU 为进程存活期均值，仅辅助定位，不含命令参数）：
${processes}

处理建议：
${advice}

只告警，不会自动停服务。持续超标不重复发送；低于 ${RECOVERY_THRESHOLD}% 后重新启用下一次告警。"
  payload=$(python3 -c 'import json,sys; print(json.dumps({"msg_type":"text","content":{"text":sys.argv[1]}},ensure_ascii=False))' "$text")
  response=$(curl -fsS --max-time 15 -H 'Content-Type: application/json' -d "$payload" "$WEBHOOK")
  printf '%s' "$response" | python3 -c 'import json,sys; r=json.load(sys.stdin); sys.exit(0 if r.get("code") == 0 else 1)' || { echo "Feishu rejected CPU alert" >&2; exit 1; }
  if ! "$test_only"; then
    printf 'alert' > "$STATE"
  fi
  printf 'CPU alert sent: host=%s cpu=%s%% top=%s(%s%%)\n' "$host" "$usage" "$top_name" "$top_cpu"
elif (( usage < RECOVERY_THRESHOLD )) && [[ "$state" == alert ]]; then
  printf 'ok' > "$STATE"
  printf 'CPU recovered: host=%s cpu=%s%%\n' "$host" "$usage"
else
  printf '%s' "$state" > "$STATE"
  printf 'CPU sample: host=%s cpu=%s%% top=%s(%s%%) state=%s\n' "$host" "$usage" "$top_name" "$top_cpu" "$state"
fi
