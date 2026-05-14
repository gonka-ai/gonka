# Phase 3 block oracle: unit acceptance & compose integration scenarios

This file collects **[`devshard/docs/testenv.md`](../../docs/testenv.md)** **§7.1.1** (Phase 3 multi-validator mock oracle — unit acceptance) and **§7.2** (docker compose integration rows **I1–I10**) in one place next to `devshard/testenv/scenarios` tests.

**Canonical source:** update acceptance wording in `testenv.md` first, then mirror here so the two stay aligned.

**Code pointers:**

- Unit / in-process: `devshard/blockoracle/...`, `devshard/testenv/config`, `heightsync_anchor_e2e_test.go` in this directory.
- Compose citest (I1, I2a, I2b, I9, §7.7 wiring): `devshard/testenv/citest/`, driver `devshard/testenv/scripts/run-stack-citest.sh`.

---

## 7.1.1 Phase 3 acceptance tests (multi-validator block oracle)

Named cases that pin the Phase 3 behavior end-to-end. All are unit tests
inside `devshard/`, run under `make ci-unit`, and included in the
aggregate budget above. Integration coverage for the same concerns lives
in §7.2 rows I9 and I10.

| Test                                                             | Package                  | Asserts                                                                                                                                                                             |
|------------------------------------------------------------------|--------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `TestMockObserver_MultiValidator_QuorumFloor`                    | `blockoracle/observer`   | Over ≥ 200 blocks from a 10-equal-power mock, every commit retains voting power strictly > 3/4 of the total; both full-sign and partial-sign blocks are observed.                   |
| `TestMockObserver_PowerWeighted_HeavyAlwaysSigns`                | `blockoracle/observer`   | With a non-uniform set `[5,1,1,1,1,1]`, the heavy validator is present in every block; pinned `Verifier` accepts the whole stream.                                                  |
| `TestMockObserver_SignerRotation`                                | `blockoracle/observer`   | Over 500 blocks with 10 equal-power validators, every validator is dropped from at least one block — the drop algorithm is not stuck on a fixed subset.                             |
| `TestMockObserver_DeterministicForSameSeed`                      | `blockoracle/observer`   | Same `(seed, validators)` ⇒ byte-identical headers across restarts, including `Commit.Signatures` order.                                                                            |
| `TestMockObserver_SingleValidator_FullSign`                      | `blockoracle/observer`   | Degenerate cases (N ≤ 3) sign with the full set — drop budget collapses to zero.                                                                                                    |
| `TestMockObserver_RejectsBadConfig`                              | `blockoracle/observer`   | Empty validator list, malformed addresses, duplicates, zero power are all rejected by the constructor.                                                                              |
| `TestVerifier_RejectsTamperedSignatureInMultiSig`                | `blockoracle/verifier`   | In a valid 10-sig commit, flipping one byte of one signature causes the verifier to reject — the per-signature ecrecover path is exercised inside the multi-signer setting.         |
| `TestVerifier_RejectsForeignSignatureInMultiSig`                 | `blockoracle/verifier`   | Appending a valid signature from a signer outside the pinned set to an otherwise-correct 10-sig commit causes rejection with "not in pinned set", regardless of aggregate power.    |
| `TestVerifier_RejectsInsufficientVotingPower`                    | `blockoracle/verifier`   | Only 1 of 3 equal validators signs; verifier rejects with "insufficient voting power" (covers manually crafted < 2/3 headers the mock never emits).                                 |
| `TestVerifier_RejectsDuplicateSignatures`                        | `blockoracle/verifier`   | Two sigs from the same validator in one commit are rejected — prevents power-inflation via sig duplication.                                                                         |
| `TestService_LatestAfterAdvance` / `TestService_StreamDelivers*` | `blockoracle/standalone` | End-to-end stream against a 10-validator pinned `Verifier` accepts every header; each commit carries ≥ 8 signatures (the > 3/4 floor).                                              |
| `TestService_HostTrustMode`                                      | `blockoracle/standalone` | Client with `Verifier: nil` forwards the full `Commit.Signatures` set untouched and records zero rejections — host trust mode preserves proof material for later auditing.          |
| `TestClient_RejectsTamperedHeader`                               | `blockoracle/client`     | With a pinned verifier, a header with tampered `AppHash` is rejected on ingest and dropped from the cache.                                                                          |
| `TestClient_TrustModeAcceptsEverything`                          | `blockoracle/client`     | With `Verifier: nil`, even a tampered header is forwarded to subscribers with all signatures intact — hosts trust the authenticated oracle and defer cryptographic checks.          |
| `TestConfig_HeightSyncValidators*`                               | `testenv/config`         | YAML `height_sync.validators` list parses, applies `power: 1` default, rejects empty lists, TODO placeholders, and duplicate-address entries.                                       |

---

## 7.2 Integration tests (docker compose)

Each is a Go test that invokes `docker compose`, waits for health, runs the
assertion, and tears down.

| #   | Scenario                                                                | Assertion                                                                                                                                                              |
|-----|-------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| I1  | Bootstrap                                                               | All containers healthy ≤ 30 s; `GET height-sync:9100/block/latest` returns `height > 0`.                                                                               |
| I2a | Height convergence (protocol)                                           | One tight loop: GET each host's `127.0.0.1:<public_metrics_port>/metrics`; parse `devshardd_height_at_latest_nonce`; `max(H_i)−min(H_i) ≤ 1`.                            |
| I2b | Height convergence (observability)                                      | VictoriaMetrics instant query on the same gauge; `max(H_i)−min(H_i) ≤ 3` (scrape skew).                                                                                |
| I3  | Hostile header rejection                                                | A test double replaces height-sync and emits a header with tampered `AppHash` but valid old signature; every `mock-dapi` rejects it; devshardd continues on cache.     |
| I4  | Height-sync outage                                                      | Stop `height-sync` for 2×`BLOCK_INTERVAL`; `mock-dapi.Latest()` returns cached header with `stale=true`; verdicts requiring fresh `H(V)` enter `pending_verdicts{stale}`. Restart; full reconvergence ≤ 2×`BLOCK_INTERVAL`. |
| I5  | Inference happy path                                                    | `devshardctl chat ...` via `devshardd-0`; response returned; `Diff` on all hosts contains `MsgStartInference @ R_req` and `MsgConfirmStart @ R_req+1`.                 |
| I6  | Gossip consistency                                                      | After 50 inferences, diffs on all N devshards hash-match at every nonce.                                                                                               |
| I7  | Settlement                                                              | `devshardctl settle`; `mock-chain`'s `SettleEscrow` accepts the payload; log shows `VerifySettlement = true`.                                                          |
| I8  | Host crash and recovery                                                 | `docker stop devshardd-2` mid-session; protocol continues; restart; host replays `Diff` from storage and reconciles height; no corruption.                             |
| I9  | Multi-validator stream vs. auditor                                      | `devshardctl` pins the 10-validator set and subscribes to `height-sync` for 20 consecutive headers; every header verifies; at least one commit carries < 10 but ≥ 8 signatures (exercises the partial-quorum drop path end to end). |
| I10 | Foreign-signature injection                                             | A test double in front of `height-sync` appends an 11th signature from a non-pinned key; `devshardctl` rejects with `not in pinned set` on the first poisoned header; hosts in trust mode keep ingesting (cache records full `Commit.Signatures`) and their downstream proofs still fail external verification. |

Runtime budget: ≤ 5 min. Runs on PR. **Status:** I1, I2a, I2b, and I9 are implemented
in `devshard/testenv/citest` (`-tags=testenvci`, same docker-compose run as
§7.7). I3–I8 and I10 are not yet automated.
