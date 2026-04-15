# Preserved Nodes

This note separates current implementation from the proposed change. It focuses on protocol behavior and only keeps API details that affect the design.

## Current implementation

### Meaning of `timeslot_allocation`

`MLNodeInfo.timeslot_allocation` is defined in `inference-chain/proto/inference/inference/epoch_group_data.proto`.

- `PRE_POC_SLOT = 0`
- `POC_SLOT = 1`

Today `POC_SLOT=true` means the node is preserved for the whole epoch. In practice that means:

- it stays on inference during the epoch-start PoC
- it stays on inference during every confirmation PoC event in that epoch
- it contributes no fresh PoC proof during that epoch

`POC_SLOT=false` means the node is in the PoC-participating side for that epoch. It can be used in the epoch-start PoC and in any confirmation PoC event that happens in the inference phase.

One important precision: the chain uses `POC_SLOT` for weight accounting and epoch data, but the "stay on inference" behavior is realized mainly by the decentralized API broker. The chain does not use `POC_SLOT` as a hard reject rule for PoC submissions.

### Epoch timeline today

For a normal epoch, the flow is:

```text
[ epoch-start PoC x1 ] -> [ inference phase with 0..N confirmation PoC events ]
```

The confirmation count is not fixed. It can be zero because of random triggering, configuration, or because confirmation PoC is not active for early epochs.

With the current static epoch flag:

- `POC_SLOT=true` skips the epoch-start PoC and all confirmation events in that epoch
- `POC_SLOT=false` can participate in the epoch-start PoC and each confirmation event

### Weight handling today

Current weight handling uses the same split:

- preserved weight is the `POC_SLOT=true` slice
- confirmation weight covers the `POC_SLOT=false` slice
- effective reward weight is recomputed as preserved weight plus confirmation weight

Rollover also uses the same flag. `GetPreservedNodesByParticipant` and `GetPreviousEpochMLNodesWithInferenceAllocation` copy prior preserved nodes into the next epoch and reset their allocation to `[true, false]` for the new epoch.

### Allocation and stability

`POC_SLOT` is assigned in `model_assignment.go` when the next epoch is formed. The assigner limits preservation so enough weight remains available for generation, validation, and voting.

`ActiveParticipants` is written at the end of PoC validation in `module.go` and then treated as stable for that epoch. That is why the current design can carry one epoch-long preserved flag, but it is also why mid-epoch rewrites of `ActiveParticipants` are unattractive.

## Problem

The current design binds preservation to the whole epoch. That creates a predictable gap:

- a preserved node skips the only epoch-start PoC opportunity for that epoch
- the same node also skips every confirmation PoC event in that epoch
- if inference traffic is low, the hardware can go mostly unexercised between epoch boundaries

This makes hardware downgrade or partial-capacity substitution easier to plan. An operator knows in advance which boxes will avoid PoC for the entire epoch.

The asymmetry is simple:

- non-preserved nodes can be checked at epoch start and again during confirmation events
- preserved nodes wait until a later epoch boundary before they are forced back into PoC

## Proposal

### Core idea

Replace epoch-long preservation with episode-scoped preservation.

An episode is one PoC execution window:

- the epoch-start regular PoC
- one confirmation PoC event during the inference phase

At the start of each episode, the chain materializes one preserved snapshot for that episode only. The broker reads that snapshot for the active episode. The next episode gets a new snapshot.

This keeps the late-binding property we want: an operator cannot know far in advance whether a given node will be preserved for the next PoC window.

### Anchor point

The preserved snapshot should be fixed at the same boundary already used for generation start tracking:

- regular PoC: the boundary keyed by `upcomingEpoch.PocStartBlockHeight` in `module.go`
- confirmation PoC: the `GRACE_PERIOD -> GENERATION` transition keyed by `event.TriggerHeight` in `confirmation_poc.go`

That gives the design a deterministic episode boundary that already exists in protocol state.

### Candidate pool

The candidate pool for an episode draw should come from the current authoritative epoch-group state, not from raw `ActiveParticipants` alone.

For multi-model correctness, the primary source should be the current model subgroup `EpochGroupData.ValidationWeights` and `MlNodes` for the model that owns the episode. That matches how validation-time model state is already derived today.

The root epoch group still matters for participant consensus weights and network totals, but model-local preserved sampling should use subgroup state.

The pool should be derived from:

- current root epoch group for consensus-weight context
- current model subgroup `ValidationWeights` / `MlNodes`
- existing protocol exclusions

It should use the same live-member filtering principle already used by validation snapshots. In particular, members excluded from the effective validation snapshot should not be sampled for preserved status.

It should not read from future participant sets, draft merge state, any successor epoch data, or live `HardwareNodes` changes that happened after the current epoch state was formed.

### State model

Keep `ActiveParticipants` stable for the whole epoch.

Under the new design:

- `timeslot_allocation[1]` in `ActiveParticipants` stays `false`
- that field becomes deprecated for preserved-node scheduling semantics
- preserved scheduling moves to a dedicated episode snapshot

Use one unified snapshot schema for both regular PoC and confirmation PoC. The difference between the two cases should be the episode anchor key, not two different storage models.

The cleanest shape is a dedicated collection that reuses the existing `PoCValidationSnapshot` pattern:

- one keeper collection keyed by the episode anchor
- regular PoC uses `upcomingEpoch.PocStartBlockHeight`
- confirmation PoC uses `event.TriggerHeight`
- query helpers mirror the existing snapshot getter/query pattern

This should be a sibling of `PoCValidationSnapshot`, not an overload of it. `PoCValidationSnapshot` stores validation-time voting powers and app hash. The preserved snapshot should store preserved-node decisions.

Because preserved status is read often, the access path must stay cheap. The authoritative record can live in its own collection, but the active snapshot should also be surfaced through existing epoch-related query paths or caches so hot readers do not need an extra remote query on every request.

Existing rollover helpers that currently infer preserved carry from `POC_SLOT=true` must be updated to stop using `timeslot_allocation[1]` for scheduling semantics. Under the new design, episode snapshots do not imply an epoch-to-epoch carry bit in `ActiveParticipants`.

### Minimal implementation shape

The intended implementation should stay simple and mostly local:

- reuse the current preserved-node allocation logic from `AllocateMLNodesForPoC` instead of introducing a new sampling rule
- add one episode-scoped preserved snapshot keyed by epoch and episode anchor
- write that snapshot at the same transition that already records generation start
- keep `ActiveParticipants` stable and keep `timeslot_allocation[1]` deprecated for this purpose
- make the broker read the current episode snapshot instead of static epoch-long `TimeslotAllocation[1]`
- update other hot-path readers that currently depend on `TimeslotAllocation[1]`, especially `GetRandomExecutor` and broker-side PoC availability checks
- keep existing command order and most existing PoC phase logic unchanged

This avoids a mid-epoch rewrite of `ActiveParticipants` and keeps the change concentrated in state generation, snapshot reads, and broker filtering.

### Broker behavior

The broker flow should stay structurally the same:

1. `ShouldBeOperational`
2. preserved for the active episode
3. otherwise PoC

The only change is step 2. Instead of reading static epoch-long `TimeslotAllocation[1]`, the broker reads the current episode snapshot.

Each confirmation PoC event gets a fresh preserved sample. There is no carry-over requirement from one confirmation event to the next.

### Weight behavior

For each episode:

- the preserved vs participating split is read from the snapshot fixed at that episode boundary
- weight accounting for that episode uses that snapshot
- the next episode is allowed to produce a different split

The selection algorithm should not be a new design space. It should reuse the current preserved-node allocation logic and constraints from `model_assignment.go`, but run at episode start against the episode candidate pool.

### Determinism requirement

All full nodes must derive the same preserved set from:

- the episode anchor
- chain-visible entropy available at that anchor
- current authoritative epoch state
- deterministic ordering rules

The design does not require a specific RNG yet, but it does require deterministic replay.

## Admin disable and hardware removal

Admin disable is an important design constraint, but it should stay split into local and chain-visible behavior.

### Local disable

Local admin disable in the decentralized API remains immediate and local:

- `ShouldBeOperational` still runs before the preserved check
- a locally disabled node stays out of PoC work immediately
- the same predicate also blocks inference routing to that node

This behavior should stay unchanged. The proposal does not require changing current `ShouldBeOperational` semantics, and local admin state is not an input to the chain-side preserved snapshot.

### Chain-visible removal

The chain does not read local admin state. The chain-visible way to withdraw hardware remains `MsgSubmitHardwareDiff`.

That chain-visible change should be applied when the next `ActiveParticipants` set is built, not during each episode draw.

In other words:

- episode sampling uses the current authoritative epoch-group state for the current episode
- it does not re-check live `HardwareNodes` mid-epoch
- hardware removal affects the next `setModelsForParticipants` / allocation pass and therefore the next `ActiveParticipants` generation

This matches the current architecture: `ActiveParticipants` for the current epoch stays stable even if `HardwareNodes` changes mid-epoch.

### Operational consequence

There is an intentional timing gap:

- local disable can stop work immediately on the host
- chain-visible removal only changes protocol participation when the next `ActiveParticipants` set is generated

That means a locally disabled node can still exist in current epoch protocol state until the next generation cycle. The proposal should state that clearly.

If an operator wants protocol obligations to change, local disable is not enough. They must also submit the chain-visible hardware removal before the next active-participant generation.

## Open questions

The proposal is still open on a few points:

1. Exact preserved snapshot payload: per-model node bitmap only, or also participant-level summary fields.
2. How to surface the active snapshot efficiently to frequent readers such as broker state refresh and query paths like `query_get_random_executor.go`.
3. Migration order for old readers that still expect `timeslot_allocation[1]`, especially broker `ShouldContinueInference`, `GetRandomExecutor`, and any reward or rollover helpers still keyed off static `POC_SLOT`.

## Testermint coverage to update

The current `testermint` suite already contains several tests that read `timeslotAllocation` directly or assume epoch-wide preserved membership. Those tests should be updated together with the preserved-snapshot rollout.

### Direct readers that must change

- `testermint/src/test/kotlin/SchedulingTests.kt`
  - Today this test finds the preserved node by checking `epochMlNodes[*].timeslotAllocation[1] == true` and then expects that node to stay on inference during PoC.
  - Under the new design, it should stop reading epoch-long `timeslotAllocation[1]` as the scheduling source of truth.
  - Update approach:
    - query the active preserved snapshot for the regular PoC episode
    - identify the preserved node from that episode snapshot
    - keep the status assertions (`INFERENCE` vs `POC`) against broker state

- `testermint/src/test/kotlin/ConfirmationPoCMultiNodeTests.kt`
  - These tests currently read each node's `POC_SLOT` from `epochMlNodes`, count `POC_SLOT=true` vs `POC_SLOT=false`, and derive expected reward math from that static split.
  - Under the new design, confirmation preserved membership is per event, not per epoch.
  - Update approach:
    - query the preserved snapshot for the actual confirmation event keyed by `event.TriggerHeight`
    - compute preserved vs participating node counts from that event snapshot
    - keep the reward assertions, but derive expected weights from the event-local snapshot instead of epoch-long `POC_SLOT`
    - if the test wants deterministic coverage, pin the preserved sampler inputs or compare against the chain-produced snapshot rather than reproducing the selection inline

- `testermint/src/test/kotlin/NodeDisableInferenceTests.kt`
  - This test currently uses `allocation.timeslotAllocation.any { it }` as a loose proxy for inference eligibility.
  - That check is not a good fit once `timeslotAllocation[1]` is deprecated for scheduling.
  - Update approach:
    - remove the timeslot-based proxy assertion
    - if scheduling evidence is still needed, assert against broker behavior or the active preserved snapshot for the current episode
    - keep the main intent of the test focused on disable timing and reward-claim behavior

### Tests that should be adjusted for clarity

- `testermint/src/test/kotlin/MultiModelPoCTests.kt`
  - This test only asserts that `timeslotAllocation` has size 2 and that index 0 is `PRE_POC_SLOT=true`.
  - It does not currently depend on preserved scheduling.
  - Update approach:
    - keep this only as a compatibility check if `timeslotAllocation` remains on the wire during migration
    - do not extend it to interpret `timeslotAllocation[1]` as preserved scheduling
    - if a multi-model scheduling test is added later, base it on subgroup epoch-group data plus the preserved snapshot, not raw `ActiveParticipants`

### Related tests that may need expectation updates

- `testermint/src/test/kotlin/ConfirmationPoCPassTests.kt`
- `testermint/src/test/kotlin/ConfirmationPoCFailTests.kt`
  - These tests do not read `timeslotAllocation` directly, but they assume a specific set of nodes participates in confirmation and then check rewards or slashing outcomes.
  - Under event-scoped preserved sampling, those expectations remain valid only if the preserved snapshot for the event is known.
  - Update approach:
    - keep them behavior-focused
    - when necessary, query the event snapshot and base expected confirmation participants on it
    - avoid hidden assumptions that all nodes participate just because the old epoch-long `POC_SLOT` happened to allow it

- `testermint/src/test/kotlin/NodeAdminStateTests.kt`
  - These tests are mainly about local admin-disable timing.
  - They do not need to read preserved scheduling data directly, but they remain important because local disable still overrides preserved status in broker logic.
  - Update approach:
    - keep them behavior-focused
    - if any preserved check is added, make it explicit that local admin state is broker-local and is not an input to the chain-side preserved snapshot

- `testermint/src/test/kotlin/CollateralTests.kt`
  - This file contains preserved-node terminology in comments around expiry and downtime handling.
  - Update approach:
    - rename the wording from epoch-long preserved-node eligibility to episode-preserved eligibility where relevant

### Migration note for test helpers

The current `testermint` helper logic often reads node epoch data from broker APIs. That is still useful for model assignment, but it is no longer sufficient for preserved scheduling after this change.

At minimum, test helpers should gain one way to read the active preserved snapshot:

- regular PoC snapshot by `upcomingEpoch.PocStartBlockHeight`
- confirmation PoC snapshot by `activeConfirmationPoCEvent.triggerHeight`

This is especially important because production code also has hot-path readers that still depend on static `POC_SLOT`, such as:

- `inference-chain/x/inference/keeper/query_get_random_executor.go`
- `decentralized-api/broker/broker.go`
- `decentralized-api/broker/state_commands.go`

## Source map

The main files for this topic are:

- `inference-chain/proto/inference/inference/epoch_group_data.proto`
- `inference-chain/x/inference/module/model_assignment.go`
- `inference-chain/x/inference/module/chainvalidation.go`
- `inference-chain/x/inference/module/module.go`
- `inference-chain/x/inference/module/confirmation_poc.go`
- `inference-chain/x/inference/keeper/bitcoin_rewards.go`
- `inference-chain/x/inference/keeper/query_get_random_executor.go`
- `decentralized-api/broker/broker.go`
- `decentralized-api/broker/state_commands.go`
- `decentralized-api/poc/validator.go`
- `proposals/random-poc/README.md`
