#!/usr/bin/env bash
#
# Materialize devshard/testenv/config.yaml + docker-compose.yml for citest
# (invoked by: make -C devshard/testenv gen-integration-config).
#
# ── What runs, in order ─────────────────────────────────────────────────────
#   1. Create a tiny YAML *seed* in $TMP (4 hosts, 4 escrow slots, K=8, 10 empty
#      height_sync.validators rows — gencompose will fill keys).
#   2. Run: go run ./cmd/gencompose -config "$TMP" -out "$OUT"
#      - Loads the seed, fills host/user/validator keys, validates, writes
#        docker-compose to "$OUT", then **saves the filled config back to $TMP**
#        (same path as -config). That is the file mock-chain / height-sync must
#        agree with for I9 (citest loads testenv/config.yaml for the verifier).
#   3. Copy filled $TMP → testenv/config.yaml and $OUT → docker-compose.yml.
#
# Bootstrap defaults when config is missing elsewhere are 10 hosts + 16 slots
# (see cmd/gencompose defaultConfig); this script always forces the citest
# skeleton in step 1.
#
# After this script, restart or recreate mock-chain + height-sync containers
# if they were already running with an older config (run-stack-citest.sh does
# that when it invokes this flow).
# ────────────────────────────────────────────────────────────────────────────
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

msg() { printf '[gen-integration-config] %s\n' "$*"; }

TMP="$(mktemp "${TMPDIR:-/tmp}/testenv-integration-seed.XXXXXX.yaml")"
cleanup() { rm -f "$TMP" "${TMP}.out.yml"; }
trap cleanup EXIT

OUT="${TMP}.out.yml"

msg "step 1/3: write citest seed YAML → $TMP (4 hosts, 4 slots, K=8, 10 validator placeholders)"
{
  cat <<'YAML'
# Seed for citest — gencompose fills keys, IPs, and rewrites this path.
hosts:
  - id: devshardd-testenv-0
  - id: devshardd-testenv-1
  - id: devshardd-testenv-2
  - id: devshardd-testenv-3
escrow:
  slots: 4
height_sync:
  anchor_period_nonces: 8
  validators:
YAML
  for _ in $(seq 1 10); do
    printf '    - private_key_hex: ""\n'
  done
} >"$TMP"

msg "step 2/3: go run ./cmd/gencompose -config \"$TMP\" -out \"$OUT\" (fills keys; saves config back to -config path; writes compose)"
go run ./cmd/gencompose -config "$TMP" -out "$OUT"

msg "step 3/3: install repo copies → $ROOT/config.yaml + $ROOT/docker-compose.yml"
cp "$TMP" "$ROOT/config.yaml"
cp "$OUT" "$ROOT/docker-compose.yml"

msg "done — citest profile: 4 hosts, 4 escrow slots, anchor_period_nonces=8, 10 height_sync validators (I9 verifier reads config.yaml here)"
