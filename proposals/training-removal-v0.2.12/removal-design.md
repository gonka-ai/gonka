# Training Removal Design

## Overview

This document defines the scope and implementation shape of the training removal effort for `v0.2.12`.

The intent is to remove training as a supported feature everywhere it appears in the active product surface, while still cleaning up any residual training state during the chain upgrade.

## Removal Scope

### Inference Chain

Training-related feature surfaces will be removed from the chain module, including:

- training Msg RPCs in `proto/inference/inference/tx.proto`
- training Query RPCs in `proto/inference/inference/query.proto`
- training-specific proto messages and enums
- training-only gRPC service definitions used for ML-node coordination
- training msg server handlers
- training query handlers
- training runtime/sync logic
- training-related type wrappers, validation helpers, codec registration, signer helpers, and AutoCLI entries
- training-related simulation operations
- training-focused unit tests and fixtures
- training-generated protobuf, gateway, client, and OpenAPI artifacts

### Decentralized API

Training-related functionality will be removed from the off-chain API, including:

- startup wiring in `main.go`
- public `/training/...` endpoints
- admin debug endpoints used for training
- training executor and assigner logic
- event listener integration for training events
- broker commands and node state used only for training
- ML-node client methods for training execution and status
- training-related mocks, DTOs, tests, and imports

## Retained Internals

Some training-related internal storage constructs may remain in place where needed for upgrade-time cleanup. This includes existing keeper/schema definitions such as:

- `TrainingExecAllowListSet`
- `TrainingStartAllowListSet`
- training-related key helpers and prefix constants
- keeper-side training storage accessors or prefix knowledge that is needed to delete state deterministically

These retained constructs are not intended to preserve user-facing training functionality. They exist only to support safe state deletion during the upgrade.

## Upgrade Cleanup Model

The `v0.2.12` upgrade handler should explicitly remove all on-chain training state.

### Collection-Backed State

The upgrade should clear the training allowlist collections directly:

- `TrainingExecAllowListSet.Clear(ctx, nil)`
- `TrainingStartAllowListSet.Clear(ctx, nil)`

### Raw Prefix-Store State

The upgrade should also iterate and delete all raw training prefixes that are not represented as clearable collections. The expected targets include:

- `TrainingTask/value/`
- `TrainingTask/sequence/value/`
- `TrainingTask/queued/value/`
- `TrainingTask/inProgress/value/`
- `TrainingTask/sync/`

The `TrainingTask/sync/` subtree covers training KV records, heartbeat activity, and barrier state.

## Non-Goals

This removal does not attempt to:

- preserve training query access for historical data
- migrate training data into a new format
- keep deprecated stubs for old training clients
- maintain training-specific runtime behavior behind a feature flag

The design assumes hard deletion of the feature surface and full cleanup of any residual training state.
