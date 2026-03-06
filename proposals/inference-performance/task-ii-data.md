# Inference Performance Task II: Collected Implementation Data

This file contains the discovery/inventory data needed for implementing Task II.  
The implementation sequence itself is in `proposals/inference-performance/task-ii-todo.md`.

## 1) Task II Scope (Frozen)

In scope:

1. Keep a single inference persistence write in Start/Finish completion flows.
2. Remove heavy developer stats writes from the on-chain hot path.
3. Extend `inference_finished` event so API nodes can compute/store stats off-chain.
4. Migrate dashboard-facing stats reads to off-chain storage where needed.

Out of scope:

1. EndBlocker batch migration from Task III.
2. `SetParticipant`/`ComputeStatus` optimization from Task IV.
3. Signature validation optimization from Task I / issue #608.

## 2) Current On-Chain Stats Write Path

### Entry point

- `inference-chain/x/inference/keeper/inference.go`
  - `SetInference(...)` currently calls `SetDeveloperStats(...)`.

### Heavy stats writes inside `SetDeveloperStats`

- `inference-chain/x/inference/keeper/developer_stats_aggregation.go`
  - `setOrUpdateInferenceStatByTime(...)`
  - `setInferenceStatsByModel(...)`
  - `setOrUpdateInferenceStatsByEpoch(...)`

### Underlying KV prefixes touched

- `inference-chain/x/inference/keeper/developer_stats_store.go`
  - `stats/developers/epoch`
  - `stats/developers/time`
  - `stats/developers/inference`
  - `stats/model/inference`

## 3) Current Inference Write Flow (Where Double Write Happens)

### Start flow

- `inference-chain/x/inference/keeper/msg_server_start_inference.go`
  - Calls `SetInference(...)` once after payments.
  - If inference is completed in same tx, calls `handleInferenceCompleted(...)`.

### Finish flow

- `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
  - Calls `SetInference(...)` once after payments.
  - If completion condition hits, calls `handleInferenceCompleted(...)`.

### Completion handler

- `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
  - `handleInferenceCompleted(...)` currently does:
    - emits `inference_finished` event
    - participant update (`SetParticipant`)
    - epoch group reads (`GetEffectiveEpoch`, `GetEpochGroupForEpoch`, subgroup lookup)
    - validation details write (`SetInferenceValidationDetails`)
    - second inference write (`SetInference`)  <-- duplicate write target for Task II
    - epoch group write (`SetEpochGroupData`)

## 4) Current `inference_finished` Event Usage

### Emitted on chain

- `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
  - Event type: `inference_finished`
  - Current guaranteed attribute: `inference_id`

### Consumed in API node

- `decentralized-api/internal/event_listener/event_listener.go`
  - `InferenceFinishedEventHandler.CanHandle(...)` checks `inference_finished.inference_id`.
  - `InferenceFinishedEventHandler.Handle(...)` passes IDs to:
    - `validator.SampleInferenceToValidate(...)`

Compatibility constraint:

- Keep `inference_finished.inference_id` unchanged while adding new attributes.

## 5) Current Stats Read Consumers (API/Dashboard-Relevant)

### Confirmed direct usage in API server

- `decentralized-api/internal/server/public/get_pricing_handler.go`
  - `getModelMetrics(...)` calls chain query:
    - `InferencesAndTokensStatsByModels(TimeFrom, TimeTo)`
  - Uses returned values for model utilization calculations.

### Chain query handlers currently serving stats

- `inference-chain/x/inference/keeper/query_developer_stats_aggregation.go`
  - `StatsByTimePeriodByDeveloper`
  - `StatsByDeveloperAndEpochsBackwards`
  - `InferencesAndTokensStatsByEpochsBackwards`
  - `InferencesAndTokensStatsByTimePeriod`
  - `InferencesAndTokensStatsByModels`
  - `DebugStatsDeveloperStats`

## 6) Event Payload Contract Needed for Off-Chain Developer Stats

Add to `inference_finished` event (while preserving `inference_id`).
Only include fields needed to reproduce current developer stats behavior:

Required:

1. `inference_id`
2. `requested_by` (developer key)
3. `model`
4. `status`
5. `epoch_id`
6. `prompt_token_count`
7. `completion_token_count`
8. `actual_cost_in_coins`
9. `start_block_timestamp`
10. `end_block_timestamp`

## 7) Off-Chain Storage Decision for Task II

Minimum required for current developer-stats/API behavior:

1. Persist per-inference records (source of truth) keyed by `inference_id`.
2. Maintain aggregated stats by model + time window (for utilization in pricing).
3. Keep idempotent per-inference ingest key (`inference_id`) to prevent duplicate counting.

### Endpoints to implement (API node)

If we migrate stats reads from chain to API node and keep per-inference support, implement these endpoints:

1. `GET /v1/stats/developers/:developer/inferences?time_from=&time_to=`
   - Purpose: per-inference list for one developer in time range.
   - Replaces chain query semantics of `StatsByTimePeriodByDeveloper`.
2. `GET /v1/stats/developers/:developer/summary/epochs?epochs_n=`
   - Purpose: aggregate tokens/inference_count/cost for one developer over last N epochs.
   - Replaces `StatsByDeveloperAndEpochsBackwards`.
3. `GET /v1/stats/summary/epochs?epochs_n=`
   - Purpose: aggregate tokens/inference_count/cost over last N epochs.
   - Replaces `InferencesAndTokensStatsByEpochsBackwards`.
4. `GET /v1/stats/summary/time?time_from=&time_to=`
   - Purpose: aggregate tokens/inference_count/cost over time range.
   - Replaces `InferencesAndTokensStatsByTimePeriod`.
5. `GET /v1/stats/models?time_from=&time_to=`
   - Purpose: aggregate per-model tokens and inference counts in time range.
   - Replaces `InferencesAndTokensStatsByModels`.
6. `GET /v1/stats/debug/developers`
   - Purpose: debug dump of by-time and by-epoch developer stats.
   - Replaces `DebugStatsDeveloperStats`.

### Priority

1. Required first: endpoint #5 (used by pricing utilization logic).
2. Next: endpoint #1 (per-inference developer stats, because we explicitly keep per-inference).
3. Then #2/#3/#4 for aggregate parity.
4. Last: #6 debug endpoint (can be admin-only if preferred).

## 8) Implementation Guardrails

1. Keep validation sampling behavior unchanged (`inference_finished.inference_id`).
2. Do not include Task III EndBlocker migration in this implementation.
3. Do not remove stats query endpoints until API read path migration is complete.
4. Use a temporary feature flag fallback during rollout to reduce risk.
