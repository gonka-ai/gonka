#!/usr/bin/env bash
# Grep tiebreaker verdict lines from all four validator nodes.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/guardian-tiebreaker-config.sh"

guardian_tiebreaker_require_fleet

PATTERN='Guardian tiebreaker|No majority|guardians split|guardianValidCount|guardianInvalidCount|No voting powers for model|ComputeNewWeights: Final summary|Calculate: Valid majority|Calculate: Invalid majority'

fleet_lines=()
while IFS= read -r line; do
  fleet_lines+=("$line")
done < <(FLEET_JSON="$FLEET_JSON" python3 - <<'PY'
import json, os
for row in json.loads(os.environ["FLEET_JSON"]):
    print(f"{row['host']}|{row['port']}")
PY
)

for entry in "${fleet_lines[@]}"; do
  id="${entry%%|*}"
  port="${entry##*|}"
  echo "========== $id (port $port) =========="
  ssh -o BatchMode=yes -p "$port" "$SSH_HOST" \
    "docker logs node 2>&1 | grep -Ei '${PATTERN}' | grep -Ei '${VICTIM_SNIP}|Qwen3-4B|Guardian tiebreaker|No majority|No voting powers' | tail -25" \
    || echo "(no matches)"
  echo
done
