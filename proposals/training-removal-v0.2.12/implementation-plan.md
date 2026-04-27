# Training Removal Implementation Plan

This document is the execution plan for removing training from the active Gonka codebase while preserving enough internal storage knowledge to clear residual chain state during the `v0.2.12` upgrade.

**Important notes:**

- This is a hard-removal project, not a deprecation project.
- Do not edit generated protobuf artifacts directly.
- Any retained training keeper/schema constructs should exist only to support upgrade cleanup.
- The final result should remove training from both `inference-chain` and `decentralized-api`.

## Step 1: Document the Training Surfaces to Delete

**Tasks:**

1. Confirm the full training footprint in both repos.
2. Enumerate training surfaces in:
   - chain protos and generated outputs
   - chain msg/query handlers
   - permissions and allowlists
   - decentralized-api startup, routes, broker, ML-node, and event processing
3. Use that inventory as the removal checklist so no training-facing surface is left behind.

**Validation:**

1. Review the inventory against `rg` results before code deletion begins.

## Step 2: Remove Decentralized API Runtime and Endpoints

**Tasks:**

1. Delete the training package under `decentralized-api/training/`.
2. Remove training startup wiring from `decentralized-api/main.go`.
3. Remove public training endpoints and the admin debug endpoint.
4. Remove event listener and executor/assigner integration for training.
5. Remove training-specific broker commands, DTOs, node-state payloads, and ML-node client methods.

**Validation:**

1. Run focused decentralized-api package tests after the removal.
2. Confirm the server no longer registers training HTTP or gRPC handlers.

## Step 3: Remove Chain Msg, Query, and Proto Surfaces

**Tasks:**

1. Remove training RPCs and messages from `proto/inference/inference/tx.proto`.
2. Remove training RPCs and messages from `proto/inference/inference/query.proto`.
3. Remove training-only proto files and the training-specific network-node service definitions.
4. Delete chain msg server handlers, query handlers, runtime helpers, sync logic, AutoCLI entries, simulation ops, and tests that support training behavior.
5. Remove training message registrations from permissions, codec, signer, and interface wiring.

**Validation:**

1. Ensure no non-upgrade runtime path still references training APIs.
2. Confirm no public chain Msg or Query surface remains for training.

## Step 4: Regenerate Protobuf, Gateway, and OpenAPI Artifacts

**Tasks:**

1. Regenerate protobuf-derived artifacts from the updated proto definitions.
2. Refresh generated outputs in:
   - `api/inference/inference/*`
   - `x/inference/types/*.pb.go`
   - `x/inference/types/query.pb.gw.go`
   - `docs/static/openapi.yml`
3. Remove generated training artifacts that are no longer referenced.

**Validation:**

1. Confirm generated code no longer exposes training messages, queries, or services.
2. Confirm OpenAPI output no longer contains training operations.

## Step 5: Add `v0.2.12` Training-State Cleanup Helper

**Tasks:**

1. Extend `inference-chain/app/upgrades/v0_2_12/upgrades.go` with a dedicated helper for training cleanup.
2. Clear collection-backed allowlist state explicitly:
   - `TrainingExecAllowListSet.Clear(ctx, nil)`
   - `TrainingStartAllowListSet.Clear(ctx, nil)`
3. Iterate and delete all raw non-collection training state under these prefixes:
   - `TrainingTask/value/`
   - `TrainingTask/sequence/value/`
   - `TrainingTask/queued/value/`
   - `TrainingTask/inProgress/value/`
   - `TrainingTask/sync/`
4. Keep the necessary keeper fields, prefix constants, and key constructs required to perform this cleanup.

**Validation:**

1. Verify the helper is deterministic and uses explicit prefix deletion.
2. Verify that no migration or preservation path remains for training state.

## Step 6: Add or Adjust Upgrade Tests

**Tasks:**

1. Add upgrade coverage for training-state cleanup in the `v0.2.12` test suite.
2. Seed training allowlist entries and raw training prefix-store data in test setup.
3. Execute the upgrade and verify all training state has been removed.

**Validation:**

1. Assert allowlist collections are empty after the upgrade.
2. Assert all training raw prefixes are absent after the upgrade.

## Step 7: Remove Residual Tests, Mocks, and Imports

**Tasks:**

1. Delete training-specific tests in both repos.
2. Remove mock methods, generated expectations, DTOs, and helper code that only existed for training.
3. Clean up imports and compile fallout caused by deleting training code.

**Validation:**

1. Run package-level tests for the affected chain and API subsystems.
2. Ensure there are no stray compile-time references to removed training symbols.

## Step 8: Run Focused Validation Suites

**Tasks:**

1. Run focused tests for:
   - `inference-chain/x/inference/...`
   - `inference-chain/app/upgrades/v0_2_12/...`
   - decentralized-api server, broker, cosmos client, event listener, and ML-node client packages
2. Verify startup for both binaries after all deletions and regeneration steps.

**Validation:**

1. Confirm both codebases compile cleanly.
2. Confirm training does not appear in runtime registration, generated API surfaces, or OpenAPI output.

## Acceptance Criteria

- Training is removed from all active runtime, API, query, and message surfaces.
- `decentralized-api` no longer exposes or executes training behavior.
- `inference-chain` no longer exposes training Msg or Query operations.
- Generated protobuf, gateway, and OpenAPI artifacts no longer include training APIs.
- `v0.2.12` clears both training allowlist collections and all raw training prefixes.
- Remaining training-related keeper/schema constructs exist only to support upgrade cleanup and do not preserve feature functionality.
