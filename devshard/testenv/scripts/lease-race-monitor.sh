#!/usr/bin/env bash
# Analyze (or poll) devshard_validation_leases for HA exclusivity.
#
# Usage (from testenv/ with stack up):
#   ./scripts/lease-race-monitor.sh                 # one-shot PASS/FAIL
#   ./scripts/lease-race-monitor.sh --watch 1       # poll every 1s until Ctrl-C, then final verdict
#   ./scripts/lease-race-monitor.sh --min-leases 5  # require ≥5 lease rows
#
# Exit 0 = PASS, 1 = FAIL.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

WATCH_SECS=0
MIN_LEASES=1
COMPOSE=(docker compose)
PGUSER="${PGUSER:-devshardd}"
PGDATABASE="${PGDATABASE:-devshardd}"
PGPASSWORD="${PGPASSWORD:-devshardd}"

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \?//'
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --watch)
      WATCH_SECS="${2:?}"
      shift 2
      ;;
    --min-leases)
      MIN_LEASES="${2:?}"
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

psql_at() {
  local sql="$1"
  "${COMPOSE[@]}" exec -T -e "PGPASSWORD=${PGPASSWORD}" devshard-postgres \
    psql -U "$PGUSER" -d "$PGDATABASE" -At -c "$sql"
}

psql_table() {
  local sql="$1"
  "${COMPOSE[@]}" exec -T -e "PGPASSWORD=${PGPASSWORD}" devshard-postgres \
    psql -U "$PGUSER" -d "$PGDATABASE" -c "$sql"
}

analyze_once() {
  local dup_count lease_count pending submitted skipped
  dup_count="$(psql_at "SELECT COUNT(*) FROM (
      SELECT epoch_id, escrow_id, inference_id
      FROM devshard_validation_leases
      GROUP BY 1, 2, 3
      HAVING COUNT(*) > 1
    ) d")"
  lease_count="$(psql_at "SELECT COUNT(*) FROM devshard_validation_leases")"
  pending="$(psql_at "SELECT COUNT(*) FROM devshard_validation_leases WHERE status = 'pending'")"
  submitted="$(psql_at "SELECT COUNT(*) FROM devshard_validation_leases WHERE status = 'submitted'")"
  skipped="$(psql_at "SELECT COUNT(*) FROM devshard_validation_leases WHERE status = 'skipped'")"

  echo "--- lease snapshot ---"
  echo "total=${lease_count} pending=${pending} submitted=${submitted} skipped=${skipped} duplicate_groups=${dup_count}"
  psql_table "SELECT inference_id, instance_address, status, claimed_at
              FROM devshard_validation_leases
              ORDER BY claimed_at DESC LIMIT 20;" || true

  if [[ "${dup_count}" != "0" ]]; then
    echo "FAIL: duplicate (epoch_id, escrow_id, inference_id) groups=${dup_count}"
    return 1
  fi
  if [[ "${lease_count}" -lt "${MIN_LEASES}" ]]; then
    echo "FAIL: lease rows=${lease_count} < min=${MIN_LEASES}"
    return 1
  fi
  echo "PASS: uniqueness ok; leases=${lease_count} (>= ${MIN_LEASES})"
  return 0
}

if [[ "${WATCH_SECS}" -gt 0 ]]; then
  echo "watching every ${WATCH_SECS}s (Ctrl-C for final verdict)..."
  trap 'echo; analyze_once; exit $?' INT TERM
  while true; do
    analyze_once || true
    sleep "${WATCH_SECS}"
  done
fi

analyze_once
