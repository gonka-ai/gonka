#!/usr/bin/env bash
# Fire N random streaming /v1/chat/completions requests at a devshard gateway
# and report per-request TTFT / duration / bytes plus an aggregate summary.
#
# Works both on the gateway host (default 127.0.0.1:18080) and from a laptop
# through an SSH tunnel:
#   ssh -N -L 18080:127.0.0.1:18080 ubuntu@89.169.111.79
#
# Usage:
#   ./devshard-gateway-v3-random-stream-test.sh
#
# Env overrides:
#   HOST (127.0.0.1)  PORT (18080)  BASE_URL (http://HOST:PORT)
#   MODEL (Qwen/Qwen2.5-7B-Instruct)
#   API_KEY (sk-v3-user-key)         bearer token for the gateway
#   N (20)                           number of requests
#   CONCURRENCY (20)                 max requests in flight (N = all at once)
#   MAX_TOKENS (0)                   0 = use gateway default; >0 = cap per request
#   TIMEOUT (600)                    per-request curl timeout (seconds)
#   VERBOSE (0)                      1 = also print each generated completion
set -uo pipefail

HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-18080}"
BASE_URL="${BASE_URL:-http://${HOST}:${PORT}}"
MODEL="${MODEL:-Qwen/Qwen2.5-7B-Instruct}"
API_KEY="${API_KEY:-sk-v3-user-key}"
N="${N:-20}"
CONCURRENCY="${CONCURRENCY:-20}"
MAX_TOKENS="${MAX_TOKENS:-0}"
TIMEOUT="${TIMEOUT:-600}"
VERBOSE="${VERBOSE:-0}"

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }

PROMPTS=(
  "Write a short essay on the history of stones in literature, from early poems to today's bestselling novels."
  "Explain how a distributed proof-of-work network reaches consensus, in plain language."
  "Write a short story about a lighthouse keeper who collects forgotten sounds."
  "Summarize the causes and consequences of the invention of the printing press."
  "Describe the water cycle as if narrated by a single raindrop."
  "Compare and contrast optimism and stoicism as philosophies for daily life."
  "Write a brief guide to brewing the perfect cup of pour-over coffee."
  "Explain the significance of prime numbers in modern cryptography."
  "Write a poem about a city that only exists at dusk."
  "Describe how bees communicate the location of flowers to the hive."
  "Give a short history of cartography and how maps shaped exploration."
  "Explain the difference between machine learning and traditional programming."
  "Write a short fable where a river teaches patience to a mountain."
  "Describe the life cycle of a star from nebula to supernova."
  "Write an argument for and against colonizing Mars within this century."
  "Explain how vaccines train the immune system, using a simple analogy."
  "Write a short essay on why humans are drawn to storytelling."
  "Describe a bustling market in a fictional coastal town at sunrise."
  "Explain the basics of how blockchains store data immutably."
  "Write a reflective piece on what silence sounds like in a forest."
)

OUTDIR="$(mktemp -d "${TMPDIR:-/tmp}/gw-v3-stream.XXXXXX")"
trap 'rm -rf "$OUTDIR"' EXIT

echo "target      : $BASE_URL/v1/chat/completions"
echo "model       : $MODEL"
echo "requests    : $N (concurrency $CONCURRENCY)"
echo "max_tokens  : $([ "$MAX_TOKENS" -gt 0 ] && echo "$MAX_TOKENS" || echo "gateway default")"
echo "workdir     : $OUTDIR"
echo

do_request() {
  local i="$1" prompt="$2"
  local out="$OUTDIR/req_${i}.out"
  local ftt_file="$OUTDIR/req_${i}.ftt"
  local err_file="$OUTDIR/req_${i}.err"
  local meta="$OUTDIR/req_${i}.meta"

  local body
  if [ "$MAX_TOKENS" -gt 0 ]; then
    body=$(jq -nc --arg m "$MODEL" --arg c "$prompt" --argjson mt "$MAX_TOKENS" \
      '{model:$m, stream:true, max_tokens:$mt, messages:[{role:"user",content:$c}]}')
  else
    body=$(jq -nc --arg m "$MODEL" --arg c "$prompt" \
      '{model:$m, stream:true, messages:[{role:"user",content:$c}]}')
  fi

  local start end first=1
  start="$EPOCHREALTIME"
  curl -sS -N --max-time "$TIMEOUT" -X POST "$BASE_URL/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -d "$body" 2>>"$err_file" \
  | while IFS= read -r line; do
      case "$line" in
        "data: [DONE]") break ;;
        data:\ *)
          delta="$(printf '%s' "${line#data: }" | jq -rj '.choices[0].delta.content // empty' 2>/dev/null)"
          if [ -n "$delta" ]; then
            if [ "$first" = 1 ]; then printf '%s' "$EPOCHREALTIME" > "$ftt_file"; first=0; fi
            printf '%s' "$delta" >> "$out"
          fi
          ;;
        "") : ;;
        *) printf '%s\n' "$line" >> "$err_file" ;;   # non-SSE line (usually an error body)
      esac
    done
  end="$EPOCHREALTIME"

  local chars=0; [ -f "$out" ] && chars="$(wc -c < "$out" | tr -d ' ')"
  local ftt=""; [ -f "$ftt_file" ] && ftt="$(cat "$ftt_file")"
  local status="OK"
  if [ "$chars" -eq 0 ]; then status="FAIL"; fi
  # ttft / total via awk (float epoch seconds)
  local ttft_s total_s
  total_s="$(awk -v a="$start" -v b="$end" 'BEGIN{printf "%.2f", b-a}')"
  if [ -n "$ftt" ]; then
    ttft_s="$(awk -v a="$start" -v b="$ftt" 'BEGIN{printf "%.2f", b-a}')"
  else
    ttft_s="-"
  fi
  printf '%s\t%s\t%s\t%s\t%s\n' "$i" "$status" "$ttft_s" "$total_s" "$chars" > "$meta"
}

# ---- launch with a concurrency gate ----
running=0
for i in $(seq 1 "$N"); do
  prompt="${PROMPTS[$((RANDOM % ${#PROMPTS[@]}))]}"
  do_request "$i" "$prompt" &
  running=$((running+1))
  if [ "$running" -ge "$CONCURRENCY" ]; then
    wait -n 2>/dev/null || wait
    running=$((running-1))
  fi
done
wait

# ---- summary ----
echo "=== per-request ==="
printf '%-4s %-6s %-10s %-10s %-8s\n' "#" "status" "ttft(s)" "total(s)" "bytes"
ok=0; fail=0; sum_bytes=0; sum_total=0; sum_ttft=0; ttft_n=0
for i in $(seq 1 "$N"); do
  meta="$OUTDIR/req_${i}.meta"
  [ -f "$meta" ] || { printf '%-4s %-6s\n' "$i" "NOMETA"; fail=$((fail+1)); continue; }
  IFS=$'\t' read -r ridx rstat rttft rtot rbytes < "$meta"
  printf '%-4s %-6s %-10s %-10s %-8s\n' "$ridx" "$rstat" "$rttft" "$rtot" "$rbytes"
  if [ "$rstat" = "OK" ]; then
    ok=$((ok+1)); sum_bytes=$((sum_bytes+rbytes))
    sum_total="$(awk -v s="$sum_total" -v v="$rtot" 'BEGIN{printf "%.2f", s+v}')"
    if [ "$rttft" != "-" ]; then sum_ttft="$(awk -v s="$sum_ttft" -v v="$rttft" 'BEGIN{printf "%.2f", s+v}')"; ttft_n=$((ttft_n+1)); fi
  else
    fail=$((fail+1))
    if [ -s "$OUTDIR/req_${i}.err" ]; then
      echo "     └─ error: $(head -c 200 "$OUTDIR/req_${i}.err" | tr '\n' ' ')"
    fi
  fi
done

echo
echo "=== aggregate ==="
echo "ok=$ok  fail=$fail  total_bytes=$sum_bytes"
[ "$ok" -gt 0 ] && awk -v st="$sum_total" -v ok="$ok" 'BEGIN{printf "avg total   : %.2fs\n", st/ok}'
[ "$ttft_n" -gt 0 ] && awk -v sf="$sum_ttft" -v n="$ttft_n" 'BEGIN{printf "avg ttft    : %.2fs\n", sf/n}'

if [ "$VERBOSE" = "1" ]; then
  echo
  echo "=== completions ==="
  for i in $(seq 1 "$N"); do
    echo "----- request $i -----"
    [ -f "$OUTDIR/req_${i}.out" ] && cat "$OUTDIR/req_${i}.out"; echo
  done
fi

[ "$fail" -eq 0 ]
