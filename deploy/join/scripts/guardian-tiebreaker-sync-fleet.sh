#!/usr/bin/env bash
# Build guardian-tiebreaker-fleet.env from hosts.inventory + live chain participants.
#
# Matches each inventory host to a chain participant via inference URL port
# (api_port column). Writes deploy/join/scripts/guardian-tiebreaker-fleet.env
# which guardian-tiebreaker-config.sh auto-sources when present.
#
# Usage:
#   ./guardian-tiebreaker-sync-fleet.sh              # print plan + write fleet.env
#   DRY_RUN=1 ./guardian-tiebreaker-sync-fleet.sh    # print only, no write
#
# Optional env:
#   CHAIN_ID=gonka-testnet-4
#   INVENTORY=../hosts.inventory
#   FLEET_ROLES_JSON=../guardian-tiebreaker-fleet-roles.json
#   SSH_HOST=decentai@xj7-5.s.filfox.io
#   FLEET_HOST_IDS=702111,702112,...  (optional filter; default: all inventory rows for CHAIN_ID)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
INVENTORY="${INVENTORY:-$JOIN_DIR/hosts.inventory}"
FLEET_ENV="${FLEET_ENV:-$SCRIPT_DIR/guardian-tiebreaker-fleet.env}"
FLEET_ROLES_JSON="${FLEET_ROLES_JSON:-$SCRIPT_DIR/guardian-tiebreaker-fleet-roles.json}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-4}"
SSH_HOST="${SSH_HOST:-decentai@xj7-5.s.filfox.io}"
DRY_RUN="${DRY_RUN:-0}"
FLEET_HOST_IDS="${FLEET_HOST_IDS:-}"

if [[ ! -f "$INVENTORY" ]]; then
  echo "ERROR: inventory not found: $INVENTORY" >&2
  exit 1
fi
if [[ ! -f "$FLEET_ROLES_JSON" ]]; then
  echo "ERROR: fleet roles map not found: $FLEET_ROLES_JSON" >&2
  exit 1
fi

# Discover observer port from guardian host in inventory (first SSH query)
OBSERVER_PORT="${OBSERVER_PORT:-$(python3 - <<'PY' "$INVENTORY" "$CHAIN_ID" "$FLEET_ROLES_JSON"
import json, sys
inventory_path, chain_id, roles_path = sys.argv[1:4]
with open(roles_path, encoding="utf-8") as f:
    roles = json.load(f)
guardian_id = next(h for h, r in roles.items() if r == "guardian")
with open(inventory_path, encoding="utf-8") as f:
    for line in f:
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        cols = line.split("|")
        if len(cols) < 8:
            continue
        host_id, _role, _user, _host, ssh_port, _api, _deploy, inv_chain = cols[:8]
        if inv_chain == chain_id and host_id == guardian_id:
            print(ssh_port)
            break
PY
)}"

participants_json="$(ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
  "docker exec node inferenced query inference list-participant --home /root/.inference -o json")"
params_json="$(ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
  "docker exec node inferenced query inference params --home /root/.inference -o json")"
validators_json="$(ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
  "docker exec node inferenced query staking validators --home /root/.inference --output json" 2>/dev/null \
  || echo '{"validators":[]}')"

plan="$(INVENTORY="$INVENTORY" CHAIN_ID="$CHAIN_ID" FLEET_HOST_IDS="$FLEET_HOST_IDS" \
  FLEET_ROLES_JSON="$FLEET_ROLES_JSON" SSH_HOST="$SSH_HOST" \
  python3 - <<'PY' "$participants_json" "$params_json" "$validators_json"
import json, os, sys, re
from urllib.parse import urlparse

participants = json.loads(sys.argv[1])["participant"]
params = json.loads(sys.argv[2])["params"]
validators_payload = json.loads(sys.argv[3])
inventory_path = os.environ["INVENTORY"]
chain_id = os.environ["CHAIN_ID"]
roles_path = os.environ["FLEET_ROLES_JSON"]
fleet_filter = os.environ.get("FLEET_HOST_IDS", "").strip()

with open(roles_path, encoding="utf-8") as f:
    role_by_id = json.load(f)

hosts = {}
with open(inventory_path, encoding="utf-8") as f:
    for line in f:
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        cols = line.split("|")
        if len(cols) < 8:
            continue
        host_id, inv_role, _user, _host, ssh_port, api_port, _deploy, inv_chain = cols[:8]
        if inv_chain != chain_id:
            continue
        if host_id not in role_by_id:
            continue
        if fleet_filter and host_id not in set(fleet_filter.split(",")):
            continue
        key_name = "genesis" if inv_role == "genesis" else f"join-{ssh_port}"
        hosts[host_id] = {
            "ssh_port": ssh_port,
            "api_port": str(api_port),
            "inventory_role": inv_role,
            "key_name": key_name,
        }

expected_ids = set(role_by_id)
if fleet_filter:
    expected_ids = expected_ids & set(fleet_filter.split(","))
missing_hosts = expected_ids - set(hosts)
if missing_hosts:
    print(f"ERROR missing inventory rows for: {', '.join(sorted(missing_hosts))}", file=sys.stderr)
    sys.exit(1)

# api_port -> participant address
port_to_participant = {}
for p in participants:
    url = p.get("inference_url") or ""
    m = re.search(r":(\d+)\s*$", url)
    if not m:
        continue
    port_to_participant[m.group(1)] = p["index"]

errors = []
mapping = {}
for host_id in sorted(hosts, key=lambda x: int(x)):
    api_port = hosts[host_id]["api_port"]
    participant = port_to_participant.get(api_port)
    if not participant:
        errors.append(f"no participant for host {host_id} api_port {api_port}")
        continue
    mapping[host_id] = {
        **hosts[host_id],
        "participant": participant,
        "logical_role": role_by_id.get(host_id, "unknown"),
    }

if errors:
    for e in errors:
        print(f"ERROR {e}", file=sys.stderr)
    print(f"known inference ports: {sorted(port_to_participant)}", file=sys.stderr)
    sys.exit(1)

guardian_valopers = params.get("genesis_guardian_params", {}).get("guardian_addresses") or []
guardian_valoper = guardian_valopers[0] if guardian_valopers else ""
guardian_valoper_source = "params" if guardian_valoper else ""

def pick(role):
    for hid, row in mapping.items():
        if row["logical_role"] == role:
            return hid, row
    return None, None

_, guardian = pick("guardian")
_, victim = pick("preferred_victim")
_, invalid = pick("preferred_invalid")
_, valid = pick("preferred_valid")

if not guardian_valoper and guardian:
    gp = guardian["participant"]
    bonded = [
        v for v in validators_payload.get("validators", [])
        if v.get("status") in ("BOND_STATUS_BONDED", 3, "3")
    ]
    # Match guardian host's participant to its valoper (shared bech32 payload prefix).
    gp_core = gp[5:39] if len(gp) >= 39 else gp[5:]
    for v in bonded:
        op = v.get("operator_address") or ""
        op_core = op[12:46] if len(op) >= 46 else op[12:]
        if gp_core and op_core and gp_core == op_core:
            guardian_valoper = op
            guardian_valoper_source = "staking_guardian_participant"
            break
    if not guardian_valoper and len(bonded) == 1:
        guardian_valoper = bonded[0].get("operator_address") or ""
        guardian_valoper_source = "staking_sole_bonded"

for name, row in [("guardian", guardian), ("preferred_victim", victim),
                  ("preferred_invalid", invalid), ("preferred_valid", valid)]:
    if not row:
        print(f"ERROR could not resolve host for {name}", file=sys.stderr)
        sys.exit(1)

victim_snip = victim["participant"][:13]

victim_host_id = [h for h, r in mapping.items() if r["logical_role"] == "preferred_victim"][0]
invalid_host_id = [h for h, r in mapping.items() if r["logical_role"] == "preferred_invalid"][0]
valid_host_id = [h for h, r in mapping.items() if r["logical_role"] == "preferred_valid"][0]
guardian_host_id = [h for h, r in mapping.items() if r["logical_role"] == "guardian"][0]

lines = {
    "SSH_HOST": os.environ.get("SSH_HOST", "decentai@xj7-5.s.filfox.io"),
    "OBSERVER_PORT": guardian["ssh_port"],
    "GUARDIAN_SSH_PORT": guardian["ssh_port"],
    "GUARDIAN_HOST_ID": guardian_host_id,
    "VICTIM_SSH_PORT": victim["ssh_port"],
    "GUARDIAN_VALOPER": guardian_valoper,
    "GUARDIAN_PARTICIPANT": guardian["participant"],
    "VICTIM": victim["participant"],
    "VICTIM_SNIP": victim_snip,
    "PREFERRED_VICTIM_PARTICIPANT": victim["participant"],
    "PREFERRED_INVALID_PARTICIPANT": invalid["participant"],
    "PREFERRED_VALID_PARTICIPANT": valid["participant"],
    "VICTIM_HOST_ID": victim_host_id,
    "VOTE_INVALID_PORT": invalid["ssh_port"],
    "VOTE_INVALID_KEY": invalid["key_name"],
    "VOTE_INVALID_HOST_ID": invalid_host_id,
    "VOTE_VALID_PORT": valid["ssh_port"],
    "VOTE_VALID_KEY": valid["key_name"],
    "VOTE_VALID_HOST_ID": valid_host_id,
    "VOTE_GUARDIAN_PORT": guardian["ssh_port"],
    "VOTE_GUARDIAN_KEY": guardian["key_name"],
}

host_ids = sorted(mapping.keys(), key=int)
ports = [mapping[h]["ssh_port"] for h in host_ids]

print("# generated by guardian-tiebreaker-sync-fleet.sh — do not edit by hand")
print(f"# chain_id={chain_id} inventory={inventory_path}")
if guardian_valoper_source and guardian_valoper_source != "params":
    print(f"# NOTE GUARDIAN_VALOPER from {guardian_valoper_source} (genesis_guardian_params still empty until gov passes)")
for hid in host_ids:
    r = mapping[hid]
    print(f"#   {hid} :{r['ssh_port']} api:{r['api_port']} {r['logical_role']} -> {r['participant']}")
print()
for k, v in lines.items():
    print(f'{k}="{v}"')
print(f"ALL_API_PORTS=({' '.join(ports)})")
print(f"HOST_IDS=({' '.join(host_ids)})")

fleet = []
for hid in host_ids:
    r = mapping[hid]
    fleet.append({
        "host": hid,
        "port": int(r["ssh_port"]),
        "key": r["key_name"],
        "participant": r["participant"],
        "role": "guardian" if r["logical_role"] == "guardian" else r["logical_role"],
    })
import json as _json
print(f"FLEET_JSON='{_json.dumps(fleet)}'")
PY
)"

echo "$plan"
echo

if [[ "$DRY_RUN" == "1" ]]; then
  echo "DRY_RUN=1 — not writing $FLEET_ENV"
  exit 0
fi

{
  echo "# shellcheck shell=bash"
  echo "$plan"
} > "$FLEET_ENV"

echo "Wrote $FLEET_ENV"
echo "Config will auto-source it on next orchestrator/preflight run."
