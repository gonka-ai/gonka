# Testnet-4 staged rollout: `decode-poc-int` (safe path)

End-to-end runbook for introducing **decode PoC** (`decode_max_tokens`) on **gonka-testnet-4** without collapsing the epoch group.

Use this after a **clean 2.15 baseline** (fresh chain or reset), or after recovering from an empty/zero-weight epoch. Companion binary-swap detail:

- [testnet4-binary-swap-decode-poc-int-v0.2.16.md](./testnet4-binary-swap-decode-poc-int-v0.2.16.md)

---

## Fleet

| SSH | Public inference (hint) |
| --- | --- |
| `ssh -p 18221 decentai@xj7-5.s.filfox.io` | `:19242` |
| `ssh -p 18223 decentai@xj7-5.s.filfox.io` | `:19246` |
| `ssh -p 18226 decentai@xj7-5.s.filfox.io` | `:19252` |
| `ssh -p 18214 decentai@xj7-5.s.filfox.io` | `:19228` |

Join dir on every host: `/srv/dai/gonka/deploy/join`  
Repo: `/srv/dai/gonka`

**All four are bonded validators.** Coordinate stop/recreate/swap in the **same short window**.

### Images (current)

| Component | Tag | Notes |
| --- | --- | --- |
| mlnode | `ghcr.io/gonka-ai/mlnode:decode-poc-int` | same digest as `decode-poc-int-f08ea3e` |
| api | `ghcr.io/gonka-ai/api:decode-poc-int` | |
| inferenced (container) | `ghcr.io/gonka-ai/inferenced:decode-poc-int` | shell only until cosmovisor swap |

---

## Hard rules (do not skip)

1. **Order matters.** Never enable `decode_max_tokens > 0` until **all** of these are on `decode-poc-int` on **all four** hosts:
   - mlnode image
   - api + node **container** images
   - cosmovisor **binaries** (`inferenced` + `decentralized-api`)
2. **Container ≠ binary.** Retagging `ghcr.io/.../inferenced:decode-poc-int` does nothing if `cosmovisor/current` still runs an old zip.
3. **Timing.** Change images/bins only in **`Inference`**. Never during PoC generate / validate / confirmation / SetNewValidators.
4. **Stale artifacts.** Local `.dapi/data/poc-artifacts/` survives chain rollback and height reuse. After any rollback, api recreate, or height that will be reused, wipe artifacts in Inference:

   ```bash
   cd /srv/dai/gonka/deploy/join
   sudo rm -rf .dapi/data/poc-artifacts/*
   ```

5. **TMKMS after height rollback.** If you hard-rollback block height, reset `.tmkms/state/priv_validator_state.json` on all hosts (height regression otherwise blocks signing). Format example:

   ```json
   {"height":"<TARGET>","round":"0","step":0,"block_id":{"hash":"","part_set_header":{"total":0,"hash":""}}}
   ```

6. **Empty epoch is sticky.** If PoC ends with `Invalid majority` / only weight-0 members, the next round can hit `No voting powers for model` forever. Prefer another coordinated recovery (or chain reset) over “wait for next PoC”.
7. **Compose.** Always `source config.env` and use the full `-f` set (`docker-compose.yml` + `mlnode` + env/genesis/runtime overrides + decode override if present). Never recreate `proxy` with only `docker-compose.yml`.

### Compose helper (every host)

```bash
cd /srv/dai/gonka/deploy/join
source config.env
files=(-f docker-compose.yml -f docker-compose.mlnode.yml)
[ -f docker-compose.env-override.yml ] && files+=(-f docker-compose.env-override.yml)
[ -f docker-compose.genesis-override.yml ] && files+=(-f docker-compose.genesis-override.yml)
[ -f docker-compose.runtime-override.yml ] && files+=(-f docker-compose.runtime-override.yml)
[ -f docker-compose.decode-poc-int.override.yml ] && files+=(-f docker-compose.decode-poc-int.override.yml)
dc() { docker compose "${files[@]}" "$@"; }
```

### Epoch timing helper

```bash
curl -sS http://127.0.0.1:8000/v1/epochs/latest | python3 -c '
import json,sys
d=json.load(sys.stdin)
h=int(d["block_height"]); nxt=int((d.get("epoch_stages") or {}).get("next_poc_start") or 0)
print(f"phase={d.get(\"phase\")} height={h} next_poc={nxt} blocks_left={nxt-h if nxt else -1}")
'
```

| Action | Minimum `blocks_left` (approx) |
| --- | --- |
| Image pull only | > 60 |
| mlnode or api/node recreate | > 80 |
| Cosmovisor binary swap (all 4) | **> 100** |
| Submit `decode_max_tokens` gov | enough that **voting ends before next PocStart** (prefer **> 15 min** wall clock) |

---

## Success criteria (any PoC under test)

| Check | Pass |
| --- | --- |
| Commits | **4/4** hosts store-commit |
| Off-chain validation | `votedInvalid=0` on each host |
| On-chain votes | positive `validated_weight` (not `-1`) for all miners |
| Epoch formation | new epoch **4 members**, `total_weight > 0` |
| Decode mode (only after gov) | mlnode `scheme=decode decode=True max_tokens=256`; leaves **257** bytes (not 24) |
| Fail signals | `got 24 bytes, expected 257`; `Invalid majority`; `No voting powers for model`; empty epoch; `aborting epoch formation` |

### Quick status from laptop

```bash
for p in 18221 18223 18226 18214; do
  echo "==== $p ===="
  ssh -o BatchMode=yes -o ConnectTimeout=12 -p "$p" decentai@xj7-5.s.filfox.io \
    'docker exec api curl -s http://node:26657/status' \
    | python3 -c 'import json,sys;s=json.load(sys.stdin)["result"]["sync_info"];print(s["latest_block_height"],s["catching_up"])'
done
```

Epoch group:

```bash
ssh -p 18221 decentai@xj7-5.s.filfox.io \
  'docker exec api curl -s http://node:1317/productscience/inference/inference/epoch_group_data/CURRENT' \
  # if CURRENT unsupported, query latest epoch index from epoch_info / logs
```

---

## Phase overview

```text
0  Baseline ready (2.15 / healthy ep group / decode off / artifacts wiped)
1  Monitor one successful PoC (all hosts baseline)
2  Inference: disk clean + pull all three images (+ optional binary build/staging)
3  If time: swap mlnodes only (all 4)
4  Monitor PoC (still prefill; decode_max_tokens=0)
5  If OK: swap api + node containers (all 4)
6  If blocks_left>100: cosmovisor binary swap now
7  Else: wait for PoC; if OK, binary swap in next Inference
8  If >15 min to next PoC: gov decode_max_tokens=256, vote all 4, confirm PASSED before PocStart
9  Monitor first decode PoC → report
```

---

## Phase 0 — Baseline gate (before Phase 1)

On all four hosts, confirm:

- [ ] Heights advancing in lockstep; `catching_up=false`
- [ ] Running stack is the intended **baseline** (e.g. 2.15), not a half-migrated decode mix
- [ ] Epoch group: **4 members**, `total_weight > 0`
- [ ] `decode_max_tokens` is **0** or absent on all PoC models
- [ ] Stale `poc-artifacts` wiped (see Hard rule 4)
- [ ] No TMKMS `height regression` errors in logs

**Abort** the rollout if Phase 1 baseline PoC fails.

---

## Phase 1 — Monitor one successful baseline PoC

Watch full cycle: generate → validate → SetNewValidators.

**Pass:** success criteria table (prefill expected: `scheme=prefill`, 24-byte leaves OK while decode is off).

Record: `poc_start` height, epoch index, commit counts per host.

**Fail:** stop; fix baseline before pulling decode images.

---

## Phase 2 — Disk clean + pre-pull (Inference only)

Immediately after Phase 1 settles (`phase=Inference`):

### 2a. Disk

```bash
df -h /srv/dai
# prune only what you need; keep room for 3 large images
docker system df
# example (careful): docker image prune / builder prune as appropriate
```

### 2b. Pull (do **not** recreate yet)

```bash
docker pull ghcr.io/gonka-ai/mlnode:decode-poc-int
docker pull ghcr.io/gonka-ai/api:decode-poc-int
docker pull ghcr.io/gonka-ai/inferenced:decode-poc-int
```

Optional: also pull `ghcr.io/gonka-ai/mlnode:decode-poc-int-f08ea3e` and confirm digests match `decode-poc-int`.

### 2c. Optional — stage cosmovisor zips early

Run §§0–2 of [testnet4-binary-swap-decode-poc-int-v0.2.16.md](./testnet4-binary-swap-decode-poc-int-v0.2.16.md) on each host (**backup + build**, do **not** install). Saves time for Phase 6/7.

**Gate:** `phase=Inference`, prefer `blocks_left > 60`.

---

## Phase 3 — Swap mlnodes only (if time before next PoC)

Coordinated on all four:

```bash
# ensure compose / override points mlnode at decode-poc-int
dc pull mlnode-308   # service name may be mlnode-308 / join-mlnode-308-1 — use `dc ps`
dc up -d --no-deps --force-recreate <mlnode-service>
```

Verify admin nodes show `decode-poc-int` / `f08ea3e` version.

- **Do not** touch api/node.
- **Do not** set `decode_max_tokens`.

If `blocks_left` is tight → skip; run next PoC on old mlnode, swap in the following Inference window.

---

## Phase 4 — Monitor PoC after mlnode swap

Still **prefill** (`decode_max_tokens=0`).

**Pass:** success criteria (4 commits, `votedInvalid=0`, healthy epoch).  
**Fail:** do **not** proceed to api/node swap.

---

## Phase 5 — Swap api + node containers

Only if Phase 4 passed. Inference only; all four hosts:

1. Point compose / `docker-compose.decode-poc-int.override.yml` at:
   - `ghcr.io/gonka-ai/inferenced:decode-poc-int`
   - `ghcr.io/gonka-ai/api:decode-poc-int`
2. Recreate:

```bash
dc up -d --no-deps --force-recreate node
# wait until height advances on this host
dc up -d --no-deps --force-recreate api
```

3. Wipe poc-artifacts after api recreate (Hard rule 4).
4. Confirm containers Up, no restart loop; if api NATS-loops, clear `.dapi/.nats` (see binary-swap doc §5).

Heights must stay aligned across the fleet before Phase 6.

---

## Phase 6 / 7 — Cosmovisor binary swap

Follow [testnet4-binary-swap-decode-poc-int-v0.2.16.md](./testnet4-binary-swap-decode-poc-int-v0.2.16.md) §§3–4 (and §6 rollback if needed).

| Condition | Action |
| --- | --- |
| After Phase 5, Inference, **`blocks_left > 100`** | **Phase 6:** install bins on all 4 in one window |
| Not enough time | **Phase 7:** wait for next PoC; if it passes, install bins in the next Inference (`blocks_left > 100`) |

Post-install checks (all hosts):

- [ ] `docker exec node readlink /root/.inference/cosmovisor/current` → `upgrades/v0.2.16` (or your target slot)
- [ ] `inferenced` / `decentralized-api` versions match expected `decode-poc-int` build
- [ ] Heights lockstep; voting power intact

**Do not** run Phase 8 until this is done everywhere.

---

## Phase 8 — Gov: `decode_max_tokens=256`

**Preconditions:**

- [ ] mlnode + api/node images + cosmovisor bins all `decode-poc-int` on all 4
- [ ] Epoch group healthy (4 members, weight > 0)
- [ ] Wall clock **> 15 minutes** (and voting period ends **before** next `PocStart`)

Steps:

1. Export full params with a client that understands `decode_max_tokens` (prefer `docker run … ghcr.io/gonka-ai/inferenced:decode-poc-int` or the new cosmovisor binary). Host CLI 0.2.15 may be unable to encode the field.
2. Set every PoC model:

   ```json
   "decode_max_tokens": "256"
   ```

3. Submit `MsgUpdateParams` via gov (deposit per chain params; funded key on genesis / 18221).
4. Vote **yes** from all four validators immediately.
5. Confirm `PROPOSAL_STATUS_PASSED` and REST:

   ```bash
   docker exec api curl -s http://node:1317/productscience/inference/inference/params \
     | python3 -c 'import json,sys; ms=json.load(sys.stdin)["params"]["poc_params"]["models"];
   print([(m["model_id"], m.get("decode_max_tokens")) for m in ms])'
   ```

If the proposal would still be voting at PocStart → **do not submit**; wait until after that PoC.

---

## Phase 9 — Monitor first decode PoC → report

**Expected generate logs (mlnode):**

```text
scheme='decode' decode=True max_tokens=256
```

**Expected validation:** leaf length **257** (not 24); `votedInvalid=0`; 4/4 commits; new epoch 4 members / `total_weight > 0`.

### Report template

```text
Decode PoC report
- height / epoch / poc_start:
- decode_max_tokens on-chain:
- mlnode scheme / max_tokens:
- commits (per host):
- votedInvalid (per host):
- epoch_group members / total_weight:
- anomalies:
```

**Stop-the-line:** any 24-vs-257 mismatch, Invalid majority, empty/zero epoch, or missing commit on a host (e.g. OffChainValidator `numNodes=0`) — pause before further gov or rollback.

---

## Incident cheat-sheet

| Symptom | Likely cause | Action |
| --- | --- | --- |
| `got 24 bytes, expected 257` | decode on-chain but generate/artifacts still prefill, or **stale poc-artifacts** after height reuse | Wipe artifacts; ensure bins+images+param aligned; do not leave decode on mixed fleet |
| `Invalid majority` → 1 member weight 0 | miners voted invalid | Same as above; expect sticky empty group next round |
| `No voting powers for model` | previous epoch total_weight 0 / empty group | Recovery (rollback+TMKMS+artifact wipe+re-gov) or chain reset — waiting will not fix |
| TMKMS `height regression` | signed higher height than rolled-back chain | Reset TMKMS state to target height on all hosts |
| api Restarting / NATS | recreate without env or dirty `.nats` | `stop api`; `rm -rf .dapi/.nats`; recreate with full compose |
| Host missing PoC commit | mlnode/api unavailable (`numNodes=0`, dropped callbacks) | Fix node availability before relying on that host’s weight |

---

## Checklist (print / tick live)

- [ ] Phase 0 baseline gate
- [ ] Phase 1 baseline PoC pass
- [ ] Phase 2 disk + pulls (+ optional binary staging)
- [ ] Phase 3 mlnode swap (or deferred)
- [ ] Phase 4 PoC pass (prefill)
- [ ] Phase 5 api+node container swap + artifact wipe
- [ ] Phase 6 or 7 cosmovisor binary swap + verify
- [ ] Phase 8 gov `decode_max_tokens=256` PASSED before PocStart
- [ ] Phase 9 decode PoC pass + report

---

## Related docs

- Binary install / rollback: [testnet4-binary-swap-decode-poc-int-v0.2.16.md](./testnet4-binary-swap-decode-poc-int-v0.2.16.md)
- Legacy single-block recovery pattern (not for empty-epoch): [manual-recover.md](./manual-recover.md)
