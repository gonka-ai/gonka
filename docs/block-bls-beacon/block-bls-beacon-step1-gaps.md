# Block BLS beacon — Step 0/1/2/3 remaining gaps

**Scope:** what is still open after the Step 1 implementation, its review and the M1/M2 hardening (2026-11-09) for [`proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md`](../../proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md).
**Audience:** engineers implementing Steps 2–5 and whoever runs the Step 0 signer rollout.
**How to use:** each gap has a severity, evidence, a proposed fix and a **gate** (the latest point it must be closed). Close a gap by moving it to [Resolved](#resolved-in-the-step-1-review) with the commit/test that closes it.

Severity: 🔴 high (can halt the chain or take validators offline) · 🟠 medium (security exposure or a trap for a later step) · 🟡 low (hardening, ergonomics).

---

## Open gaps

### ✅ H3. Step 4 design would let one validator halt the chain (designed away and implemented 2026-11-11)

**Evidence.** The original Step 4 injected the whole extended commit and rejected a proposal whenever an included extension failed `ValidateBeaconExtensionStructure`. A proposer cannot drop an extension (the injected commit had to match `ProposedLastCommit`, and extensions are signed by their validators), so a single byzantine validator would have made every proposal containing its precommit invalid.

**Resolution (plan Steps 4–5 rewritten 2026-11-11).** Block `h+1` must carry σ_h or, in miss rounds, a proof of miss (the extended commit). A proposal is rejected only for the proposer's own choices (missing beacon tx, wrong σ, false proof of miss). Inside a proof of miss, a malformed or invalid extension only counts as zero valid slots, so one byzantine validator cannot make a proposal invalid. `VerifyVoteExtension` stays size-cap only (R2).

**Tests to keep:** app test where one validator's extension is garbage: σ is still built from the other slots (`OK` when ≥ threshold), and a proof of miss containing the garbage extension is accepted (it counts zero); testermint case with a deliberately corrupt share on one node.

**Gate:** Step 4. Done: `TestProcessProposalAccepts` (proof of miss with a garbage extension accepted), `TestBlockBeacon_ProofOfMiss`, testermint `withholding proposer is replaced`.

### 🟠 M4. Step 4 trap: the pass-through `PrepareProposal` decodes every tx (now plan rules R4/R5)

**Evidence.** `app/beacon_abci.go` wraps `baseapp.NewDefaultProposalHandler(NoOpMempool, …)`, whose NoOp branch calls `TxDecode` on every tx. Step 4 injects the extended commit as `Txs[0]`, which is not an SDK tx. The handler then returns an error, and the SDK falls back to the raw `req.Txs` (`baseapp/abci.go:481-483`). The injection is lost and no byte budget is applied.

**Fix.** In Step 4, pass only the mempool txs to the pass-through, with `MaxTxBytes` reduced by the size of the injected tx, then prepend the injected tx. Add a test asserting the injected tx survives a tight `MaxTxBytes`.

**Plan:** encoded as rules **R4** (reserved leading txs: build `ecTx` first, budget, pass-through on `req.Txs` only, error path keeps `ecTx`) and **R5** (`ProcessProposal` interprets only `Txs[0]`) in the plan's [Implementation rules](../../proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md#implementation-rules-normative-for-steps-18); Step 4 PrepareProposal/ProcessProposal text and tests rewritten accordingly; Step 7 `TestPrepareProposalReservedTxs`.

**Gate:** Step 4 (implementation + those tests).

### 🟠 M5. Capability activation boundaries (now plan rule R6)

**Evidence.** At upgrade height `U`, precommits (and `ExtendVote`) for `U` are made by the **old** binary, which then halts in `FinalizeBlock(U)`. The new binary's `PrepareProposal(U+1)` therefore sees an extended commit whose extensions are empty.

**Broader issue found while writing the rule.** The original Steps 4–5 keyed injection and aggregation on `E` (the vote-extension enable height). On mainnet `E` passes at v0.2.18, long before Steps 3–5 ship. The first Step 4 binary would then demand an injected commit in blocks the old binary proposed without one, and reject them.

**Fix.** Rule **R6** in the plan's [Implementation rules](../../proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md#implementation-rules-normative-for-steps-18): each capability has its own on-chain activation height (`BeaconActivation{ext_from, inj_from, agg_from}` in x/bls state), written by the upgrade handler that introduces it, never inferred from `E`. The activation block of each capability was proposed and precommitted by the old binary: no injection in block `inj_from` (→ `MISS_NO_EC`), empty extensions in the first injected commit (→ `MISS_*`), never `REJECT` or an error. Steps 3/4/5 text, tests, the Step 7 checklist (`TestBeaconActivationBoundaries`) and a Step 8 "capability activation" e2e case were updated.

**Gate:** Steps 3–5 (implementation + those tests).

### 🟠 X1. Step 0 rollout operations not started

Tooling is ready; none of the operational phases have started:

| Phase | Owner | Notes |
|---|---|---|
| 1. Publish image (`make tmkms-release VERSION=<tag>`) and bump `deploy/join/docker-compose.yml` `tmkms.image` | needs ghcr push rights | M1/M2 done. `make tmkms-release` pushes only the **keygen** image; push `tmkms-softsign-with-import` too if operators use it |
| 2. Testnet rehearsal on a node with an existing `.tmkms` volume | ops | The cloud testnet recreates the tmkms PVC on every deploy, so it does not exercise the migration |
| 3–4. Operator announcement + readiness tracker (≥ 95% of voting power) | coordinator | Channel and coordinator not chosen |
| 5. v0.2.18 proposal | — | Gated on phase 4. Rerun the live-params gate (below) right before tagging |

**Release gate command** (closes review finding M3 at release time):
```bash
cd inference-chain
GONKA_LIVE_RPC=http://node1.gonka.ai:8000/chain-rpc go test ./app/upgrades/v0_2_18/ -run LiveRPC -v
```

### 🟡 L1. Short grace window after the upgrade

`VoteExtensionsEnableDelay = 100` blocks ≈ **8.8 minutes** at mainnet's 5.27 s average block time (measured over heights 6,971,000–6,971,500). The proposal gives days of notice, but an operator who missed the tmkms upgrade has under ten minutes once `U` passes.

**Fix.** Raise the delay to about one day of blocks (≈ 16,400) or one epoch, or keep 100 and state the wall-clock time in the proposal text. Decide before tagging v0.2.18.

### 🟡 L2. CI does not build the tmkms image

A regression like the tmkms 0.15 wire bug (see the plan's F7) is only caught by a manual testermint run.

**Fix.** CI job: build `Dockerfile_keygen` (verified locally for linux/amd64 and arm64 on 2026-11-09), assert `tmkms version` = `0.14.0`, start it on the legacy fixture volume (migration path), and check that the proto `Vote` type in the image has `extension` (tag 9) and `extension_signature` (tag 10).

### 🟡 L3. Migration enables `sign_extensions` on every `[[chain]]`

`migrate-toml.sh` inserts the key under each `[[chain]]` header, so a tmkms instance serving several chains signs extensions for all of them. Harmless (CometBFT drops unused extension signatures while extensions are off), but surprising.

**Fix.** Document it, or restrict insertion to the `[[chain]]` block whose `id = "gonka-mainnet"` when such a block exists.

### 🟡 L4. Supply chain of the tmkms build (mostly closed)

Done with M1/M2: base images pinned by digest, the tmkms crate verified by sha256 against the crates.io index, a committed reviewed `Cargo.lock`, `cargo build --locked`. Remaining: crates are still fetched from crates.io at build time (not vendored).

**Fix (optional).** `cargo vendor` the dependency tree into a build context or an internal mirror.

### 🟡 L5. Behaviour change for nodes with an app-side mempool

Nodes that set `mempool.max-txs ≥ 0` no longer validate txs in `ProcessProposal`; they now behave like the default NoOp nodes. This is intended (identical proposal evaluation across nodes), and the node logs a startup warning.

**Fix.** Mention it in the v0.2.18 release notes.

### 🟡 L6. Extended-commit storage bound

With the tolerant `VerifyVoteExtension` (H1 fix), each validator may attach up to `64 × i_total_slots + 512` bytes (≥ 4 KiB). CometBFT persists extended commits per height, so a byzantine validator can bloat block-store growth up to that cap per height (≈ 64 KiB/validator/height at 1,000 slots).

**Fix.** Step 3 should tighten the cap to the owner's actual slot range once ownership is known (`ValidateBeaconExtensionStructure`). Add the expected growth to the ops docs.

### 🟡 P1. Pre-existing: consensus key baked into the import image

`tmkms/docker/Dockerfile_import_keys` copies `priv_validator_key.json` (and the state file) into image layers at build time. If that image is ever pushed, the consensus key leaks. Outside Step 1 but in the same files.

**Fix.** Mount the key at runtime (volume or secret) and import it in `init.sh` instead of at build time.

### 🟡 P2. Local tree hygiene

An empty untracked `devshard/cmd/devshardd/inference/zz_frames_probe_test.go` (0 bytes, no `package` clause) broke `make devshardd-build`. It was moved aside, not deleted, during the 2026-11-09 rebuild. Do not restore it as an empty file.

### 🟡 S2-3. dAPI reconcile queries full epoch data for epochs it cannot export

**Evidence.** For the current and next epoch the reconcile loop calls `GetOrRecoverVerificationResult` when the file is missing. For a non-participant, or while the next epoch's DKG is not final, that is a full `EpochBLSData` query every 2 minutes (backoff).

**Fix (if it shows up in load):** remember "not a participant" per epoch after a successful query with the participant list, or check `dkg_phase` with a lighter query first.

**Gate:** none (operational).

### 🟡 S3-1. Block time with partials measured only within one run

**Evidence.** `real extensions after dkg` saw 5.01 s → 5.56 s mean block interval (+11%) between the empty-extension window and the partials window of the same run. Signing takes 2–6 ms, and the windows fall in different epoch phases, so the cause is unclear; the plan's gate asks for ≤10% against a VE-off baseline run.

**Fix.** Step 7 benchmark: same cluster config with and without partials over the same epoch phases (or a long window), and per-block precommit timing from CometBFT metrics.

**Gate:** Step 7.

### 🟡 S3-2. Step 3 activation rides in v0.2.18

**Evidence.** `ActivateBlockBeacon` runs in the v0.2.18 handler (R6 allows it; nothing is signed before `E = U+100`). If v0.2.18 must ship without Step 3 (e.g. the tmkms gate is met before Step 3 is reviewed), the call moves to the next upgrade.

**Gate:** decision before tagging v0.2.18.

### 🟡 S3-3. Extensions are carried but not yet consumed

**Evidence.** From Step 3 on, every precommit carries ~52 bytes per owned slot (≈5.2 KB per block for 100 slots), stored in CometBFT's extended commits, while nothing aggregates them until Step 5. See L6 (extended-commit storage bound).

**Gate:** Steps 4–5.

### ℹ️ S3-4. `TestBLS_SybilAttack_Full` exceeds the default test timeout under `-race`

**Evidence.** Pre-existing (since v0.2.12): ~43 s without `-race`, over 10 min with it, so `go test -race ./x/bls/keeper/` times out. Everything else in the package passes under `-race` (`-skip TestBLS_SybilAttack_Full`).

**Fix (optional).** `testing.Short()` guard or a `-timeout` in the race CI job.

### ℹ️ U1. Upstream tmkms

tmkms 0.15.0 drops the extension signature on the wire (iqlusioninc/tmkms#1174, no fixed release; crates.io newest is 0.15.0). Revisit when upstream ships a fix, since 0.14.0 needs the old toolchain and old crates (M1).

---

## Resolved in the Step 1 review

| ID | Finding | Resolution | Verified by |
|---|---|---|---|
| **S2-2** 🟡 / review **M1** 🟠 | Share files sat in plaintext in `.inference`, readable by every container mounting it (versiond read-only), included in backups/copies of the home | Memory-only `bls-beacon` volume (`tmpfs`, `mode=0700`, 16 MiB) mounted at `/root/.inference/bls-beacon` in `node` and `api` only (`deploy/join/docker-compose.yml`, `local-test-net/docker-compose-base.yml`). Defaults unchanged; versiond sees an empty mount point; the api re-exports from chain when both containers restart. Warm-key encryption was rejected: the node would need the warm key + `KEYRING_PASSWORD` (which sits in plaintext in `config.env`), so it adds risk without protection | Live compose check (shared node↔api, tmpfs 0700, empty for versiond and on the host disk, survives an api restart, cleared when both stop); `compose-contract_test.sh`; testermint `share file exported` asserts the tmpfs mount in node and api (2/2 gates passed, 2026-11-10) |
| review **M2** 🟠 | The node accepted any participant's shares as long as they matched committed state, so a home copied from another validator would sign with that validator's slots | `SelfIdentity`: the node reads its consensus address from the local CometBFT `/status` (works with tmkms) in the background; `EpochKeys.OwnerConsAddress` resolves the slot owner participant → validator (`ValAddress(acc)`) → consensus address in committed state; a mismatch is `invalid`, an unknown identity is retried every block, an owner without a validator every 20 blocks | `app/beacon` (foreign owner, waits for identity, owner without validator, identity before scalar checks), `app` (`epochKeysFromData` owner resolution, `/status` parsing over TCP and unix socket, retry/close, end-to-end with a real genesis validator: foreign identity rejected, own accepted), dAPI round trip; testermint `share file exported` (logged identity == `/status`, a file copied from genesis to join2 rejected with `this node signs as`; 2/2 gates passed, 2026-11-10) |
| **S2-1** 🟡 / review **L5** | Share verification ran inside `ExtendVote` (committed-state reads + one G2 scalar mult per slot on the precommit path) | Background verifier: the beacon PreBlocker records the committed height (atomic); the provider worker (1 s tick + kick on file load) verifies new files on a read-only context over that immutable IAVL version (`CacheMultiStoreWithVersion`, as gRPC queries do). `Get` in `ExtendVote` only reads memory; the next epoch's file is verified as soon as it lands (pre-warm). A "DKG not final" verdict is retried at the next committed height in this mode: the first cluster run showed the file lands ~200 ms after the DKG-completing block commits, one version before the verifier sees it, and a 20-block retry left fresh shares unused for ~100 s | `app/beacon` `TestProviderVerifiesInBackground` (Get never calls committed keys), `TestProviderBackgroundRetryFollowsCommittedHeight`; `app` end-to-end test waits for the worker's verdict, all under `-race` |
| **S2-6** / review **L1** | Any committed-keys error rejected the file until it changed | `beacon.ErrKeysInvalid` marks deterministic verdicts (DKG `FAILED`, range outside the epoch); every other error (store, not found yet, validator lookup, precompute) leaves the file unverified and retries after 20 heights; `Statuses` reports the retry reason | `TestProviderRetriesTransientKeysError`, `TestEpochKeysFromDataErrorClasses`, end-to-end (epoch without BLS data → `unverified`, `has no BLS data`) |
| **S2-7** / review **L2** | Reconcile only checked the file format, so a stale or foreign well-formed file was never repaired | Reconcile compares the file's checksum with the file this node's final result produces (cache, or recovered from chain once per epoch per process) and rewrites on mismatch; removes the file when the final result says this node holds no slots | `TestReconcileReplacesStaleOrForeignFile`, `TestReconcileRemovesFileOfNonParticipantEpoch`, `TestReconcileChecksExistingFileOnceAfterRestart`; testermint `share file exported` (join2's dAPI rewrites the foreign file planted while it was stopped, node reloads it as valid) |
| **S2-8** / review **L3** | Reader followed symlinks, could block on a FIFO (hanging node startup) and read `/dev/zero` without limit; mode checked after reading | `sharefile.ReadFile`/`ReadPrivateFile`: `Lstat` must be a regular file, open with `O_NOFOLLOW\|O_NONBLOCK`, same-file re-check on the handle (retried if atomically replaced meanwhile), `io.LimitReader`; the provider uses `ReadPrivateFile` (mode checked before content). `WriteAtomic` replaces a planted symlink instead of following it | `TestReadRejectsNonRegularFiles` (symlink, `/dev/zero`, FIFO without blocking, directory), `TestReadPrivateFileRejectsOpenModes`, `TestWriteAtomicReplacesSymlink`, `TestProviderIgnoresSpecialFiles`, concurrent-reader test ×3 under `-race` |
| **S2-9** / review **L4** | A dAPI/node share-dir mismatch was silent | The provider warns once per epoch when `Get` has asked for an epoch with no file for 20 heights, naming the directory and both config keys; never for signing epoch 0 (no BLS epoch yet; the first cluster run warned for it). (Step 3 adds the `no_shares` metric.) | `TestProviderWarnsOnceWhenSharesAreMissing` |
| **S2-10** / review **L6** | Export ran synchronously in the DKG-completion handler (disk + first chain-id query, up to 10 s) | The handler only snapshots the result (copies shares and group key) and enqueues it; a worker writes the file (queue of 8, a full queue drops and reconcile writes it on the next tick); the chain id is prefetched when the exporter starts. Without a running worker (tests) the write stays synchronous | `TestStoreHookIsAsyncAndSnapshotsShares` (hook returns while the chain-id query blocks, the file has the pre-mutation shares, 24 more results never block) |
| **S2-5** 🟠 / review **M3** | The signing epoch switches before its DKG completes, so beacon rounds would miss for part of every epoch | Rule R9 implemented: `BeaconKeyEpoch` switches at `dkg_completed_height + 1 + beacon_key_switch_delay`; the dAPI keeps the active key's and the candidate's files | keeper key-switch tests; testermint `real extensions after dkg` (switches at rounds 62 and 102 on all nodes, no gap) |
| **S2-4** 🟡 | Testermint Step 2 gates not yet run | Run on rebuilt api + node images; the epoch wait now uses the effective (signing) epoch, and the share cases use 40-block epochs | `BlockBeaconTests` `share file exported`, `share file re-exported after dapi restart`: 2/2 passed (2026-11-09) |
| **H1** 🔴 | `VerifyVoteExtension` rejected any non-empty extension, so binary-version skew (patch release, rolling swap, first Step 3 height) could reject precommits and halt the chain | Accept any extension up to `beaconExtensionMaxBytes(i_total_slots)` (64 B/slot + 512 B, floor 4 KiB); reject only oversized ones. Content is validated deterministically in Steps 4–5 | `app/beacon_abci_test.go` (`TestBeaconVerifyVoteExtension`, `TestBeaconExtensionMaxBytes`); `app/beacon_ve_integration_test.go` (unknown payload accepted, 1 MiB rejected, real committed x/bls params) |
| **H2** 🔴 | Rolling back to tmkms 0.12 after migration took the validator offline (`unknown field 'sign_extensions'`) | `tmkms/docker/rollback-toml.sh` (bundled in both images), rollback procedure first in `tmkms/Readme.md` | `rollback-toml_test.sh` (migrate → rollback is byte-identical, state file untouched); `rollback-smoke_test.sh` runs the real 0.12 image: rejects before, starts after. Both in CI |
| **M3** 🟠 | Upgrade validity was checked only against genesis params; a rejection would halt the chain at `U` | Pinned live mainnet params (height 6,971,588) as a test vector plus an opt-in live-RPC gate | `v0_2_18/vote_extensions_mainnet_test.go`; live run against `node1.gonka.ai` at height 6,971,609 passed |
| **M1** 🟠 | tmkms 0.14.0 locked vulnerable / yanked crates | Build from the checksum-pinned crate with a reviewed lockfile (`tmkms/build/tmkms-0.14.0.Cargo.lock`, provenance and allowlist in `tmkms/build/README.md`). Patched: `bytes`, `time`, `tracing-subscriber`, `anyhow`, `keccak`, `futures-*`, `spin`, plus non-compiled `curve25519-dalek`, `h2`, `openssl`, `tokio`, `rand`. The `rust:1.79` pin is gone (current `time` builds on Rust 1.91). Accepted residuals need major upstream bumps and are not exploitable in our configuration: `crypto-bigint` (secp256k1 path only), `idna` (local config input), `adler`/`paste` (unmaintained) | `cargo audit` CI job (`lockfile-audit`, explicit allowlist); builds on Rust 1.91 for arm64 and amd64 |
| **M2** 🟠 | Signer ran in a full Rust toolchain image as root | Multi-stage build: `rust:1.91-slim-bookworm` builder → `debian:bookworm-slim` runtime (both pinned by digest), only the binary + Python for `tmkms-pubkey` + scripts (1.81 GB → ~150 MB); tmkms runs as uid 10001 via `setpriv --no-new-privs` after root-only setup (`TMKMS_RUN_AS=root` opt-out) | Smoke on fresh and legacy root-owned volumes: PID 1 is `tmkms` with uid 10001, no toolchain in the image; testermint tmkms cases (see plan) |
| **P3** 🟡 (found during M2) | `tmkms init` at build time baked one `kms-identity.key` into the public image, so every install shared the same secret-connection identity | Template ships no secrets; `init.sh` generates the identity key per install when missing (existing volumes keep theirs; rotation optional) | Two fresh installs get different keys (smoke) |

Earlier (during the Step 1 testermint runs): tmkms 0.15.0 → 0.14.0 switch, and five testermint harness bugs. See the plan's [Step 1 testermint results](../../proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md#step-1-testermint-results-2026-11-09).
