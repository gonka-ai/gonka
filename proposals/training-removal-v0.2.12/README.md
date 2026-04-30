# Training Removal for v0.2.12

## Overview

The Gonka project is no longer pursuing training as part of the product direction. The training subsystem was previously developed across both `inference-chain` and `decentralized-api`, including chain messages and queries, off-chain coordination endpoints, broker execution flows, and ML-node integrations.

This proposal records the decision to remove that feature set from the active product surface.

## Decision

The project will remove all training-related feature surfaces from:

- `inference-chain`
- `decentralized-api`

This is a hard removal. Training-specific runtime behavior, public endpoints, chain RPCs, proto definitions, generated artifacts, tests, and supporting logic will be deleted rather than deprecated.

## Compatibility Posture

Backward compatibility for training clients is not a goal of this change. Old training routes, RPCs, and generated types are expected to disappear.

The only compatibility exception is internal chain storage cleanup. Some training-related keeper fields, prefixes, and key helpers may remain temporarily where needed to support deterministic cleanup during the `v0.2.12` upgrade.

## Upgrade Exception

Although little or no real training data is expected to exist on-chain, the `v0.2.12` upgrade should explicitly clear any residual training state.

That upgrade will:

- clear the training allowlist collections
- remove all raw training task and training sync prefix-store data

No migration or preservation path is planned for training state.

## Documents

- `removal-design.md` describes the scope of removal and the upgrade cleanup model
- `implementation-plan.md` provides the ordered execution plan for carrying out the removal
