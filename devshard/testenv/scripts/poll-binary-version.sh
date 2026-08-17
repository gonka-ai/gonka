#!/usr/bin/env bash
# Poll GET /stats/shards for protocol_version + binary_version during a same-name
# rolling update. Protocol stays (e.g. v4); binary_version should drift from the
# pre-flip stamp (e.g. 0.2.14-v4) to the new stamp (e.g. 0.2.14-v4-r2) as
# versiond hosts publish the new SHA.
#
# Usage:
#   ./poll-binary-version.sh
#   STATS_URL=http://127.0.0.1:8000/devshard/v4/stats/shards ./poll-binary-version.sh
#   ./poll-binary-version.sh --url http://127.0.0.1:8080/v4/stats/shards --interval 1
#   ./poll-binary-version.sh --expect-from 0.2.14-v4 --expect-to 0.2.14-v4-r2
#
# Requires: curl, jq
set -euo pipefail

STATS_URL="${STATS_URL:-http://127.0.0.1:8000/devshard/stats/shards}"
INTERVAL_SECS="${INTERVAL_SECS:-1}"
EXPECT_FROM="${EXPECT_FROM:-}"
EXPECT_TO="${EXPECT_TO:-}"
MAX_SAMPLES=0

usage() {
  sed -n '2,16p' "$0" | sed 's/^# \?//'
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --url)
      STATS_URL="${2:?}"
      shift 2
      ;;
    --interval)
      INTERVAL_SECS="${2:?}"
      shift 2
      ;;
    --expect-from)
      EXPECT_FROM="${2:?}"
      shift 2
      ;;
    --expect-to)
      EXPECT_TO="${2:?}"
      shift 2
      ;;
    --max)
      MAX_SAMPLES="${2:?}"
      shift 2
      ;;
    -h|--help)
      usage
      ;;
    *)
      echo "unknown arg: $1" >&2
      usage
      ;;
  esac
done

command -v jq >/dev/null || { echo "jq required" >&2; exit 1; }
command -v curl >/dev/null || { echo "curl required" >&2; exit 1; }

HIST="$(mktemp)"
n=0
saw_from=0
saw_to=0

echo "polling ${STATS_URL} every ${INTERVAL_SECS}s (Ctrl-C to stop)"
echo "ts	http	protocol_version	binary_version"

cleanup() {
  echo
  echo "--- binary_version histogram ---"
  if [[ ! -s "${HIST}" ]]; then
    echo "(no successful samples)"
    rm -f "${HIST}"
    exit 1
  fi
  sort "${HIST}" | uniq -c | sort -nr
  if [[ -n "${EXPECT_FROM}" && "${saw_from}" -eq 0 ]]; then
    echo "FAIL: never saw expect-from binary_version=${EXPECT_FROM}" >&2
    rm -f "${HIST}"
    exit 1
  fi
  if [[ -n "${EXPECT_TO}" && "${saw_to}" -eq 0 ]]; then
    echo "FAIL: never saw expect-to binary_version=${EXPECT_TO}" >&2
    rm -f "${HIST}"
    exit 1
  fi
  if [[ -n "${EXPECT_FROM}" && -n "${EXPECT_TO}" && "${saw_from}" -eq 1 && "${saw_to}" -eq 1 ]]; then
    echo "PASS: saw drift ${EXPECT_FROM} → ${EXPECT_TO}"
  fi
  rm -f "${HIST}"
}
trap cleanup EXIT

while true; do
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  body="$(mktemp)"
  code="$(curl -sS -o "${body}" -w '%{http_code}' --max-time 5 "${STATS_URL}" || echo "000")"
  if [[ "${code}" != "200" ]]; then
    printf '%s\t%s\t-\t-\n' "${ts}" "${code}"
  else
    proto="$(jq -r '.protocol_version // .version // "-" ' "${body}")"
    bin="$(jq -r '.binary_version // "-" ' "${body}")"
    printf '%s\t%s\t%s\t%s\n' "${ts}" "${code}" "${proto}" "${bin}"
    printf '%s\n' "${bin}" >> "${HIST}"
    [[ -n "${EXPECT_FROM}" && "${bin}" == "${EXPECT_FROM}" ]] && saw_from=1
    [[ -n "${EXPECT_TO}" && "${bin}" == "${EXPECT_TO}" ]] && saw_to=1
  fi
  rm -f "${body}"
  n=$((n + 1))
  if [[ "${MAX_SAMPLES}" -gt 0 && "${n}" -ge "${MAX_SAMPLES}" ]]; then
    break
  fi
  if [[ -n "${EXPECT_FROM}" && -n "${EXPECT_TO}" && "${saw_from}" -eq 1 && "${saw_to}" -eq 1 ]]; then
    break
  fi
  sleep "${INTERVAL_SECS}"
done
