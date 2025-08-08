#!/usr/bin/env bash
# build_authz_grants_address_pairs.sh
# Reads pairs from DIR using files:
#   address-0.txt         (line1 = granter / cold)
#   address-0-warm.txt    (line1 = grantee / warm)
#   address-1.txt
#   address-1-warm.txt
# …etc.
#
# Merges with a list of message types (full type URLs or bare names + --namespace)
# to produce app_state.authz.authorization in authz_grants.json.

set -euo pipefail
shopt -s nullglob

PAIRS_DIR=""
MSGS_FILE=""
OUT_FILE="authz_grants.json"
NAMESPACE=""
EXPLICIT_EXP=""
DAYS="365"

# Naming convention (your final choice)
COLD_PREFIX="address-"
COLD_SUFFIX=".txt"
WARM_SUFFIX="-warm.txt"

usage() {
  cat <<EOF
Usage: $0 --dir DIR --msgs FILE [--out FILE] [--namespace PKG.PATH.V1] [--days N | --expiration RFC3339]
  --dir,  -d       Directory with address-N.txt + address-N-warm.txt files
  --msgs, -m       File with message types (one per line):
                   - Full type URLs:   /pkg.path.v1.MsgFoo
                   - or bare names:    MsgFoo   (then pass --namespace)
  --namespace      Protobuf package for bare names (e.g., inferenced.inference.v1)
  --out,  -o       Output JSON (default: authz_grants.json)
  --days           Expiration offset in days from now (default: 365). Ignored if --expiration is set.
  --expiration, -e RFC3339 timestamp (e.g., 2026-08-08T00:00:00Z). Overrides --days.
  --help,  -h      This help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -d|--dir)        PAIRS_DIR="$2"; shift 2 ;;
    -m|--msgs)       MSGS_FILE="$2"; shift 2 ;;
    -o|--out)        OUT_FILE="$2"; shift 2 ;;
    --namespace)     NAMESPACE="$2"; shift 2 ;;
    --days)          DAYS="$2"; shift 2 ;;
    -e|--expiration) EXPLICIT_EXP="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown arg: $1"; usage; exit 1 ;;
  esac
done

[[ -z "$PAIRS_DIR" || -z "$MSGS_FILE" ]] && { echo "Error: --dir and --msgs are required."; usage; exit 1; }
command -v jq >/dev/null || { echo "Error: jq is required."; exit 1; }

# --- expiration (RFC3339 Z) ---
if [[ -n "$EXPLICIT_EXP" ]]; then
  if date -u -d "$EXPLICIT_EXP" "+%Y-%m-%dT%H:%M:%SZ" >/dev/null 2>&1; then
    EXPIRATION="$(date -u -d "$EXPLICIT_EXP" "+%Y-%m-%dT%H:%M:%SZ")"
  else
    if [[ "$EXPLICIT_EXP" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
      EXPIRATION="${EXPLICIT_EXP}T00:00:00Z"
    else
      echo "WARN: couldn't normalize expiration '$EXPLICIT_EXP'; using as-is"
      EXPIRATION="$EXPLICIT_EXP"
    fi
  fi
else
  if date -u -d "+${DAYS} days" "+%Y-%m-%dT%H:%M:%SZ" >/dev/null 2>&1; then
    EXPIRATION="$(date -u -d "+${DAYS} days" "+%Y-%m-%dT%H:%M:%SZ")"
  else
    EXPIRATION="2099-12-31T00:00:00Z"
    echo "WARN: 'date -d' unavailable; using $EXPIRATION"
  fi
fi

# --- load messages ---
mapfile -t MSGS_RAW < <(grep -v '^\s*#' "$MSGS_FILE" | sed '/^\s*$/d')
declare -a MSGS=()
for m in "${MSGS_RAW[@]}"; do
  m="$(echo "$m" | xargs)"
  if [[ "$m" == /* ]]; then
    MSGS+=("$m")
  else
    [[ -z "$NAMESPACE" ]] && { echo "ERROR: '$m' is not a type URL and --namespace not provided"; exit 1; }
    MSGS+=("/${NAMESPACE}.${m}")
  fi
done
[[ ${#MSGS[@]} -gt 0 ]] || { echo "ERROR: no messages found"; exit 1; }

TMP="$(mktemp)"
echo '{"app_state":{"authz":{"authorization":[]}}}' > "$TMP"

# --- discover cold files (exclude warm ones) ---
ALL_TXT=( "$PAIRS_DIR"/${COLD_PREFIX}*${COLD_SUFFIX} )
COLD_FILES=()
for f in "${ALL_TXT[@]}"; do
  [[ "$(basename "$f")" == *"$WARM_SUFFIX" ]] && continue  # skip warm files
  COLD_FILES+=( "$f" )
done
[[ ${#COLD_FILES[@]} -gt 0 ]] || { echo "ERROR: no '${COLD_PREFIX}N${COLD_SUFFIX}' files in '$PAIRS_DIR'"; rm -f "$TMP"; exit 1; }

# --- build grants ---
for cold in "${COLD_FILES[@]}"; do
  base="$(basename "$cold")"                  # e.g., address-12.txt
  idx="${base#${COLD_PREFIX}}"                # e.g., 12.txt
  idx="${idx%${COLD_SUFFIX}}"                 # e.g., 12
  warm="$PAIRS_DIR/${COLD_PREFIX}${idx}${WARM_SUFFIX}"  # address-12-warm.txt

  if [[ ! -f "$warm" ]]; then
    echo "WARN: missing warm file for index '$idx' (expected '$warm') — skipping"
    continue
  fi

  granter="$(grep -v '^\s*#' "$cold" | sed '/^\s*$/d' | sed -n '1p' | xargs || true)"
  grantee="$(grep -v '^\s*#' "$warm" | sed '/^\s*$/d' | sed -n '1p' | xargs || true)"

  if [[ -z "$granter" || -z "$grantee" ]]; then
    echo "WARN: empty granter/grantee in index '$idx' — skipping"
    continue
  fi

  for msg in "${MSGS[@]}"; do
    jq --arg granter "$granter" \
       --arg grantee "$grantee" \
       --arg msg "$msg" \
       --arg exp "$EXPIRATION" \
       '.app_state.authz.authorization += [{
          "granter": $granter,
          "grantee": $grantee,
          "authorization": { "@type": "/cosmos.authz.v1beta1.GenericAuthorization", "msg": $msg },
          "expiration": $exp
        }]' "$TMP" > "${TMP}.next"
    mv "${TMP}.next" "$TMP"
  done
done

# de-dup by (granter, grantee, msg)
jq '.app_state.authz.authorization |= unique_by([.granter,.grantee,.authorization.msg])' "$TMP" > "$OUT_FILE"
rm -f "$TMP"
echo "Wrote $OUT_FILE"
