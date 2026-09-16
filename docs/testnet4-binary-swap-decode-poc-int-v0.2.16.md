# Testnet-4 binary swap: `decode-poc-int` into cosmovisor `v0.2.16`

Manual cosmovisor swap for **node** (`inferenced`) and **api** (`decentralized-api`) on **gonka-testnet-4** filfox fleet.

**Full staged rollout** (baseline PoC → mlnode → containers → this binary swap → gov → decode PoC):  
[testnet4-decode-poc-int-staged-rollout.md](./testnet4-decode-poc-int-staged-rollout.md)

**Goal:** run the `decode-poc-int` branch binaries (decode-based PoC / `decode_max_tokens`) while keeping the existing cosmovisor upgrade directory name **`v0.2.16`**.

**Pattern:** same safety / backup / stop→install→start flow as [testnet-binary-swap-v0.2.14-testnet-8.md](./testnet-binary-swap-v0.2.14-testnet-8.md), but **build on each host** from git instead of downloading a race-release zip.

**Install into cosmovisor dir `v0.2.16`** (overwrite bin under that name; keep `current → upgrades/v0.2.16`).

---

## Fleet (gonka-testnet-4)

| Role | SSH | Public inference URL (hint) | Account (typical) |
| --- | --- | --- | --- |
| Genesis | `ssh -p 18221 decentai@xj7-5.s.filfox.io` | `http://xj7-5.s.filfox.io:19242` | `gonka1gfvhpp…` |
| Validator | `ssh -p 18223 decentai@xj7-5.s.filfox.io` | `http://xj7-5.s.filfox.io:19246` | — |
| Validator | `ssh -p 18226 decentai@xj7-5.s.filfox.io` | `http://xj7-5.s.filfox.io:19252` | — |
| Validator | `ssh -p 18214 decentai@xj7-5.s.filfox.io` | `http://xj7-5.s.filfox.io:19228` | `gonka1h3z3m8…` |

**All four are bonded validators.** Stop / install / start them in the **same short window**.

Join dir on every host: `/srv/dai/gonka/deploy/join`  
Repo checkout on every host: `/srv/dai/gonka` (git)

---

## Critical compatibility notes (read before swap)

1. **Container image ≠ running binary.** Cosmovisor runs  
   `/root/.inference/cosmovisor/current/bin/inferenced`  
   (today `upgrades/v0.2.16`). Retagging `ghcr.io/gonka-ai/inferenced:decode-poc-int` alone does nothing if `current` still points at the old zip contents.
2. **Proto field clash:** live **v0.2.16-testnet-5** stores `PoCModelConfig` field **6** as `dynamic_coefficient` (bytes).  
   **`decode-poc-int`** uses field **6** as `decode_max_tokens` (varint).  
   After this swap, `inferenced q inference params` may error with `wrong wireType … DecodeMaxTokens` until you land a full `MsgUpdateParams` that rewrites `poc_params.models[*]` with `decode_max_tokens` (and without `dynamic_coefficient`). Have that proposal ready **before** the swap window.
3. **`decode-poc-int` upgrade handlers** on that branch historically stop at **v0.2.15**. This runbook is a **manual binary overwrite** of the `v0.2.16` cosmovisor slot (no gov software-upgrade height). Confirm the built binary starts and syncs before declaring success.
4. Rebuild **both** `inferenced` and `decentralized-api` from the **same** `decode-poc-int` commit so fee / params clients match the chain.
5. **This swap does not delete chain state.** Only cosmovisor **binaries** under `.inference/cosmovisor` / `.dapi/cosmovisor` are replaced. Block store / app state stay in `.inference/data` (etc.). The earlier incident was “node started with empty `bin`,” not wiped chain data — still enough to miss blocks / get jailed if left broken.

---

## When to run

On **any** host (proxy or local API):

```bash
# Prefer local if proxy:8000 is up; else use public genesis URL
curl -sS http://127.0.0.1:8000/v1/epochs/latest | jq '{
  phase,
  height: .block_height,
  next_poc: .epoch_stages.next_poc_start,
  blocks_left: (.epoch_stages.next_poc_start - (.block_height|tonumber))
}'
```

Proceed only if:

- `phase` is **`Inference`**
- `blocks_left` is comfortably **> 100**
- You can run the stop→install→start steps on **18221, 18223, 18226, 18214** in one coordinated window

Do **not** start a swap during PoC generate / validate / confirmation PoC windows.

---

## 0. One-time setup (per host)

SSH into each of the four ports and run:

```bash
set -euo pipefail

export JOIN_DIR='/srv/dai/gonka/deploy/join'
export REPO_DIR='/srv/dai/gonka'
export BRANCH='decode-poc-int'
# Prefer durable path — /tmp can be cleaned between §2 and §3
export STAGING="/srv/dai/backups/staging-decode-poc-int-v0.2.16"

# Unique per host (+ SSH source port when available)
if [ -n "${SSH_CLIENT:-}" ]; then
  export BACKUP_ROOT="/srv/dai/backups/binary-swap-decode-poc-int-v0.2.16-$(date +%Y%m%d-%H%M%S)-$(hostname -s)-p${SSH_CLIENT##* }"
else
  export BACKUP_ROOT="/srv/dai/backups/binary-swap-decode-poc-int-v0.2.16-$(date +%Y%m%d-%H%M%S)-$(hostname -s)"
fi

test -d "$JOIN_DIR" || { echo "FATAL: JOIN_DIR missing: $JOIN_DIR"; exit 1; }
test -d "$REPO_DIR/.git" || { echo "FATAL: REPO_DIR is not a git checkout: $REPO_DIR"; exit 1; }

sudo mkdir -p /srv/dai/backups
sudo chown "$(whoami):$(id -gn)" /srv/dai/backups

# Persist paths for later shells / §3 / §6
echo "$BACKUP_ROOT" > /srv/dai/backups/LAST_BACKUP_ROOT_decode-poc-int.txt
echo "$STAGING" > /srv/dai/backups/LAST_STAGING_decode-poc-int.txt

cd "$JOIN_DIR"
mkdir -p "$STAGING" "$BACKUP_ROOT/inference-cosmovisor" "$BACKUP_ROOT/dapi-cosmovisor"

# Need Docker BuildKit / buildx for make build-for-upgrade; zip for packaging
docker buildx version
if ! command -v zip >/dev/null || ! command -v unzip >/dev/null; then
  sudo apt-get update -qq
  sudo apt-get install -y -qq zip unzip
fi
df -h /srv/dai /tmp | sed -n '1,5p'

echo "§0 OK"
echo "  JOIN_DIR=$JOIN_DIR"
echo "  REPO_DIR=$REPO_DIR"
echo "  BRANCH=$BRANCH"
echo "  STAGING=$STAGING"
echo "  BACKUP_ROOT=$BACKUP_ROOT"
```

Each host has its **own** `BACKUP_ROOT` — do not reuse another machine’s path. Re-export these vars (or reload from `LAST_*` files) in every new shell before §1–§3.

---

## 1. Backup (preserve current cosmovisor)

```bash
cd "$JOIN_DIR"

{
  echo "=== host: $(hostname) ==="
  echo "=== time: $(date -Is) ==="
  echo "=== ssh port hint: check your session ==="
  docker exec node /root/.inference/cosmovisor/current/bin/inferenced version 2>&1 || true
  docker exec api  /root/.dapi/cosmovisor/current/bin/decentralized-api version 2>&1 || true
  docker exec node readlink /root/.inference/cosmovisor/current 2>&1 || true
  docker exec api  readlink /root/.dapi/cosmovisor/current 2>&1 || true
  curl -sS http://127.0.0.1:8000/v1/versions 2>&1 || true
  docker exec node wget -qO- http://127.0.0.1:26657/abci_info 2>&1 || true
} | tee "$BACKUP_ROOT/pre-swap-state.txt"

CUR_INF=$(sudo readlink .inference/cosmovisor/current)
CUR_DAPI=$(sudo readlink .dapi/cosmovisor/current)
echo "$CUR_INF"  | tee "$BACKUP_ROOT/inference-cosmovisor/active-upgrade-name.txt"
echo "$CUR_DAPI" | tee "$BACKUP_ROOT/dapi-cosmovisor/active-upgrade-name.txt"

# Expect upgrades/v0.2.16 — abort if unexpected
echo "CUR_INF=$CUR_INF CUR_DAPI=$CUR_DAPI"
[[ "$CUR_INF"  == *v0.2.16* ]] || { echo "UNEXPECTED inference cosmovisor current: $CUR_INF"; exit 1; }
[[ "$CUR_DAPI" == *v0.2.16* ]] || { echo "UNEXPECTED dapi cosmovisor current: $CUR_DAPI"; exit 1; }

sudo cp -a ".inference/cosmovisor/${CUR_INF}"  "$BACKUP_ROOT/inference-cosmovisor/active-upgrade/"
sudo cp -a ".dapi/cosmovisor/${CUR_DAPI}"     "$BACKUP_ROOT/dapi-cosmovisor/active-upgrade/"
sudo chown -R "$(whoami):$(id -gn)" "$BACKUP_ROOT"

ls -la "$BACKUP_ROOT/inference-cosmovisor/active-upgrade/bin/"
ls -la "$BACKUP_ROOT/dapi-cosmovisor/active-upgrade/bin/"

# Hard gate: rollback is useless without these
test -x "$BACKUP_ROOT/inference-cosmovisor/active-upgrade/bin/inferenced" \
  || { echo "FATAL: backup missing inferenced"; exit 1; }
test -x "$BACKUP_ROOT/dapi-cosmovisor/active-upgrade/bin/decentralized-api" \
  || { echo "FATAL: backup missing decentralized-api"; exit 1; }

echo "Backup OK: $BACKUP_ROOT"
echo "$BACKUP_ROOT" > /srv/dai/backups/LAST_BACKUP_ROOT_decode-poc-int.txt
```

Optional extra preserve (params snapshot for the post-swap gov patch):

```bash
docker exec node wget -qO- \
  'http://127.0.0.1:1317/productscience/inference/inference/params' \
  > "$BACKUP_ROOT/params-pre-swap.json" || true
```

---

## 2. Pull `decode-poc-int` and build linux/amd64 (per host)

Builds use Docker (`make build-for-upgrade`). Do this **before** the coordinated stop window so compile time is not on the critical path. Binaries stay in `$STAGING` until step 3.

**Do not start §3 until** both `$STAGING/inferenced-amd64.zip` and `$STAGING/decentralized-api-amd64.zip` exist on **this** host (and `git-sha.txt` matches the other hosts). §3 will refuse to stop/wipe without them.

```bash
set -euo pipefail

# If this is a fresh shell, reload paths from §0:
export JOIN_DIR="${JOIN_DIR:-/srv/dai/gonka/deploy/join}"
export REPO_DIR="${REPO_DIR:-/srv/dai/gonka}"
export BRANCH="${BRANCH:-decode-poc-int}"
export STAGING="${STAGING:-$(cat /srv/dai/backups/LAST_STAGING_decode-poc-int.txt 2>/dev/null || echo /srv/dai/backups/staging-decode-poc-int-v0.2.16)}"
mkdir -p "$STAGING"

sudo apt-get update -qq
sudo apt-get install -y -qq zip unzip

cd "$REPO_DIR"
git fetch origin "$BRANCH"

# Preserve live host deploy/join files (avoid stash merge conflicts)
JOIN_SAVE="/tmp/host-join-save-$$"
mkdir -p "$JOIN_SAVE"
cp -a deploy/join/docker-compose.yml \
      deploy/join/docker-compose.mlnode.yml \
      "$JOIN_SAVE/" 2>/dev/null || true
# optional extras if present
for f in deploy/join/docker-compose.env-override.yml \
         deploy/join/docker-compose.genesis-override.yml \
         deploy/join/docker-compose.runtime-override.yml \
         deploy/join/config.env; do
  [ -f "$f" ] && cp -a "$f" "$JOIN_SAVE/"
done

git checkout -f -B "$BRANCH" "origin/$BRANCH"

# Restore host deploy files over branch versions
cp -a "$JOIN_SAVE"/. deploy/join/
rm -rf "$JOIN_SAVE"

git rev-parse --abbrev-ref HEAD | grep -qx "$BRANCH"
EXPECTED_SHA="$(git rev-parse origin/"$BRANCH")"
test "$(git rev-parse HEAD)" = "$EXPECTED_SHA" \
  || { echo "FATAL: HEAD != origin/$BRANCH"; exit 1; }
git rev-parse HEAD | tee "$STAGING/git-sha.txt"
git log -1 --oneline | tee -a "$STAGING/git-sha.txt"

# --- inferenced (chain) ---
cd "$REPO_DIR/inference-chain"
rm -rf output ../public-html/v2/inferenced/inferenced-amd64.zip
mkdir -p ../public-html/v2
make build-for-upgrade PLATFORM=linux/amd64 GOOS=linux GOARCH=amd64

test -f ../public-html/v2/inferenced/inferenced-amd64.zip
cp -a ../public-html/v2/inferenced/inferenced-amd64.zip "$STAGING/"
shasum -a 256 "$STAGING/inferenced-amd64.zip" | tee "$STAGING/inferenced-amd64.zip.sha256"

rm -rf "$STAGING/inf-unpack" && mkdir -p "$STAGING/inf-unpack"
unzip -o "$STAGING/inferenced-amd64.zip" -d "$STAGING/inf-unpack"
test -x "$STAGING/inf-unpack/inferenced"

# Sanity: raw byte search (strings|grep is unreliable on these musl bins)
python3 - "$STAGING/inf-unpack/inferenced" <<'PY'
import sys
p = sys.argv[1]
data = open(p, "rb").read()
ok = b"DecodeMaxTokens" in data or b"decode_max_tokens" in data
print("inferenced markers:",
      "DecodeMaxTokens" if b"DecodeMaxTokens" in data else "-",
      "decode_max_tokens" if b"decode_max_tokens" in data else "-")
sys.exit(0 if ok else 1)
PY

# --- decentralized-api ---
cd "$REPO_DIR/decentralized-api"
rm -rf output ../public-html/v2/dapi/decentralized-api-amd64.zip
mkdir -p ../public-html/v2
make build-for-upgrade PLATFORM=linux/amd64 GOOS=linux GOARCH=amd64

test -f ../public-html/v2/dapi/decentralized-api-amd64.zip
cp -a ../public-html/v2/dapi/decentralized-api-amd64.zip "$STAGING/"
shasum -a 256 "$STAGING/decentralized-api-amd64.zip" | tee "$STAGING/decentralized-api-amd64.zip.sha256"

rm -rf "$STAGING/api-unpack" && mkdir -p "$STAGING/api-unpack"
unzip -o "$STAGING/decentralized-api-amd64.zip" -d "$STAGING/api-unpack"
test -x "$STAGING/api-unpack/decentralized-api"

ls -lh "$STAGING"/*.zip "$STAGING"/*.sha256 "$STAGING"/git-sha.txt
echo "Build OK host=$(hostname -s) sha=$(head -1 "$STAGING/git-sha.txt")"
```

**Note:** Your previous run already built the correct commit (`7e2e35ea1`) and produced `inferenced-amd64.zip`. The failure was only the bad `strings|grep` check. You can either re-run the full block above, or only finish the API build + re-check:

```bash
# Quick resume if inferenced zip already good:
export REPO_DIR=/srv/dai/gonka STAGING=/srv/dai/backups/staging-decode-poc-int-v0.2.16
python3 -c 'd=open("'"$STAGING"'/inf-unpack/inferenced","rb").read(); import sys; sys.exit(0 if (b"DecodeMaxTokens" in d or b"decode_max_tokens" in d) else 1)' \
  && echo "inferenced OK" || echo "re-run full §2"

cd "$REPO_DIR/decentralized-api"
rm -rf output ../public-html/v2/dapi/decentralized-api-amd64.zip
mkdir -p ../public-html/v2
make build-for-upgrade PLATFORM=linux/amd64 GOOS=linux GOARCH=amd64
cp -a ../public-html/v2/dapi/decentralized-api-amd64.zip "$STAGING/"
shasum -a 256 "$STAGING/decentralized-api-amd64.zip" | tee "$STAGING/decentralized-api-amd64.zip.sha256"
rm -rf "$STAGING/api-unpack" && mkdir -p "$STAGING/api-unpack"
unzip -o "$STAGING/decentralized-api-amd64.zip" -d "$STAGING/api-unpack"
test -x "$STAGING/api-unpack/decentralized-api"
ls -lh "$STAGING"/*.zip
```

**Record the git SHA** from `$STAGING/git-sha.txt`. All four hosts should build the **same** commit (compare SHAs before the stop window).

---

## 3. Immediate swap (same on every host — coordinated)

Run on **18221, 18223, 18226, 18214** as close together as possible.

**Prerequisites (hard):** §2 must already have finished on **this** host. Do **not** stop or clear `bin` until the preflight below passes. If preflight fails, leave the live node running and finish the build first.

**Always `source config.env` before `docker compose`.**

```bash
cd "$JOIN_DIR"

set -euo pipefail

# --- restore env if this is a new shell ---
: "${JOIN_DIR:?JOIN_DIR is unset — export it from §0}"
if [ -z "${STAGING:-}" ] && [ -f /srv/dai/backups/LAST_STAGING_decode-poc-int.txt ]; then
  STAGING="$(cat /srv/dai/backups/LAST_STAGING_decode-poc-int.txt)"
fi
if [ -z "${BACKUP_ROOT:-}" ] && [ -f /srv/dai/backups/LAST_BACKUP_ROOT_decode-poc-int.txt ]; then
  BACKUP_ROOT="$(cat /srv/dai/backups/LAST_BACKUP_ROOT_decode-poc-int.txt)"
fi
: "${STAGING:?STAGING is unset — export it from §0 or re-run §0}"
: "${BACKUP_ROOT:?BACKUP_ROOT is unset — export it from §0/§1 or re-run §1}"

# --- timing gate: refuse mid-PoC / tight windows ---
EPOCH_JSON="$(curl -fsS --max-time 8 http://127.0.0.1:8000/v1/epochs/latest \
  || curl -fsS --max-time 8 http://xj7-5.s.filfox.io:19242/v1/epochs/latest)"
printf '%s' "$EPOCH_JSON" | python3 -c '
import json,sys
d=json.load(sys.stdin)
phase=d.get("phase")
h=int(d.get("block_height"))
next_poc=int((d.get("epoch_stages") or {}).get("next_poc_start") or 0)
left=next_poc-h if next_poc else -1
print(f"phase={phase} height={h} next_poc={next_poc} blocks_left={left}")
if phase != "Inference":
    raise SystemExit(f"FATAL: phase={phase!r} — wait for Inference")
if left <= 100:
    raise SystemExit(f"FATAL: blocks_left={left} — need >100 before stop/wipe")
'

# --- preflight: abort BEFORE stop/wipe if staging OR rollback artifacts missing ---
test -f "$STAGING/inferenced-amd64.zip" \
  || { echo "FATAL: missing $STAGING/inferenced-amd64.zip — finish §2 build first"; exit 1; }
test -f "$STAGING/decentralized-api-amd64.zip" \
  || { echo "FATAL: missing $STAGING/decentralized-api-amd64.zip — finish §2 build first"; exit 1; }
test -s "$STAGING/inferenced-amd64.zip"
test -s "$STAGING/decentralized-api-amd64.zip"

# Confirm zip contents before touching live bins (allow optional ./ prefix)
unzip -Z1 "$STAGING/inferenced-amd64.zip" | grep -Eq '^(./)?inferenced$' \
  || { echo "FATAL: inferenced-amd64.zip does not contain inferenced"; exit 1; }
unzip -Z1 "$STAGING/decentralized-api-amd64.zip" | grep -Eq '^(./)?decentralized-api$' \
  || { echo "FATAL: decentralized-api-amd64.zip does not contain decentralized-api"; exit 1; }

# Rollback must be usable BEFORE we wipe live bins
test -x "$BACKUP_ROOT/inference-cosmovisor/active-upgrade/bin/inferenced" \
  || { echo "FATAL: backup inferenced missing under $BACKUP_ROOT — re-run §1"; exit 1; }
test -x "$BACKUP_ROOT/dapi-cosmovisor/active-upgrade/bin/decentralized-api" \
  || { echo "FATAL: backup decentralized-api missing under $BACKUP_ROOT — re-run §1"; exit 1; }

echo "Preflight OK — staging zips + backup present; timing OK. Proceeding with stop/wipe/install."
cat "$STAGING/git-sha.txt" 2>/dev/null || true
echo "BACKUP_ROOT=$BACKUP_ROOT"

set -a && source config.env && set +a

files=(-f docker-compose.yml)
[ -f docker-compose.mlnode.yml ] && files+=(-f docker-compose.mlnode.yml)
[ -f docker-compose.env-override.yml ] && files+=(-f docker-compose.env-override.yml)
[ -f docker-compose.genesis-override.yml ] && files+=(-f docker-compose.genesis-override.yml)
[ -f docker-compose.runtime-override.yml ] && files+=(-f docker-compose.runtime-override.yml)
dc() { docker compose "${files[@]}" "$@"; }

# --- stop (only after preflight) ---
dc stop node api
sleep 3

# --- install into cosmovisor v0.2.16 (preserve directory name) ---
sudo mkdir -p .inference/cosmovisor/upgrades/v0.2.16/bin \
              .dapi/cosmovisor/upgrades/v0.2.16/bin

# Clear previous bin contents carefully (keep upgrades/v0.2.16 dir)
sudo find .inference/cosmovisor/upgrades/v0.2.16/bin -mindepth 1 -delete
sudo find .dapi/cosmovisor/upgrades/v0.2.16/bin -mindepth 1 -delete

sudo unzip -o "$STAGING/inferenced-amd64.zip"        -d .inference/cosmovisor/upgrades/v0.2.16/bin
sudo unzip -o "$STAGING/decentralized-api-amd64.zip" -d .dapi/cosmovisor/upgrades/v0.2.16/bin

sudo chmod +x .inference/cosmovisor/upgrades/v0.2.16/bin/inferenced
sudo chmod +x .dapi/cosmovisor/upgrades/v0.2.16/bin/decentralized-api

# --- post-install gate: do NOT start if binaries are missing ---
test -x .inference/cosmovisor/upgrades/v0.2.16/bin/inferenced \
  || { echo "FATAL: inferenced missing after unzip — run §6 rollback NOW, do not start"; exit 1; }
test -x .dapi/cosmovisor/upgrades/v0.2.16/bin/decentralized-api \
  || { echo "FATAL: decentralized-api missing after unzip — run §6 rollback NOW, do not start"; exit 1; }

sudo ln -sfn upgrades/v0.2.16 .inference/cosmovisor/current
sudo ln -sfn upgrades/v0.2.16 .dapi/cosmovisor/current

# NATS state often wedges api across recreates
sudo rm -rf .dapi/.nats

# --- start node first, then api ---
dc up -d --no-deps --force-recreate node && sleep 20
dc up -d --no-deps --force-recreate api  && sleep 20
```

If the post-install gate fails with node/api **already stopped**, run **§6 immediately** (do not leave validators down).

Optional: also refresh container **images** to `decode-poc-int` (shell only; cosmovisor still owns the app binary). Only if your compose pins those tags and you intentionally want image/code parity:

```bash
# Optional — not a substitute for the cosmovisor install above
# Edit compose / pull as you already do for decode-poc-int images, then:
# dc up -d --no-deps --force-recreate node api
```

---

## 4. Verify (per host)

```bash
docker exec node readlink /root/.inference/cosmovisor/current
# → upgrades/v0.2.16

docker exec node /root/.inference/cosmovisor/current/bin/inferenced version
docker exec api  /root/.dapi/cosmovisor/current/bin/decentralized-api version

docker exec node python3 - <<'PY'
import pathlib
p = pathlib.Path("/root/.inference/cosmovisor/current/bin/inferenced")
d = p.read_bytes()
print("DecodeMaxTokens", b"DecodeMaxTokens" in d)
print("decode_max_tokens", b"decode_max_tokens" in d)
assert b"DecodeMaxTokens" in d or b"decode_max_tokens" in d
PY

docker exec node wget -qO- http://127.0.0.1:26657/status | python3 -c '
import json,sys
d=json.load(sys.stdin)["result"]
print("catching_up", d["sync_info"]["catching_up"],
      "height", d["sync_info"]["latest_block_height"],
      "vp", d["validator_info"]["voting_power"])
'

curl -sS http://127.0.0.1:8000/v1/versions | jq '{
  node: .node_version,
  api: .api_version
}' 2>/dev/null || true

docker ps --filter name=api --filter name=node --format '{{.Names}} {{.Status}}'
```

**Pass criteria:**

- `current` still **`upgrades/v0.2.16`**
- `inferenced` / `decentralized-api` versions match the **decode-poc-int** build (commit ≈ `$STAGING/git-sha.txt`)
- `decode_max_tokens` string present in the **running** inferenced binary
- `catching_up=false`, voting power unchanged for each validator
- `api` **Up** (not Restarting)

Fleet height alignment (from your laptop):

```bash
for p in 18221 18223 18226 18214; do
  echo "==== $p ===="
  ssh -o BatchMode=yes -o ConnectTimeout=12 -p "$p" decentai@xj7-5.s.filfox.io \
    'docker exec node wget -qO- http://127.0.0.1:26657/status' \
    | python3 -c 'import json,sys;d=json.load(sys.stdin)["result"];print(d["sync_info"]["latest_block_height"], d["validator_info"]["voting_power"], d["sync_info"]["catching_up"])'
done
```

### 4b. After swap — enable decode PoC params

When nodes are healthy, submit gov `MsgUpdateParams` (genesis key on **18221**, `/srv/dai/inferenced` + `home=/srv/dai/.inference` or the matching **new** binary via docker) setting:

```json
"poc_params": {
  "models": [
    { "model_id": "Qwen/Qwen3-4B-Instruct-2507", "decode_max_tokens": "256", "...": "..." },
    { "model_id": "Qwen/Qwen2.5-7B-Instruct", "decode_max_tokens": "256", "...": "..." },
    { "model_id": "Qwen/QwQ-32B", "decode_max_tokens": "256", "...": "..." }
  ]
}
```

Use a **full** params dump from a client that matches the **new** binary. Strip `dynamic_coefficient` / unknown fields the new codec rejects. Vote from enough validators for quorum (~33.4%).

Confirm via REST:

```bash
docker exec node wget -qO- \
  'http://127.0.0.1:1317/productscience/inference/inference/params' \
  | jq '.params.poc_params.models[] | {model_id, decode_max_tokens, seq_len}'
```

---

## 5. API NATS crash-loop fix

If `api` restarts with `NATS server not ready after 3 attempts`:

```bash
cd "$JOIN_DIR"
set -a && source config.env && set +a
# ... dc() helper from step 3 ...

dc stop api && sleep 5
sudo rm -rf .dapi/.nats
dc up -d --no-deps --force-recreate api
```

Usually caused by restarting `api` once without `config.env` loaded.

---

## 6. Rollback (restore preserved cosmovisor)

Uses the per-host `$BACKUP_ROOT` from step 1. If unset:  
`export BACKUP_ROOT="$(cat /srv/dai/backups/LAST_BACKUP_ROOT_decode-poc-int.txt)"`

```bash
cd "$JOIN_DIR"
set -euo pipefail
: "${BACKUP_ROOT:?BACKUP_ROOT unset}"
: "${JOIN_DIR:?JOIN_DIR unset}"

test -x "$BACKUP_ROOT/inference-cosmovisor/active-upgrade/bin/inferenced"
test -x "$BACKUP_ROOT/dapi-cosmovisor/active-upgrade/bin/decentralized-api"

set -a && source config.env && set +a
files=(-f docker-compose.yml)
[ -f docker-compose.mlnode.yml ] && files+=(-f docker-compose.mlnode.yml)
[ -f docker-compose.env-override.yml ] && files+=(-f docker-compose.env-override.yml)
[ -f docker-compose.genesis-override.yml ] && files+=(-f docker-compose.genesis-override.yml)
[ -f docker-compose.runtime-override.yml ] && files+=(-f docker-compose.runtime-override.yml)
dc() { docker compose "${files[@]}" "$@"; }

INF_NAME=$(cat "$BACKUP_ROOT/inference-cosmovisor/active-upgrade-name.txt")
DAPI_NAME=$(cat "$BACKUP_ROOT/dapi-cosmovisor/active-upgrade-name.txt")

dc stop node api || true

sudo rm -rf ".inference/cosmovisor/${INF_NAME}"
sudo cp -a "$BACKUP_ROOT/inference-cosmovisor/active-upgrade" ".inference/cosmovisor/${INF_NAME}"
sudo ln -sfn "$INF_NAME" .inference/cosmovisor/current

sudo rm -rf ".dapi/cosmovisor/${DAPI_NAME}"
sudo cp -a "$BACKUP_ROOT/dapi-cosmovisor/active-upgrade" ".dapi/cosmovisor/${DAPI_NAME}"
sudo ln -sfn "$DAPI_NAME" .dapi/cosmovisor/current

test -x ".inference/cosmovisor/${INF_NAME}/bin/inferenced"
test -x ".dapi/cosmovisor/${DAPI_NAME}/bin/decentralized-api"

sudo rm -rf .dapi/.nats

dc up -d --no-deps --force-recreate node && sleep 20
dc up -d --no-deps --force-recreate api
```

Re-check versions against `$BACKUP_ROOT/pre-swap-state.txt`.

---

## Notes

- Recreating Docker **images** alone does not change the running binary if cosmovisor `current` points at an upgrade dir.
- Host-side `/srv/dai/inferenced` may be an older CLI (e.g. 0.2.15) or a musl binary that fails with `No such file or directory` on the host glibc; prefer `docker exec …/cosmovisor/current/bin/…`.
- Do **not** run `docker compose` without sourcing `deploy/join/config.env`.
- Optional helper if present: `source ./scripts/prepare-compose-restart.sh prepare` (defines `dc()` with overrides).
- TMKMS / mlnode are **not** part of this swap; leave them running unless you have a separate reason to touch them.
- After a successful decode enable, watch one full epoch PoC + one confirmation PoC before further image churn.

---

## Quick checklist

- [ ] Inference phase, `blocks_left > 100` (**§3 refuses otherwise**)
- [ ] Backup on **18221, 18223, 18226, 18214** (own `BACKUP_ROOT` each; `test -x` both bins)
- [ ] `LAST_BACKUP_ROOT_decode-poc-int.txt` written; can restore without remembering the path
- [ ] Same `decode-poc-int` git SHA on all four builds
- [ ] Zips under durable `$STAGING` (not only `/tmp`); contain `decode_max_tokens`
- [ ] §3 preflight passes (staging **and** backup) **before** any stop/wipe
- [ ] Coordinated stop → install into **`upgrades/v0.2.16`** → start only after post-unzip `test -x`
- [ ] All four synced, VP intact, api Up
- [ ] Gov params: `decode_max_tokens=256` on all PoC models
- [ ] Know §6 rollback; do not leave node stopped if install gate fails
