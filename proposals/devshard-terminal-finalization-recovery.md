# Recover devshard finalization at the nonce limit

Status: draft proposal; implementation belongs in a follow-up after this
proposal is agreed. This work is split from
[authenticated v7 checkpoints, PR #2271](https://github.com/gonka-ai/gonka/pull/2271)
and [warm-key rotation, PR #2042](https://github.com/gonka-ai/gonka/pull/2042).

## Problem

A gateway can persist the final allowed diff and crash before collecting enough
host state signatures. When it resumes more than two minutes later, that diff's
checkpoint is stale and hosts withhold new signatures. At the escrow's absolute
`max_nonce`, the gateway cannot append a fresh-checkpoint diff. The ordinary
refresh-and-retry path therefore cannot recover settlement.

The current behavior is fail-closed. The existing PRs keep it and document this
limitation. This proposal does not remove their freshness checks or change their
implementation scope.

## Intended result

After a crash at any point in finalization, the gateway should be able to resume
from its persisted journal, obtain the required finalization evidence and submit
one settlement. It must preserve the agreed final root, accounting, nonce limit
and participant identities. Recovery must also work after a host restart and
with an HA replica taking over.

## Protocol work to specify separately

The recovery operation needs its own explicit protocol treatment rather than
an unconditional terminal-state exemption from checkpoint freshness. Its exact
wire format and chain verification rules remain open in this draft.

The agreed design must specify:

- How a recovery request names the escrow, final nonce, final root and protocol
  version, and how it carries a fresh authenticated mainnet checkpoint without
  treating it as another inference diff past `max_nonce`.
- What prior acceptance evidence is required for the frozen final state, and
  how ordinary state signatures differ from recovery/finalization evidence.
- Which participants can attest, which authorization height applies, and how
  rotation or expiration between original acceptance and recovery is handled.
- What the chain verifies when accepting recovered settlement. An off-chain
  exception alone is insufficient if the chain still rejects the result.
- Which records are durable before acknowledging each step, how retries are
  idempotent, and how a gateway/host reconstructs incomplete work from storage.
- How protocol negotiation and rollout keep older escrows on their original
  rules, including whether a chain upgrade is needed.

This proposal must be completed and agreed before implementing these protocol
changes. It does not claim that the existing Strong dispute/escalation protocol
already provides this recovery operation.

## Acceptance checks for the implementation

1. Persist the last allowed diff, crash before collecting any signatures, wait
   beyond the freshness window, restart and recover settlement.
2. Repeat after collecting a partial quorum and after collecting a full quorum
   but before persisting/submitting the settlement transaction.
3. Exercise gateway restart, host restart, HA takeover and lost replies at each
   durable transition. Repeated requests produce the same final result.
4. Keep the root, host costs, user balance and nonce cap unchanged throughout
   recovery. A conflicting root or mismatched escrow/nonce/version is rejected.
5. Cover a warm-key rotation, grant expiration and grant revocation between
   original acceptance and recovery using the explicitly agreed authority rules.
6. Retain the regression that an ordinary never-signed stale terminal state
   does not become signable merely because it is terminal.
7. Verify the complete path on a Docker stand through chain settlement and
   recovery from persisted PostgreSQL state, not only an in-memory host test.

## Scope and release notes

No implementation, protobuf changes, new signature domain, chain upgrade or
freshness exception is included in this proposal PR. PR #2271 and #2042 retain
the documented limitation until the separately reviewed implementation ships.
