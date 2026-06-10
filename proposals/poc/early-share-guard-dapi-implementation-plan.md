# Implementation plan: DAPI-only early PoC share guard

Steps are intentionally small and ordered. Each references the files/functions to
touch. No code here — just what each step does. See `early-share-guard-dapi.md`
for the design and rationale.

All work is in `decentralized-api/` (DAPI). No chain changes.

## Phase 1 — Configuration

1. Add an `EarlyShareGuardConfig` struct in `decentralized-api/apiconfig/config.go`
   with fields for `enabled`/mode, `first_fraction`, `threshold_ratio`, and
   `require_prefix_proof`. Use `koanf`/`json` tags to match the other config
   structs. No retention field — early-guard rows reuse the existing stage-based
   PoC pruning (see Phase 7).
2. Add that struct as a new field on `Config` in
   `decentralized-api/apiconfig/config.go` (next to `PoCParams`).
3. Set defaults (guard disabled, `first_fraction = 1/3`, `threshold_ratio = 0.5`,
   `require_prefix_proof = true`) wherever config defaults are initialized in
   `decentralized-api/apiconfig/config_manager.go`.
4. Confirm the new config round-trips through load/save in
   `decentralized-api/apiconfig/config_manager.go` (and its migration test if one
   asserts the full struct).

## Phase 2 — Local persistence

5. Create a new local store package, e.g. `decentralized-api/poc/earlyshare/`,
   following the existing local-DB pattern used by
   `decentralized-api/apiconfig/sqlite_store.go` and
   `decentralized-api/internal/bls` persistence.
6. Define the three tables from the design (`poc_early_checkpoints`,
   `poc_early_guard_state`, `poc_early_capture_runs`) and a schema/migration init
   in that package.
7. Add store methods: upsert/get early checkpoints by `(stage, model_id)`,
   read/update guard state by `(participant_address, model_id)`, and
   read/update capture-run status by `(stage, model_id)`.
8. Add a `DeleteStage(stage_height)` method that removes the
   `poc_early_checkpoints` and `poc_early_capture_runs` rows for a stage; leave
   `poc_early_guard_state` unpruned except optional GC for unknown participants.
   No retain-count of its own — pruning is driven by the existing PoC stage
   pruning (see Phase 7).
9. Instantiate the store in `decentralized-api/main.go`, next to where
   `artifactStore` is created, and pass it into the dispatcher and validator.

## Phase 3 — Early checkpoint capture worker

10. Add a first-third target-height helper in `decentralized-api/poc/phase.go`,
    next to `GetCurrentPocStageHeight`, computing
    `first_third_height = poc_start + floor(PocStageDuration * first_fraction)`
    for both regular PoC and CPoC (CPoC uses the confirmation event generation
    start height).
11. In `decentralized-api/internal/event_listener/new_block_dispatcher.go` (the
    per-block stage handler that already checks `IsStartOfPocStage` /
    `IsStartOfPoCValidationStage`), add a check that fires once when the block
    height reaches the first-third target for the active stage.
12. On that trigger, schedule a capture (background goroutine, mirroring the
    existing `ValidateAll` goroutine pattern) only when the guard mode is not
    `disabled`; make it idempotent against `poc_early_capture_runs` so a restart
    does not recapture.
13. In the capture path, query early commitments via the inference query client
    `AllPoCV2StoreCommitsForStage` (same client used in
    `decentralized-api/poc/validator.go`), using the cosmos client from
    `decentralized-api/cosmosclient`.
14. Store each returned commitment as a row in `poc_early_checkpoints` and write a
    `poc_early_capture_runs` row with status `completed` and the captured count.
15. On capture failure, retry until a deadline then mark the run skipped/failed so
    the guard later treats the stage as not captured (fail open).

## Phase 4 — Validation integration

16. In `decentralized-api/poc/validator.go` `ValidateAll`, after loading final
    commits and the `PoCValidationSnapshot`, load early checkpoints for the stage
    from the store; if none exist for a `(stage, model_id)`, skip the guard for
    that stage.
17. Build the per-`(stage, model_id)` early-share distribution: for each
    participant with a final commit, compute `early_count / final_count`, treating
    a participant absent from the early snapshot (but present in final) as
    `early_count = 0`.
18. Add a weighted-median helper (new function in `decentralized-api/poc/`, near
    the sampling helpers) that takes `(early_share, weight)` pairs and returns the
    value where cumulative weight crosses half of the total.
19. Compute `p50_share` using that helper, weighting each participant by its
    per-model voting power from `snapshot.ModelVotingPowers[model_id]` (already
    fetched in `ValidateAll` for sampling); if that model is absent from the
    snapshot or its total voting power is `0`, skip the guard for that
    `(stage, model_id)` — no unweighted fallback.
20. Compute `threshold_share = p50_share * threshold_ratio` per
    `(stage, model_id)`.

## Phase 5 — Per-participant guard checks

21. In `decentralized-api/poc/validator.go` `validateParticipant`, before the
    MLNode statistical validation, run the guard for participants that have early
    checkpoint data.
22. Threshold check: compare the participant's `early_share` against
    `threshold_share`.
23. Miss-streak check: read/update `poc_early_guard_state` for
    `(participant, model_id)` — track `consecutive_misses`, and
    apply the one-miss grace rule (only vote no on the relevant repeated miss).
24. Shared-leaf injection: when `require_prefix_proof` is set, ensure the
    validation sample includes one deterministic `shared_leaf_index < early_count`
    by adjusting the sampling step in `validateParticipant`.
25. Prefix proof comparison: request the shared leaf against
    `(early_root_hash, early_count)` using `decentralized-api/poc/proof_client.go`
    `FetchAndVerifyProofs`, and compare `leaf_index`/`nonce`/`vector` with the
    final-set proof for the same leaf.
26. Decide the outcome: prefix-proof mismatch (or `early_root` cannot prove the
    shared leaf) is an immediate vote-no, not subject to the grace rule; low early
    share follows the miss-streak rule; `early_count > final_count` follows the
    miss-streak rule and marks the prefix proof unavailable.

## Phase 6 — Modes and vote submission

27. Gate behavior by mode: `observe` runs all checks and logs pass/fail but never
    changes the vote; `enforce` returns the failing result.
28. On an enforced vote-no, return permanent validation failure and submit
    `ValidatedWeight = -1` via the existing submission path in
    `decentralized-api/poc/validator.go`.
29. Ensure every "skip guard" branch (Phase 3/4 fail-open conditions) falls
    through to normal PoC validation unchanged.

## Phase 7 — Retention wiring

30. Drive early-guard pruning off the existing artifact-store stage pruning so
    there is no separate retention setting: when the artifact store prunes a
    stage in `decentralized-api/poc/artifacts/managed_store.go` (`PruneStore` /
    cleanup loop), also call the Phase 2 `DeleteStage(stage_height)` for the same
    stage. This guarantees early-guard rows live exactly as long as that stage's
    artifacts.

## Phase 8 — Tests

31. Unit-test the first-third height helper in `decentralized-api/poc/phase.go`
    for regular PoC and CPoC.
32. Unit-test the weighted-median helper (ties, zero weights, single dominant
    weight, absent participants as zero).
33. Unit-test the capture worker idempotency and fail-open paths against a stubbed
    query client (mirror `decentralized-api/poc/commit_worker_test.go`).
34. Unit-test the guard decision matrix in `validator.go` (pass, low-share with
    grace, prefix mismatch immediate no, `early_count > final_count`, missing
    snapshot voting power → skip) extending the existing validator tests.
35. Add a persistence test for the store package (schema init, upsert/get, prune).

## Phase 9 — Rollout

36. Ship with the guard `disabled` by default.
37. Enable `observe` on testnet/mainnet and review logged pass/fail decisions
    against known-good participants before switching to `enforce`.
