#!/usr/bin/env bash
# Manual testnet checks for PR #1386 (v0.2.14 deprecations + versioned devshard).
# Run on a join host (e.g. gonka-testnet-4 / 702111) or via SSH:
#
#   ssh -p 18222 decentai@xj7-5.s.filfox.io 'bash -s' < deploy/join/scripts/pr-1386-manual-testnet-tests.sh
#   KEYRING_PASSWORD=12345678 EXPECT_410=0 bash deploy/join/scripts/pr-1386-manual-testnet-tests.sh
#
# After upgrading to 0.2.14+ set EXPECT_410=1 (default). On 0.2.13 baseline use EXPECT_410=0.

set -euo pipefail

API_BASE="${API_BASE:-http://127.0.0.1:8000}"
API_DIRECT="${API_DIRECT:-http://127.0.0.1:9200}"
CHAIN_RPC="${CHAIN_RPC:-${API_BASE}/chain-rpc/}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-4}"
EXPECT_410="${EXPECT_410:-1}"

INFERENCED="${INFERENCED:-/srv/dai/inferenced}"
INFERENCE_HOME="${INFERENCE_HOME:-/srv/dai/.inference}"
KEY_NAME="${KEY_NAME:-gonka-account-key}"
KEYRING_PASSWORD="${KEYRING_PASSWORD:-12345678}"
KEYRING_BACKEND="${KEYRING_BACKEND:-file}"

DEPRECATED_TEXT="classic inference is deprecated"
# Valid base64-encoded 64-byte r||s placeholder for classic inference msgs.
DUMMY_SIG="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
TX_TMP="${TX_TMP:-/tmp/pr1386-classic-tx}"

PASS=0
FAIL=0
SKIP=0

section() { printf '\n======== %s ========\n' "$1"; }

with_keyring() {
  printf '%s\n' "$KEYRING_PASSWORD" | "$INFERENCED" "$@" \
    --home "$INFERENCE_HOME" --keyring-backend "$KEYRING_BACKEND"
}

key_addr() {
  with_keyring keys show "$KEY_NAME" -a
}

expect_deprecated_text() {
  local id="$1" desc="$2" haystack="$3"
  local require_nonempty="${4:-0}"

  if [[ "$require_nonempty" == "1" ]] && [[ -z "$(echo "$haystack" | tr -d '[:space:]')" ]]; then
    echo "$id FAIL ($desc) no tx output captured (broadcast/query failed)"
    FAIL=$((FAIL + 1))
    return
  fi

  if [[ "$EXPECT_410" == "1" ]]; then
    if [[ "$haystack" == *"$DEPRECATED_TEXT"* ]]; then
      echo "$id PASS ($desc)"
      PASS=$((PASS + 1))
    else
      echo "$id FAIL ($desc) expected: $DEPRECATED_TEXT"
      echo "  got: $(echo "$haystack" | tr '\n' ' ' | head -c 240)"
      FAIL=$((FAIL + 1))
    fi
  else
    if [[ "$haystack" == *"$DEPRECATED_TEXT"* ]]; then
      echo "$id FAIL ($desc) unexpected deprecated message on baseline"
      FAIL=$((FAIL + 1))
    elif [[ "$require_nonempty" == "1" ]] && [[ "$haystack" != *"signature"* ]] && [[ "$haystack" != *"invalid keys"* ]] && [[ "$haystack" != *"result=failed"* ]] && [[ "$haystack" != *"inference with id"* ]]; then
      echo "$id FAIL ($desc) baseline tx ran but no expected keeper response"
      echo "  got: $(echo "$haystack" | tr '\n' ' ' | head -c 240)"
      FAIL=$((FAIL + 1))
    else
      echo "$id PASS ($desc) baseline (keeper responded, not deprecated)"
      if [[ "$require_nonempty" == "1" ]]; then
        echo "  sample: $(echo "$haystack" | tr '\n' ' ' | head -c 160)"
      fi
      PASS=$((PASS + 1))
    fi
  fi
}

# Run inferenced tx ... --dry-run; keeper errors surface on stderr.
expect_chain_dry_run() {
  local id="$1" desc="$2"
  shift 2
  local out
  set +e
  out=$(with_keyring tx inference "$@" \
    --chain-id "$CHAIN_ID" --node "$CHAIN_RPC" --dry-run 2>&1)
  local rc=$?
  set -e
  expect_deprecated_text "$id" "$desc" "$out" 1
  if [[ "$rc" -ne 0 && "$EXPECT_410" == "0" ]]; then
    echo "  dry-run rc=$rc (expected on baseline)"
  fi
}

decode_tx_data() {
  local txhash="$1"
  python3 - "$txhash" "$CHAIN_RPC" "$INFERENCED" <<'PY'
import json, subprocess, sys, time
txhash, node, inferenced = sys.argv[1:4]
last_err = ""
for attempt in range(8):
    try:
        proc = subprocess.run(
            [inferenced, "query", "tx", txhash, "--node", node, "-o", "json"],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            check=False,
        )
        if proc.returncode != 0:
            last_err = (proc.stderr or proc.stdout or "").strip()
            time.sleep(3)
            continue
        doc = json.loads(proc.stdout)
        tr = doc.get("tx_response", doc)
        parts = []
        if tr.get("raw_log"):
            parts.append(tr["raw_log"])
        data = tr.get("data") or ""
        if data:
            try:
                parts.append(bytes.fromhex(data).decode("utf-8", errors="replace"))
            except ValueError:
                pass
        for event in tr.get("events", []):
            if event.get("type") in ("start_inference", "finish_inference"):
                for attr in event.get("attributes", []):
                    parts.append(f'{attr.get("key")}={attr.get("value")}')
        if parts:
            print("\n".join(parts))
            sys.exit(0)
        last_err = "empty tx response"
    except Exception as exc:
        last_err = str(exc)
    time.sleep(3)
sys.exit(1)
PY
}

broadcast_signed_tx() {
  local signed="$1"
  local out txhash decoded="" attempt
  set +e
  out=$(with_keyring tx broadcast "$signed" \
    --chain-id "$CHAIN_ID" --node "$CHAIN_RPC" \
    --broadcast-mode sync --yes --gas auto --gas-adjustment 1.5 -o json 2>&1)
  local rc=$?
  set -e

  txhash=$(echo "$out" | sed -n 's/^txhash: //p' | head -1)
  if [[ -z "$txhash" ]]; then
    txhash=$(echo "$out" | python3 -c 'import sys,json
try:
 print(json.load(sys.stdin).get("txhash",""))
except Exception:
 pass' 2>/dev/null || true)
  fi

  if [[ -n "$txhash" ]]; then
    for attempt in 1 2 3 4 5 6 7 8; do
      decoded=$(decode_tx_data "$txhash" 2>/dev/null || true)
      if [[ -n "$decoded" ]]; then
        break
      fi
      sleep 3
    done
  fi

  if [[ -n "$decoded" ]]; then
    printf '%s\n%s\n' "$out" "$decoded"
  else
    printf '%s\n' "$out"
  fi

  if [[ "$rc" -ne 0 && -z "$txhash" ]]; then
    return 1
  fi
  return 0
}

build_unsigned_from_start_template() {
  local addr="$1" inference_id="$2" ts="$3"
  with_keyring tx inference start-inference "$inference_id" "$DUMMY_SIG" payload "$addr" \
    --assigned-to "$addr" --model pr1386-test-model --original-prompt-hash "$DUMMY_SIG" \
    --request-timestamp "$ts" --transfer-signature "$DUMMY_SIG" \
    --from "$KEY_NAME" --chain-id "$CHAIN_ID" --generate-only -o json > "${TX_TMP}.unsigned.json"
}

swap_message_type() {
  local msg_type="$1"
  python3 - "$msg_type" "${TX_TMP}.unsigned.json" "${TX_TMP}.msg.json" <<'PY'
import json, sys
msg_type, src, dst = sys.argv[1:4]
with open(src) as f:
    tx = json.load(f)
creator = tx["body"]["messages"][0]["creator"]
msg = {"@type": msg_type, "creator": creator}
if msg_type.endswith("MsgValidation"):
    msg.update({
        "id": "1",
        "inference_id": "pr1386-classic-validation",
        "response_payload": "payload",
        "response_hash": "hash",
        "value": 0.5,
    })
elif msg_type.endswith("MsgFinishInference"):
    sig = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
    msg.update({
        "inference_id": sig,
        "response_hash": "hash",
        "response_payload": "payload",
        "prompt_token_count": 1,
        "completion_token_count": 1,
        "executed_by": creator,
        "requested_by": creator,
        "transferred_by": creator,
        "model": "pr1386-test-model",
        "prompt_hash": "hash",
        "original_prompt_hash": "hash",
        "request_timestamp": 1700000001,
        "transfer_signature": sig,
        "executor_signature": sig,
    })
tx["body"]["messages"] = [msg]
with open(dst, "w") as f:
    json.dump(tx, f)
PY
}

sign_unsigned() {
  with_keyring tx sign "${TX_TMP}.msg.json" \
    --from "$KEY_NAME" --chain-id "$CHAIN_ID" --node "$CHAIN_RPC" -o json > "${TX_TMP}.signed.json"
}

# MsgValidation is rejected in CheckTx by ValidationEarlyRejectDecorator before the
# deprecated keeper runs. Use tx simulate (DeliverTx+simulate=true) to hit the keeper.
simulate_unsigned_msg() {
  local unsigned="$1"
  set +e
  local out
  out=$(with_keyring tx simulate "$unsigned" \
    --from "$KEY_NAME" --chain-id "$CHAIN_ID" --node "$CHAIN_RPC" 2>&1)
  set -e
  printf '%s\n' "$out"
}

# expect_http TEST_ID description url [curl args...]
expect_http() {
  local id="$1" desc="$2" url="$3"
  shift 3
  local out code
  out=$(curl -sS -m 15 -w '\n__HTTP_CODE__%{http_code}' "$@" "$url" 2>&1) || {
    echo "$id FAIL ($desc): curl error"
    FAIL=$((FAIL + 1))
    return
  }
  code="${out##*__HTTP_CODE__}"
  body="${out%__HTTP_CODE__*}"

  local expect_codes
  case "$id" in
    D-01|D-03)
      if [[ "$EXPECT_410" == "1" ]]; then expect_codes="410"; else expect_codes="401|403|400|200"; fi
      ;;
    D-02)
      if [[ "$EXPECT_410" == "1" ]]; then expect_codes="410"; else expect_codes="404|400|401"; fi
      ;;
    D-04|D-05)
      if [[ "$EXPECT_410" == "1" ]]; then expect_codes="410"; else expect_codes="200|400|500"; fi
      ;;
    D-06|D-06b)
      if [[ "$EXPECT_410" == "1" ]]; then
        if [[ "$id" == "D-06" ]]; then expect_codes="410|405"; else expect_codes="410"; fi
      else
        expect_codes="200|400|401|405"
      fi
      ;;
    V-01) expect_codes="200" ;;
    V-02) expect_codes="200|404" ;; # 404 if v2 not in approved_versions
    B-01|B-02|R-01|R-04) expect_codes="200" ;;
    R-05) expect_codes="400|200" ;; # validation error, not 410
    P-01) expect_codes="200" ;;
    *) expect_codes=".*" ;;
  esac

  if [[ "$code" =~ ^($expect_codes)$ ]]; then
    echo "$id PASS ($desc) HTTP $code"
    PASS=$((PASS + 1))
  else
    echo "$id FAIL ($desc) HTTP $code (expected ~$expect_codes)"
    FAIL=$((FAIL + 1))
  fi
  echo "  body: $(echo "$body" | tr '\n' ' ' | head -c 200)"
}

section "CONFIG"
echo "API_BASE=$API_BASE"
echo "API_DIRECT=$API_DIRECT"
echo "CHAIN_ID=$CHAIN_ID"
echo "INFERENCED=$INFERENCED"
echo "INFERENCE_HOME=$INFERENCE_HOME"
echo "KEY_NAME=$KEY_NAME"
echo "EXPECT_410=$EXPECT_410 (1=post-PR, 0=0.2.13 baseline)"

section "0 PREFLIGHT"
curl -sS -m 10 -X POST "$CHAIN_RPC" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"status","params":[],"id":1}' \
  | python3 -c 'import sys,json; r=json.load(sys.stdin)["result"]; print("network:", r["node_info"]["network"]); print("height:", r["sync_info"]["latest_block_height"]); print("catching_up:", r["sync_info"]["catching_up"])'

expect_http "P-01" "health" "$API_BASE/health"

section "1 DEPRECATIONS"
expect_http "D-01" "POST /v1/chat/completions deprecated" "$API_BASE/v1/chat/completions" \
  -X POST -H 'Content-Type: application/json' \
  -d '{"model":"Qwen/Qwen3-4B-Instruct-2507","messages":[{"role":"user","content":"ping"}]}'

expect_http "D-02" "GET /v1/chat/completions deprecated" "$API_BASE/v1/chat/completions?id=test"

expect_http "D-03" "POST /v1/completions deprecated" "$API_BASE/v1/completions" \
  -X POST -H 'Content-Type: application/json' \
  -d '{"model":"x","prompt":"hi"}'

expect_http "D-04" "GET /v1/devshard/stats/shards deprecated" "$API_BASE/v1/devshard/stats/shards"

expect_http "D-05" "GET /v1/devshard/sessions/.../mempool deprecated" \
  "$API_BASE/v1/devshard/sessions/1/mempool"

expect_http "D-06" "POST /admin/v1/bls/request via proxy" "$API_BASE/admin/v1/bls/request" \
  -X POST -H 'Content-Type: application/json' -d '{}'

expect_http "D-06b" "POST /admin/v1/bls/request direct api" "$API_DIRECT/admin/v1/bls/request" \
  -X POST -H 'Content-Type: application/json' \
  -d '{"current_epoch_id":1,"chain_id":"11155111","request_id":"MQ==","data":"ZGVhZGJlZWY="}'

section "2 VERSIONED DEVSHARD"
expect_http "V-01" "/devshard/v1/healthz" "$API_BASE/devshard/v1/healthz"
expect_http "V-02" "/devshard/v2/healthz" "$API_BASE/devshard/v2/healthz"

section "3 CLASSIC INFERENCE CHAIN MSGS"
if [[ ! -x "$INFERENCED" ]]; then
  echo "SKIP chain msg tests: INFERENCED not found at $INFERENCED"
  SKIP=$((SKIP + 5))
else
  CHAIN_ADDR="$(key_addr)"
  CHAIN_TS="$(date +%s)"
  CHAIN_IID="$DUMMY_SIG"

  echo "--- C-01 MsgStartInference (broadcast + decode response data) ---"
  build_unsigned_from_start_template "$CHAIN_ADDR" "$CHAIN_IID" "$CHAIN_TS"
  cp "${TX_TMP}.unsigned.json" "${TX_TMP}.msg.json"
  sign_unsigned
  C01_OUT="$(broadcast_signed_tx "${TX_TMP}.signed.json" || true)"
  expect_deprecated_text "C-01" "MsgStartInference" "$C01_OUT" 1

  echo "--- C-02 MsgFinishInference (broadcast + decode response data) ---"
  build_unsigned_from_start_template "$CHAIN_ADDR" "$CHAIN_IID" "$CHAIN_TS"
  swap_message_type "/inference.inference.MsgFinishInference"
  sign_unsigned
  C02_OUT="$(broadcast_signed_tx "${TX_TMP}.signed.json" || true)"
  expect_deprecated_text "C-02" "MsgFinishInference" "$C02_OUT" 1

  echo "--- C-03 MsgValidation (tx simulate; CheckTx ante rejects broadcast) ---"
  build_unsigned_from_start_template "$CHAIN_ADDR" "$CHAIN_IID" "$CHAIN_TS"
  swap_message_type "/inference.inference.MsgValidation"
  C03_OUT="$(simulate_unsigned_msg "${TX_TMP}.msg.json")"
  expect_deprecated_text "C-03" "MsgValidation" "$C03_OUT" 1

  echo "--- C-04 MsgInvalidateInference (simulate via --dry-run) ---"
  expect_chain_dry_run "C-04" "MsgInvalidateInference" \
    invalidate-inference pr1386-classic-invalidate \
    --from "$CHAIN_ADDR"

  echo "--- C-05 MsgRevalidateInference (simulate via --dry-run) ---"
  expect_chain_dry_run "C-05" "MsgRevalidateInference" \
    revalidate-inference pr1386-classic-revalidate \
    --from "$CHAIN_ADDR"
fi

section "4 BRIDGE (must stay alive post-PR)"
expect_http "B-01" "bridge addresses" "$API_BASE/v1/bridge/addresses?chain=ethereum"
expect_http "B-02" "bridge status" "$API_BASE/v1/bridge/status"

section "5 REGRESSION"
expect_http "R-01" "dashboard" "$API_BASE/dashboard/gonka/validator"
expect_http "R-04" "chain-api node_info" "$API_BASE/chain-api/cosmos/base/tendermint/v1beta1/node_info"
expect_http "R-05" "poc proofs not deprecated" "$API_BASE/v1/poc/proofs" \
  -X POST -H 'Content-Type: application/json' -d '{}'

section "BINARIES"
docker ps --format '{{.Names}} {{.Image}}' 2>/dev/null | grep -E 'api|node|versiond|proxy|bridge' || true

section "SUMMARY"
echo "PASS=$PASS FAIL=$FAIL SKIP=$SKIP"
if [[ "$EXPECT_410" == "0" ]]; then
  echo "Baseline mode: deprecation tests PASS when legacy paths still respond (not 410)."
fi
[[ "$FAIL" -eq 0 ]]
