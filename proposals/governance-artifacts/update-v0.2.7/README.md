# Upgrade Proposal: v0.2.7

This document outlines the proposed security fixes for on-chain software upgrade v0.2.7. The `Changes` section details the vulnerabilities addressed.

## Upgrade Plan

This PR updates the code for the `inference-chain` and `decentralized-api` services with security fixes.

The binary versions will be updated via an on-chain upgrade proposal. For more information on the upgrade process, refer to [`/docs/upgrades.md`](https://github.com/gonka-ai/gonka/blob/upgrade-v0.2.7/docs/upgrades.md).

## Proposed Process

1. Active hosts review this proposal on GitHub.
2. Once the PR is approved by a majority, a `v0.2.7` release will be created from this branch, and an on-chain upgrade proposal for this version will be submitted.
3. If the on-chain proposal is approved, this PR will be merged immediately after the upgrade is executed on-chain.

## Testing

All fixes have been tested to ensure they do not introduce regressions and properly address the security vulnerabilities.

## Changes

---

### SSRF and Request-Hang DoS via Participant-Controlled InferenceUrl [Security Bounty]

Commit: [85e6af1](https://github.com/gonka-ai/gonka/commit/85e6af195e9ae4150e83809ac0ee8dfebde3871c)

Adds SSRF protection to participant URL validation, rejecting private/internal IP ranges (localhost, RFC1918, link-local, AWS metadata endpoint). Also adds 30-second HTTP client timeout to prevent request-hang DoS from slow executor URLs.

Found by: Ouicate

---

### PoC Validation Overwrite Enabling Vote Flipping [Security Bounty]

Commit: [897ef58](https://github.com/gonka-ai/gonka/commit/897ef58dd63057daf3195e0cc107334916003903)

Prevents vote flipping by rejecting duplicate PoC validations. Adds `HasPoCValidation()` check before storing to enforce first-write-wins semantics, preventing validators from changing their vote after seeing others' votes.

Found by: Ouicate

---

### MsgSubmitPocBatch Size/Count Bounds [Security Bounty]

Commit: [5f29222](https://github.com/gonka-ai/gonka/commit/5f2922295e2a2590264594cadbd716956ee5bce4)

Adds maximum size limits to prevent state bloat and block-space DoS from arbitrarily large PoC batches: `MaxPocBatchNonces=10000`, `MaxPocBatchIdLength=128`, `MaxPocNodeIdLength=128`.

Found by: Ouicate

---

### Ineffective PoC Exclusion for Inference-Serving Nodes [Security Bounty]

Commit: [5fc4db4](https://github.com/gonka-ai/gonka/commit/5fc4db4f5de6f898c67b5f84d293aa3b2af4b1b2)

Fixes PoC exclusion filter that was always returning empty set. Rewrites `getInferenceServingNodeIds()` to use `GetActiveParticipants()` which properly stores MlNodes data, instead of parent epoch group which returns nil for MlNodes.

Found by: Ouicate

---

### Epoch-Based Authorization Bypass and Signature Replay Attack [Security Bounty]

Commit: [8853af8](https://github.com/gonka-ai/gonka/commit/8853af800a88c170d06f560e8a1a28de9c45ea61)

Fixes two related vulnerabilities: (1) Authorization bypass where attackers could provide an epoch header where they ARE active to access payloads from a different epoch - now queries inference's actual epoch first. (2) Signature replay attack - signatures now include epochId to prevent replay with different epoch headers.

Found by: Ouicate

---
