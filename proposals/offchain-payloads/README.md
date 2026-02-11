# Proposal: Offchain Payloads

## Goal / Problem

All inference prompts and response artifacts are currently stored on-chain. Validation requires access to inference artifacts: output tokens and top-k logprobs.

**Current bandwidth consumption:**
- Block size limit: 22MB
- Typical payload: 1000 input + 150 output tokens = ~102 KB total
  - Input: ~0.0023 KB/token mean
  - Output: ~0.64 KB/token mean due to top-k=5 logprobs
- Current throughput: ~42 requests/second average, ~18 requests/second P90
- Bandwidth constrains inference throughput significantly below compute capacity

**After offchain payloads:**
- Transaction size: ~500 bytes (hashes + metadata only)
- ~200x reduction per inference
- Bandwidth no longer bottleneck

**Transaction structure:**
- `MsgStartInference`: `prompt_hash`, metadata
- `MsgFinishInference`: `response_hash`, metadata
- `Inference` state: only hashes and metadata, payloads stored offchain
- Validators retrieve `prompt_payload` and `response_payload` offchain for validation

**Signature verification:** 
Signatures use hashes instead of full payloads:
- User signature: `original_prompt_hash + timestamp + transfer_address` (verified by TA off-chain AND chain)
- Transfer agent signature: `prompt_hash + timestamp + transfer_address + executor_address` (verified by executor off-chain AND chain)
- Executor signature: `prompt_hash + timestamp + transfer_address + executor_address` (verified by chain)

Where `original_prompt_hash` = SHA256(original_prompt), `prompt_hash` = SHA256(prompt_payload). TA verifies user signature off-chain for early rejection, then creates prompt_payload and signs prompt_hash. Chain verifies ALL signatures (dev, TA, executor) for security. Off-chain verification provides better UX through early error detection.

## Proposal

Move validation payloads offchain while preserving validation integrity and signature verification.

**What goes offchain:**
- `prompt_payload`: Canonical JSON with seed and modifications needed for validation
- `response_payload`: Full response with output tokens and top-k=5 logprobs
- `original_prompt`: Original request body no longer needed on-chain

**What stays on-chain:**
- `prompt_hash` and `response_hash`: SHA256 hashes for cryptographic commitment
- Token counts, timestamps, addresses, metadata

**Storage:**
- Local storage on each node, organized by epoch for efficient pruning
- File-based initially, interface supports future PostgreSQL backend

**Execution flow:**
1. User computes `original_prompt_hash`, signs it with their key, sends request to transfer agent REST API
2. Transfer agent verifies user signature, assigns executor
3. Transfer agent creates `prompt_payload` (adds seed, logprobs), computes `prompt_hash`
4. Transfer agent signs `prompt_hash` + timestamp + addresses, sends `prompt_payload` to executor via REST API
5. Transfer agent broadcasts `MsgStartInference` with `prompt_hash`, metadata, and TA signature
6. Chain verifies TA signature over `prompt_hash`
7. Executor validates `prompt_payload` matches `prompt_hash` before execution
8. Executor runs inference, stores both `prompt_payload` and `response_payload` locally
9. Executor computes `response_hash`, broadcasts `MsgFinishInference` with hash and metadata

**Validation flow:**
1. Validator requests both payloads from executor's REST API endpoint
2. Validator verifies both hashes match on-chain commitments
3. Validator re-runs inference with `prompt_payload` and enforced tokens for validation
4. On hash mismatch, validator detects cheating (planned: submit executor's signed payload as proof for fast invalidation without voting - not yet implemented)
5. On payload unavailability (timeout/refusal), validator retries for defined period (e.g. 20m). If still unavailable, initiates voting for invalidation

**User impact:** Client SDK must compute `prompt_hash` and sign hash instead of full payload. REST API endpoint unchanged. Users using SDK will see no behavioral change, just updated SDK version required.

**Authentication protocol:**
- TA→Executor (prompt submission): TA signs `inferenceId` + `prompt_payload` + `timestamp` with warm key
- Validator→Executor (payload retrieval): Validator signs `inferenceId` + `timestamp` with warm key
- Model-based authorization: only participants serving same model can submit/request payloads
- Participant verification: Executor verifies signer is an active participant (Validator/TA) in the current epoch
- Timestamp prevents replay attacks (reject requests >60s old)
- Executor response: `inferenceId` + payloads signed by warm key
- Executor signature provides non-repudiable proof for invalidation without voting
- Uses existing SECP256K1 signature infrastructure

**Future optimization:** Transaction batching in Phase 2 to further reduce overhead by batching multiple inferences per transaction.

## Security Analysis

**1. Payload Unavailability (Withholding):**
Executor commits hash but fails to serve payload (timeout or refusal). Validator attempts retrieval for a defined window (e.g., 20 minutes) to rule out temporary network issues. If still unavailable, validator initiates voting for invalidation. Unavailability is treated as a validity failure since validators cannot verify work.

**2. Wrong Payload Attack:**
Executor serves payload mismatching committed hash. Validator detects hash mismatch. Current behavior: logs error, triggers invalidation via existing voting mechanism. Planned (Phase 7): submit executor's signed payload as cryptographic proof for immediate invalidation without voting. Warm keys must remain stable during epoch for accountability.

**3. Hash Collision Attack:**
SHA256 collision resistance makes finding alternate payload with same hash cryptographically infeasible. Negligible risk.

**4. Replay Attack:**
Request signature includes timestamp. Executor rejects requests with timestamps >60s old using existing validation logic. Prevents unauthorized repeated access.

**Non-repudiation:** Executor's warm key signature on served payload provides cryptographic proof. Planned (Phase 7): use this proof for fast invalidation without voting when executor serves wrong data.

## Implementation

**Affected Components:**
- `decentralized-api`: 
  - Transfer agent: Send prompts to executor REST API before broadcasting transaction
  - Executor: Storage layer for payloads, REST API endpoints for receiving prompts and serving payloads
  - Validator: Payload retrieval from executor REST API
- `inference-chain`: Transaction protos remove all payload fields, signature verification uses hash instead of full payload
- `testermint`: Integration tests for offchain p2p communication between TA→Executor and Validator→Executor

**Key changes:**
- Transfer agent must send prompt to executor via REST before broadcasting transaction
- User signature: `original_prompt` → `original_prompt_hash` (verified by TA off-chain)
- TA signature: `original_prompt` → `prompt_hash` (verified by chain)
- All payload data eliminated from transactions

**Storage Interface:**
```
interface PayloadStorage {
    Store(inferenceId, epochId, payload)
    Retrieve(inferenceId) -> payload
    PruneEpoch(epochId)
}
```
File-based implementation: `storage/{epochId}/{inferenceId}.json`
Hash computation and verification in application layer.

**Implementation Phases:**
1. **Storage Layer** - Executor storage module for payloads, file-based backend, unit tests
2. **Dual Write** - TA sends prompts to executor REST API, store payloads both on-chain and locally, verify consistency
3. **Signature Migration** - Migrate to signing hashes: user signs `original_prompt_hash` (TA verifies off-chain), TA signs `prompt_hash` (chain verifies)
4. **Validator Retrieval** - Implement payload serving endpoints for validators, fallback to on-chain for old inferences
5. **Validation Migration** - Validators use REST API primary, on-chain fallback for old inferences, vote for invalidation if executor unavailable after retry window (~20m)
6. **Chain Migration** - Remove all payload fields from transactions, state, and `Inference` proto
7. **Invalidation Proof Endpoint** (not yet implemented) - Endpoint for validators to submit executor's signed payload as proof when hash mismatch detected (fast invalidation without voting)

Each phase independently testable with rollback capability.

**Testing Strategy:**
- Unit: Storage operations, hash verification, pruning
- Integration: REST API retrieval, signature validation, timeout handling, hash mismatches
- Testermint: Unavailability, wrong payloads, concurrent validation
- Load: Verify bandwidth no longer bottlenecks throughput
