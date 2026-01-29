# Merge Planning: gm/poc-offchain → upgrade-v0.2.8

## Overview

The `gm/poc-offchain` branch was created from `main` to implement off-chain PoC validation. Before creating a PR to `upgrade-v0.2.8`, we need to merge/rebase onto `upgrade-v0.2.8` to incorporate all changes that went into that branch.

This document catalogs all commits from `main` to `upgrade-v0.2.8` to ensure none are lost during the merge.

---

## Commits Summary (15 total)

| Commit | PR | Description | Priority |
|--------|-----|-------------|----------|
| `43bee5c4d` | #505 | Security Fixes for v0.2.7 | HIGH |
| `874a05bb4` | #551 | fix(bls): reject duplicate slot indices | HIGH |
| `7654de9c9` | #536 | perf: optimize participants endpoint | MEDIUM |
| `c83d671ff` | #559 | Burn extra pool coins, fix ValueDecimal | MEDIUM |
| `9604feacd` | #534 | security: prevent SSRF via executor redirect | HIGH |
| `326b0ae5d` | #541 | PoC validation, retry getting nodes | MEDIUM |
| `1c26217a8` | #506 | Standardize floating point math | HIGH |
| `d924c0362` | #544 | inference: defense-in-depth against int overflow | HIGH |
| `8184fe350` | #550 | Negative coin balance for settle | MEDIUM |
| `5bccbecbe` | #549 | Disable future timestamp check for EA | MEDIUM |
| `e09715188` | #540 | Remove ALL panic and Must from chain code | HIGH |
| `d5b6f44ff` | - | removed legacy emoji too | LOW |
| `143b2d201` | - | removed emojis and corrected path to sh script | LOW |
| `e287431f4` | - | Updated script snippets and MacOS Docker settings | LOW |
| `fffc7689b` | - | Upgrade boilerplate | LOW |

---

## High-Priority Changes (Security/Critical)

### 1. Security Fixes for v0.2.7 (#505) - `43bee5c4d`

**Multiple security fixes bundled together:**

- **SSRF Protection**: `ValidateURLWithSSRFProtection()` rejects private/internal IP ranges in participant URLs
  - Files: `inference-chain/x/inference/utils/signature_and_url_validation.go`
  - Files: `inference-chain/x/inference/types/message_submit_new_participant.go`
  - Files: `inference-chain/x/inference/types/message_submit_new_unfunded_participant.go`

- **PoC Validation Overwrite Fix**: Prevents vote flipping by rejecting duplicate PoC validation submissions
  - Added `HasPoCValidation()` check
  - Added `ErrPocValidationAlreadyExists` error
  - Files: `inference-chain/x/inference/keeper/msg_server_submit_poc_validation.go`
  - Files: `inference-chain/x/inference/types/errors.go`

- **PoC Batch Size Bounds**: Added maximum size constants
  - `MaxPocBatchNonces = 10000`
  - `MaxPocBatchIdLength = 128`
  - `MaxPocNodeIdLength = 128`
  - Files: `inference-chain/x/inference/types/message_submit_poc_batch.go`

- **Epoch Auth Bypass Fix**: Query inference epoch FIRST, then validate participant
  - Include epochId in signed payload
  - Files: `decentralized-api/internal/server/public/payload_handlers.go`
  - Files: `inference-chain/x/inference/calculations/signature_validate.go`

- **getInferenceServingNodeIds Fix**: Rewrote to use `GetActiveParticipants()` 
  - Files: `inference-chain/x/inference/module/chainvalidation.go`

### 2. BLS Duplicate Slot Indices (#551) - `874a05bb4`

**Problem**: Slot indices weren't deduplicated before counting coverage. Could submit `[0,0,0,...]` fifty times and hit threshold with one slot.

**Fix**: Reject submissions with repeated indices early.

**Files changed**:
- `inference-chain/x/bls/keeper/threshold_signing.go`

### 3. SSRF via Executor Redirect (#534) - `9604feacd`

**Problem**: `handleTransferRequest` used `http.DefaultClient` which follows redirects. Malicious executor could redirect to internal services.

**Fix**: Custom HTTP client with `CheckRedirect` returning `http.ErrUseLastResponse`.

**Files changed**:
- `decentralized-api/internal/server/public/post_chat_handler.go`
- `decentralized-api/internal/server/public/ssrf_test.go`

### 4. Int Overflow Defense (#544) - `d924c0362`

**Problem**: Escrow/cost math used uint64 then cast to int64 without bounds checks. Could wrap negative.

**Fix**:
- `CalculateEscrow` and `CalculateCost` use checked arithmetic (`math/bits`)
- Added `types.MaxAllowedTokens` constant
- Added hard caps for `max_tokens`, `prompt_token_count`, `completion_token_count`
- Added `allowRefund` flag to `processInferencePayments`

**Files changed**:
- `inference-chain/x/inference/calculations/inference_state.go`
- `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- `inference-chain/x/inference/keeper/msg_server_start_inference.go`
- `inference-chain/x/inference/types/message_finish_inference.go`
- `inference-chain/x/inference/types/message_start_inference.go`
- `inference-chain/x/inference/types/token_limits.go`
- `inference-chain/x/inference/types/errors.go`

### 5. Standardize Floating Point Math (#506) - `1c26217a8`

**Problem**: Floating point math can cause consensus failures due to platform differences.

**Fix**:
- Dynamic Pricing now uses `shopspring/decimal`
- Bitcoin-style rewards use table lookup for `exp(decayRate)`
- Removed unused reward calculation code with floats

**Files changed**:
- `inference-chain/x/inference/keeper/dynamic_pricing.go`
- `inference-chain/x/inference/keeper/bitcoin_rewards.go`
- `inference-chain/x/inference/types/params.go`
- `inference-chain/x/inference/types/message_validation.go`
- `inference-chain/proto/inference/inference/tx.proto`
- Plus many test files

### 6. Remove ALL panic/Must (#540) - `e09715188`

**Major refactor**: Removes all `panic()` and `Must*` calls from chain code to prevent consensus failures.

**Changes**:
- Added `.golangci.yml` with `forbidigo` linter
- Added `.github/workflows/dont_panic.yml` CI check
- 140+ files modified to return errors instead of panicking

**Key patterns**:
- `sdk.MustAccAddressFromBech32()` → `sdk.AccAddressFromBech32()` with error handling
- Explicit error returns instead of panic
- Exemptions marked with `//nolint:forbidigo` for genesis/init code

---

## Medium-Priority Changes (Functional)

### 7. Optimize Participants Endpoint (#536) - `7654de9c9`

**Problem**: `/v1/participants` made N separate gRPC calls for balances (~15s for 5000 participants).

**Fix**: New `ParticipantsWithBalances` query returns all participants with balances in one call.

**Files changed**:
- `inference-chain/proto/inference/inference/query.proto` - new query
- `inference-chain/x/inference/keeper/query_participants_with_balances.go` - new file
- `inference-chain/x/inference/types/expected_keepers.go`
- `decentralized-api/internal/server/public/get_participants_handler.go`

### 8. Burn Extra Pool Coins, ValueDecimal Fix (#559) - `c83d671ff`

**Fix**: `ValueDecimal` is `nil` not 0 if unspecified. Fixed validation for older messages.

**Files changed**:
- `inference-chain/app/upgrades/v0_2_8/upgrades.go`
- `inference-chain/x/inference/keeper/msg_server_validation.go`
- `inference-chain/x/inference/types/message_validation.go`

### 9. PoC Validation Retry (#541) - `326b0ae5d`

**Fix**: Retry getting nodes for PoC validation with configurable retries and delay.

**Files changed**:
- `decentralized-api/internal/poc/node_orchestrator.go`
- `decentralized-api/internal/poc/node_orchestrator_test.go`

### 10. Negative Coin Balance for Settle (#550) - `8184fe350`

**Fix**: Handle edge case where negative `CoinBalance` exists with reward that could cover it.

**Files changed**:
- `inference-chain/x/inference/keeper/bitcoin_rewards.go`

### 11. Disable Future Timestamp Check for EA (#549) - `5bccbecbe`

**Fix**: EA can get behind chain during high load, causing it to reject valid TA requests.

**Files changed**:
- `decentralized-api/internal/server/public/post_chat_handler.go`

---

## Low-Priority Changes (Infrastructure/Docs)

### 12. Upgrade v0.2.8 Boilerplate - `fffc7689b`

**Files changed**:
- `inference-chain/app/upgrades.go`
- `inference-chain/app/upgrades/v0_2_8/constants.go`
- `inference-chain/app/upgrades/v0_2_8/upgrades.go`

### 13-15. Documentation Cleanup

- `d5b6f44ff` - removed legacy emoji
- `143b2d201` - removed emojis and corrected path to sh script
- `e287431f4` - Updated script snippets and MacOS Tahoe 26.1 Docker settings
- Files: `testermint/README.md` and similar docs

---

## Files With Potential Conflicts

These 18 files are modified in both `gm/poc-offchain` and `upgrade-v0.2.8`:

### PoC-Related Files (HIGH attention needed)
- `inference-chain/x/inference/keeper/msg_server_submit_poc_batch.go`
- `inference-chain/x/inference/keeper/msg_server_submit_poc_validation.go`
- `inference-chain/x/inference/module/chainvalidation.go`
- `inference-chain/x/inference/module/confirmation_poc.go`
- `decentralized-api/internal/poc/node_orchestrator.go`

### Proto Files (regenerate after merge)
- `inference-chain/proto/inference/inference/query.proto`
- `inference-chain/proto/inference/inference/tx.proto`
- `inference-chain/x/inference/types/query.pb.go`
- `inference-chain/x/inference/types/query.pb.gw.go`
- `inference-chain/x/inference/types/tx.pb.go`
- `inference-chain/api/inference/inference/tx.pulsar.go`

### Core Files
- `inference-chain/x/inference/keeper/keeper.go`
- `inference-chain/x/inference/types/params.go`
- `inference-chain/x/inference/types/errors.go`
- `inference-chain/x/inference/module/module.go`

### decentralized-api
- `decentralized-api/internal/server/public/post_chat_handler.go`

### Tests
- `inference-chain/x/inference/module/chainvalidation_test.go`
- `inference-chain/x/inference/keeper/msg_server_participant_access_test.go`

---

## Merge Checklist

After merging, verify these items are preserved:

### Security Fixes
- [ ] `ValidateURLWithSSRFProtection()` is used for participant URL validation
- [ ] `HasPoCValidation()` check exists to prevent duplicate submissions
- [ ] `ErrPocValidationAlreadyExists` error is defined and used
- [ ] PoC batch size constants are present (`MaxPocBatchNonces`, etc.)
- [ ] BLS duplicate slot indices check exists in `threshold_signing.go`
- [ ] No-redirect HTTP client used in `post_chat_handler.go`
- [ ] Token limits enforced (`MaxAllowedTokens`)
- [ ] No `panic()` or `Must*` calls in chain code (except exempted)

### Functional Changes
- [ ] `ParticipantsWithBalances` query exists
- [ ] `shopspring/decimal` used for dynamic pricing
- [ ] Bitcoin rewards uses table lookup for decay rate
- [ ] PoC validation retry logic preserved in `node_orchestrator.go`
- [ ] Negative coin balance handling in `bitcoin_rewards.go`
- [ ] Future timestamp check disabled in EA

### Infrastructure
- [ ] `.golangci.yml` present with `forbidigo` config
- [ ] `.github/workflows/dont_panic.yml` workflow present
- [ ] `v0_2_8` upgrade handler registered

### Proto Files
- [ ] Run `ignite generate proto-go` after merge to regenerate
- [ ] Verify new queries/messages are present

---

## Merge Strategy

Recommended approach:

1. **Rebase onto upgrade-v0.2.8**:
   ```bash
   git fetch origin
   git rebase origin/upgrade-v0.2.8
   ```

2. **Resolve conflicts** using this document as reference

3. **Regenerate proto files**:
   ```bash
   cd inference-chain
   ignite generate proto-go
   ```

4. **Run tests**:
   ```bash
   cd inference-chain && go test ./...
   cd decentralized-api && go test ./...
   ```

5. **Verify checklist** above
