### validate_basic_todo.md — End-to-end plan and per-message checklist

This document turns proposals/cleanup_1/validate_basic.md into an actionable, deterministic implementation plan for adding rigorous ValidateBasic across the entire inference-chain. It lists every tx message in all modules, shows exactly where it is defined and handled, and provides step-by-step checklists for implementation and testing.

---

### Project-wide objectives

- Implement strong, deterministic, stateless ValidateBasic for all sdk.Msg types across all modules.
- Centralize and reuse canonical limits (max lengths, max list sizes, allowed denoms, numeric bounds) to keep behavior consistent and testable.
- Ensure consistent, human-readable error messages and stable error codes.
- Do not access state, time, randomness, or context in ValidateBasic.

---

### Global implementation flow (do this once per module, then per message)

1) Create/extend module-wide validation constants and helpers
    - Files to prefer:
        - x/<module>/types/validation.go (new helper functions)
        - x/<module>/types/limits.go (max sizes and ranges)
        - x/<module>/types/denoms.go (allowed denoms, if any)
    - Add constants for:
        - Max length caps for strings (IDs, URLs, payloads, memos, descriptions, model IDs, hashes, etc.)
        - Max bytes caps for []byte fields
        - Max counts for repeated fields
        - Allowed denoms (if module-specific)
    - Add helpers:
        - ValidateAddress(bech32 string) error
        - ValidateNonZeroId(u64/i64 string/int) error
        - ValidateHash(hex/base64 string, expectedLen) error
        - ValidateStringNonEmptyTrimmed(s string, maxLen int) error
        - ValidateCoins(coins sdk.Coins/coin sdk.Coin) error (reusing SDK val rules)
        - ValidateEnum(value, allowed...) error
        - ValidateNumbers, ranges and relations (e.g., non-negative/positive, min ≤ max)

2) For each message type under x/<module>/types/message_*.go
    - Implement or extend ValidateBasic to cover:
        - Addresses: creator/authority/participant/etc. bech32 validity (and required signers not empty)
        - Identifiers and numerics: non-zero IDs, ranges
        - Strings and bytes: non-empty when required, length caps, encoding for hashes
        - Repeated fields: size caps, non-empty where required, per-element validation
        - Cross-field stateless relationships (e.g., matching lengths or constraints that don’t need state)
        - Denoms and Coin invariants (positive amounts, sorted coins, etc.)
    - Return precise errors using errorsmod.Wrapf with sdkerrors (e.g., ErrInvalidAddress, ErrInvalidRequest)

3) Unit tests for each message
    - File: x/<module>/types/message_<name>_test.go
    - Cover: valid case, each invalid field with crisp error expectations.

4) Run tests (in inference-chain dir)
    - go test ./...

5) Documentation: If limits are new, reflect them in module README/specs if present.

---

### Reference guide

- Validation rules to follow: proposals/cleanup_1/validate_basic.md
- Message definitions (proto): inference-chain/proto/inference/**/tx.proto
- Message implementation files: x/<module>/types/message_*.go
- Message handlers: x/<module>/keeper/msg_server_*.go (useful to understand required fields, but do NOT access state in ValidateBasic)

---

### Module: inference
Proto: /inference-chain/proto/inference/inference/tx.proto
Types: /inference-chain/x/inference/types/*.go
Keeper msg handlers: /inference-chain/x/inference/keeper/msg_server_*.go

For every message below, implement ValidateBasic in x/inference/types/message_<snake_case>.go using the rules. Where present, extend existing ValidateBasic beyond address-only checks.

General suggested limits for this module (define under x/inference/types/limits.go):
- MaxModelLen = 128
- MaxUrlLen = 2048
- MaxHashLen = 128 (if hex/base64 string; adjust per actual hash format)
- MaxPromptBytes = e.g., 1_000_000 (1 MB) or a project-approved size
- MaxJsonBytes = 1_000_000 for payloads that are JSON blobs (adjust per project)
- MaxSignatureLen = 512 (ASCII/base64); or if hex/base64 with exact length, enforce exact
- MaxAssignees = 256 (adjust per protocol)
- MaxKVKeyLen = 128; MaxKVValueLen = 8192
- NonceListMax = 10_000; DistListMax = 10_000 (ensure both within practical bounds)
- Ensure all lengths are finalized with the team before coding; use constants.

Note: If fields are expected to be hex/base64/bytes-like, define encoding rules and assert expected lengths as per the spec.


1) Add ValidationBasic for StartInference
- Proto: tx.proto -> MsgStartInference
- Handler: x/inference/keeper/msg_server_start_inference.go
- Types file: x/inference/types/message_start_inference.go
- ValidateBasic must check:
    - creator, requested_by, assigned_to (if used) are valid bech32 when present
    - inference_id: non-empty, trimmed, max length; no spaces if constrained; not "0"
    - model: non-empty, max length, charset if any
    - prompt_hash: correct encoding/length if defined; non-empty
    - prompt_payload and original_prompt: non-empty when required, max size
    - node_version: non-empty, sane length
    - max_tokens, prompt_token_count: ≥ 0 and within protocol bounds
    - request_timestamp: non-zero if required
    - transfer_signature: non-empty, encoding/length

2) Add ValidationBasic for FinishInference
- Proto: tx.proto -> MsgFinishInference
- Handler: x/inference/keeper/msg_server_finish_inference.go
- Types file: x/inference/types/message_finish_inference.go
- ValidateBasic must check:
    - creator, executed_by, transferred_by, requested_by valid bech32
    - inference_id non-empty, max length
    - response_hash encoding/length non-empty
    - response_payload non-empty, max size (JSON/text as per spec)
    - prompt_token_count, completion_token_count within ranges (≥0, caps)
    - request_timestamp non-zero if required
    - transfer_signature, executor_signature non-empty and encoding/length
    - original_prompt and model: non-empty, max length; model charset if any

3) Add ValidationBasic for SubmitNewParticipant
- Proto: MsgSubmitNewParticipant
- Handler: x/inference/keeper/msg_server_submit_new_participant.go
- Types file: x/inference/types/message_submit_new_participant.go
- Check:
    - creator valid bech32
    - url valid URI, max length
    - validator_key, worker_key non-empty, length/encoding constraints

4) Add ValidationBasic for Validation
- Proto: MsgValidation
- Handler: x/inference/keeper/msg_server_validation.go
- Types file: x/inference/types/message_validation.go
- Check:
    - creator valid bech32
    - id, inference_id non-empty, max length
    - response_payload, response_hash non-empty, length/encoding
    - value within allowed [min,max] (e.g., [0,1] if probability-like; confirm spec)
    - revalidation boolean requires id/inference_id presence

5) Add ValidationBasic for SubmitNewUnfundedParticipant
- Proto: MsgSubmitNewUnfundedParticipant
- Handler: x/inference/keeper/msg_server_submit_new_unfunded_participant.go
- Types file: x/inference/types/message_submit_new_unfunded_participant.go
- Check:
    - creator valid bech32
    - address valid bech32
    - url valid URI, max length
    - pub_key, validator_key, worker_key: presence and encoding/length

6) Add ValidationBasic for InvalidateInference
- Proto: MsgInvalidateInference
- Handler: x/inference/keeper/msg_server_invalidate_inference.go
- Types file: x/inference/types/message_invalidate_inference.go
- Check: creator bech32; inference_id non-empty, max length

7) Add ValidationBasic for RevalidateInference
- Proto: MsgRevalidateInference
- Handler: x/inference/keeper/msg_server_revalidate_inference.go
- Types file: x/inference/types/message_revalidate_inference.go
- Check: creator bech32; inference_id non-empty, max length

8) Add ValidationBasic for ClaimRewards
- Proto: MsgClaimRewards
- Handler: x/inference/keeper/msg_server_claim_rewards.go
- Types file: x/inference/types/message_claim_rewards.go
- Check: creator bech32; seed could be any int64? If semantic requires non-zero, enforce; poc_start_height > 0

9) Add ValidationBasic for SubmitPocBatch
- Proto: MsgSubmitPocBatch
- Handler: x/inference/keeper/msg_server_submit_poc_batch.go
- Types file: x/inference/types/message_submit_poc_batch.go
- Check:
    - creator bech32
    - poc_stage_start_block_height > 0
    - batch_id non-empty, max length
    - nonces: non-empty, each >= 0, max list size; no duplicates if required
    - dist: non-empty, each finite number, bounds [0,1] if probability-like; length matches nonces if required
    - node_id non-empty, max length

10) Add ValidationBasic for SubmitPocValidation
- Proto: MsgSubmitPocValidation
- Handler: x/inference/keeper/msg_server_submit_poc_validation.go
- Types file: x/inference/types/message_submit_poc_validation.go
- Check:
    - creator, participant_address bech32
    - poc_stage_start_block_height > 0
    - nonces and dist and received_dist lists: non-empty, bounded length, matching lengths where required
    - r_target, fraud_threshold: in valid ranges [0,1] or protocol-defined
    - n_invalid >= 0; probability_honest in [0,1]

11) Add ValidationBasic for SubmitSeed
- Proto: MsgSubmitSeed
- Handler: x/inference/keeper/msg_server_submit_seed.go
- Types file: x/inference/types/message_submit_seed.go
- Check: creator bech32; block_height > 0; signature non-empty with encoding/length

12) Add ValidationBasic for SubmitUnitOfComputePriceProposal
- Proto: MsgSubmitUnitOfComputePriceProposal
- Handler: x/inference/keeper/msg_server_submit_unit_of_compute_price_proposal.go
- Types file: x/inference/types/message_submit_unit_of_compute_price_proposal.go
- Check: creator bech32; price > 0 and within a sane upper bound

13) Add ValidationBasic for RegisterModel
- Proto: MsgRegisterModel
- Handler: x/inference/keeper/msg_server_register_model.go
- Types file: x/inference/types/message_register_model.go
- Check:
    - authority bech32 (signer)
    - proposed_by bech32
    - id non-empty, max length, charset (e.g., [a-zA-Z0-9-_])
    - units_of_compute_per_token > 0
    - hf_repo valid URI or repo name constraints, max length
    - hf_commit exact length/charset (sha-like?)
    - model_args: count bound, each non-empty and length bound
    - v_ram, throughput_per_nonce > 0
    - validation_threshold within [0,1]

14) Add ValidationBasic for CreateTrainingTask
- Proto: MsgCreateTrainingTask
- Handler: x/inference/keeper/msg_server_create_training_task.go
- Types file: x/inference/types/message_create_training_task.go
- Check: creator bech32; hardware_resources non-empty bounded count with per-item structural checks; config present with stateless field ranges

15) Add ValidationBasic for SubmitHardwareDiff
- Proto: MsgSubmitHardwareDiff
- Handler: x/inference/keeper/msg_server_submit_hardware_diff.go
- Types file: x/inference/types/message_submit_hardware_diff.go
- Check: creator bech32; newOrModified and removed lists bounded; each HardwareNode has stateless structural validation (IDs, names, numeric bounds)

16) Add ValidationBasic for CreatePartialUpgrade
- Proto: MsgCreatePartialUpgrade
- Handler: x/inference/keeper/msg_server_create_partial_upgrade.go
- Types file: x/inference/types/message_create_partial_upgrade.go
- Check: authority bech32; height > 0; nodeVersion non-empty max length; apiBinariesJson non-empty and max size (optionally basic JSON shape validation without schema lookups)

17) Add ValidationBasic for ClaimTrainingTaskForAssignment
- Proto: MsgClaimTrainingTaskForAssignment
- Handler: x/inference/keeper/msg_server_claim_training_task_for_assignment.go
- Types file: x/inference/types/message_claim_training_task_for_assignment.go
- Check: creator bech32; task_id > 0

18) Add ValidationBasic for AssignTrainingTask
- Proto: MsgAssignTrainingTask
- Handler: x/inference/keeper/msg_server_assign_training_task.go
- Types file: x/inference/types/message_assign_training_task.go
- Check: creator bech32 (already); task_id > 0; assignees non-empty, max count; each assignee’s address bech32 and fields statelessly valid

19) Add ValidationBasic for SubmitTrainingKvRecord
- Proto: MsgSubmitTrainingKvRecord
- Handler: x/inference/keeper/msg_server_submit_training_kv_record.go
- Types file: x/inference/types/message_submit_training_kv_record.go
- Check: creator bech32; taskId > 0; participant bech32; key/value non-empty with max lengths; optional charset on key

20) Add ValidationBasic for JoinTraining
- Proto: MsgJoinTraining
- Handler: x/inference/keeper/msg_server_join_training.go
- Types file: x/inference/types/message_join_training.go
- Check: creator bech32; req present; req subfields statelessly valid (addresses, capacity numbers > 0, lists bounded)

21) Add ValidationBasic for TrainingHeartbeat
- Proto: MsgTrainingHeartbeat
- Handler: x/inference/keeper/msg_server_training_heartbeat.go
- Types file: x/inference/types/message_training_heartbeat.go
- Check: creator bech32; req present; req subfields bounded and sane (counters non-negative, arrays bounded)

22) Add ValidationBasic for SetBarrier
- Proto: MsgSetBarrier
- Handler: x/inference/keeper/msg_server_set_barrier.go
- Types file: x/inference/types/message_set_barrier.go
- Check: creator bech32; req present with stateless checks (e.g., barrier name non-empty, participant sets bounded)

23) Add ValidationBasic for JoinTrainingStatus
- Proto: MsgJoinTrainingStatus
- Handler: x/inference/keeper/msg_server_join_training_status.go
- Types file: x/inference/types/message_join_training_status.go
- Check: creator bech32; req present with stateless checks

24) Add ValidationBasic for CreateDummyTrainingTask
- Proto: MsgCreateDummyTrainingTask
- Handler: x/inference/keeper/msg_server_create_dummy_training_task.go
- Types file: x/inference/types/message_create_dummy_training_task.go
- Check: creator bech32; embedded TrainingTask present and structurally valid statelessly

25) Add ValidationBasic for BridgeExchange
- Proto: MsgBridgeExchange
- Handler: x/inference/keeper/msg_server_bridge_exchange.go
- Types file: x/inference/types/message_bridge_exchange.go
- Check:
    - validator bech32 (signer)
    - originChain non-empty; restrict allowed chains if applicable (enum)
    - contractAddress format/length (hex/0x-prefixed?); ownerAddress format for origin chain if static rules known; otherwise length caps
    - ownerPubKey encoding/length; amount non-empty numeric string with range checks
    - blockNumber, receiptIndex numeric strings; receiptsRoot hash encoding and exact length

26) UpdateParams (inference)
- Proto: MsgUpdateParams
- Handler: x/inference/keeper/msg_update_params.go
- Types file: x/inference/types/msg_update_params.go
- Check: authority bech32; params.Validate() statelessly validates each param bound (already standard pattern)


### Module: bls
Proto: /inference-chain/proto/inference/bls/tx.proto
Types: /inference-chain/x/bls/types/*.go
Keeper handlers: /inference-chain/x/bls/keeper/msg_server_*.go

General suggested limits (x/bls/types/limits.go):
- MaxParticipantCount, fixed sizes for G1(48 bytes)/G2(96 bytes) in compressed form
- request_id, chain_id length: 32 bytes each

1) UpdateParams (bls)
- Proto: MsgUpdateParams
- Handler: x/bls/keeper/msg_update_params.go
- Types: x/bls/types/msg_update_params.go
- Check: authority bech32; params.Validate()

2) SubmitDealerPart
- Proto: MsgSubmitDealerPart
- Handler: x/bls/keeper/msg_server_dealer.go
- Types: x/bls/types/message_submit_dealer_part.go
- Check:
    - creator bech32; epoch_id > 0
    - commitments: non-empty list, each bytes of expected G2 length (96 compressed) and non-zero
    - encrypted_shares_for_participants: non-empty, bounded, each sub-struct statelessly valid (lengths, indices)

3) SubmitVerificationVector
- Proto: MsgSubmitVerificationVector
- Handler: x/bls/keeper/msg_server_verifier.go
- Types: x/bls/types/message_submit_verification_vector.go
- Check: creator bech32; epoch_id > 0; dealer_validity non-empty bounded; only booleans allowed (length <= participant count cap)

4) SubmitGroupKeyValidationSignature
- Proto: MsgSubmitGroupKeyValidationSignature
- Handler: x/bls/keeper/msg_server_group_validation.go
- Types: x/bls/types/message_submit_group_key_validation_signature.go
- Check: creator bech32; new_epoch_id > 0; slot_indices non-empty bounded with valid ranges; partial_signature length == 48 bytes (G1 compressed)

5) SubmitPartialSignature
- Proto: MsgSubmitPartialSignature
- Handler: x/bls/keeper/msg_server_threshold_signing.go
- Types: x/bls/types/message_submit_partial_signature.go
- Check: creator bech32; request_id length == 32 bytes; slot_indices bounded; partial_signature length == 48 bytes

6) RequestThresholdSignature
- Proto: MsgRequestThresholdSignature
- Handler: x/bls/keeper/msg_server_threshold_signing.go
- Types: x/bls/types/message_request_threshold_signature.go
- Check: creator bech32; current_epoch_id > 0; chain_id == 32 bytes; request_id == 32 bytes; data non-empty bounded and each element == 32 bytes


### Module: collateral
Proto: /inference-chain/proto/inference/collateral/tx.proto
Types: /inference-chain/x/collateral/types/*.go
Keeper handlers: /inference-chain/x/collateral/keeper/msg_server_*.go

1) UpdateParams (collateral)
- Proto: MsgUpdateParams
- Handler: x/collateral/keeper/msg_update_params.go
- Types: x/collateral/types/msg_update_params.go
- Check: authority bech32; params.Validate()

2) DepositCollateral
- Proto: MsgDepositCollateral
- Handler: x/collateral/keeper/msg_server_deposit_collateral.go
- Types: x/collateral/types/message_deposit_collateral.go
- Check: participant bech32; amount denom/amount valid and > 0; denom allow-list if needed

3) WithdrawCollateral
- Proto: MsgWithdrawCollateral
- Handler: x/collateral/keeper/msg_server_withdraw_collateral.go
- Types: x/collateral/types/message_withdraw_collateral.go
- Check: participant bech32; amount denom/amount valid and > 0


### Module: bookkeeper
Proto: /inference-chain/proto/inference/bookkeeper/tx.proto
Types: /inference-chain/x/bookkeeper/types/*.go
Keeper handlers: /inference-chain/x/bookkeeper/keeper/msg_update_params.go

1) UpdateParams (bookkeeper)
- Proto: MsgUpdateParams
- Handler: x/bookkeeper/keeper/msg_update_params.go
- Types: x/bookkeeper/types/msg_update_params.go
- Check: authority bech32; params.Validate()


### Module: streamvesting
Proto: /inference-chain/proto/inference/streamvesting/tx.proto
Types: /inference-chain/x/streamvesting/types/*.go
Keeper handlers: /inference-chain/x/streamvesting/keeper/msg_update_params.go

1) UpdateParams (streamvesting)
- Proto: MsgUpdateParams
- Handler: x/streamvesting/keeper/msg_update_params.go
- Types: x/streamvesting/types/msg_update_params.go
- Check: authority bech32; params.Validate()

---

### Detailed per-message checklists (copy/paste as you work)

Use this uniform template per message. Keep boxes checked as you progress.

Template:
- [ ] Locate types file: x/<module>/types/message_<snake>.go (create if missing)
- [ ] Implement ValidateBasic with:
    - [ ] Address checks (bech32)
    - [ ] Non-empty/trimmed strings with max lengths
    - [ ] Numeric ranges and non-zero IDs
    - [ ] Coins/denoms rules
    - [ ] Bytes/encoding sizes
    - [ ] Repeated fields bounds and per-element checks
    - [ ] Stateless cross-field relations
- [ ] Add/extend tests in x/<module>/types/message_<snake>_test.go (valid + invalids)

Now, the concrete list for this repository:

inference module
- MsgStartInference
    - Types: x/inference/types/message_start_inference.go
    - Handler: x/inference/keeper/msg_server_start_inference.go
    - [x] Implement/extend ValidateBasic
    - [ ] Tests
- MsgFinishInference
    - Types: x/inference/types/message_finish_inference.go
    - Handler: x/inference/keeper/msg_server_finish_inference.go
    - [x] Implement/extend ValidateBasic
    - [ ] Tests
- MsgSubmitNewParticipant
    - Types: x/inference/types/message_submit_new_participant.go
    - Handler: x/inference/keeper/msg_server_submit_new_participant.go
    - [x] Implement ValidateBasic
    - [ ] Tests
- MsgValidation
    - Types: x/inference/types/message_validation.go
    - Handler: x/inference/keeper/msg_server_validation.go
    - [x] Implement ValidateBasic
    - [ ] Tests
- MsgSubmitNewUnfundedParticipant
    - Types: x/inference/types/message_submit_new_unfunded_participant.go
    - Handler: x/inference/keeper/msg_server_submit_new_unfunded_participant.go
    - [x] Implement ValidateBasic
    - [ ] Tests
- MsgInvalidateInference
    - Types: x/inference/types/message_invalidate_inference.go
    - Handler: x/inference/keeper/msg_server_invalidate_inference.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgRevalidateInference
    - Types: x/inference/types/message_revalidate_inference.go
    - Handler: x/inference/keeper/msg_server_revalidate_inference.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgClaimRewards
    - Types: x/inference/types/message_claim_rewards.go
    - Handler: x/inference/keeper/msg_server_claim_rewards.go
    - [x] Implement/extend ValidateBasic
    - [x] Tests
- MsgSubmitPocBatch
    - Types: x/inference/types/message_submit_poc_batch.go
    - Handler: x/inference/keeper/msg_server_submit_poc_batch.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgSubmitPocValidation
    - Types: x/inference/types/message_submit_poc_validation.go
    - Handler: x/inference/keeper/msg_server_submit_poc_validation.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgSubmitSeed
    - Types: x/inference/types/message_submit_seed.go
    - Handler: x/inference/keeper/msg_server_submit_seed.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgSubmitUnitOfComputePriceProposal
    - Types: x/inference/types/message_submit_unit_of_compute_price_proposal.go
    - Handler: x/inference/keeper/msg_server_submit_unit_of_compute_price_proposal.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgRegisterModel
    - Types: x/inference/types/message_register_model.go
    - Handler: x/inference/keeper/msg_server_register_model.go
    - [ ] Implement ValidateBasic
    - [ ] Tests
- MsgCreateTrainingTask
    - Types: x/inference/types/message_create_training_task.go
    - Handler: x/inference/keeper/msg_server_create_training_task.go
    - [ ] Implement ValidateBasic
    - [ ] Tests
- MsgSubmitHardwareDiff
    - Types: x/inference/types/message_submit_hardware_diff.go
    - Handler: x/inference/keeper/msg_server_submit_hardware_diff.go
    - [ ] Implement/extend ValidateBasic
    - [ ] Tests
- MsgCreatePartialUpgrade
    - Types: x/inference/types/message_create_partial_upgrade.go
    - Handler: x/inference/keeper/msg_server_create_partial_upgrade.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgClaimTrainingTaskForAssignment
    - Types: x/inference/types/message_claim_training_task_for_assignment.go
    - Handler: x/inference/keeper/msg_server_claim_training_task_for_assignment.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgAssignTrainingTask
    - Types: x/inference/types/message_assign_training_task.go
    - Handler: x/inference/keeper/msg_server_assign_training_task.go
    - [x] Extend ValidateBasic (task_id, assignees)
    - [x] Tests
- MsgSubmitTrainingKvRecord
    - Types: x/inference/types/message_submit_training_kv_record.go
    - Handler: x/inference/keeper/msg_server_submit_training_kv_record.go
    - [ ] Implement ValidateBasic
    - [ ] Tests
- MsgJoinTraining
    - Types: x/inference/types/message_join_training.go
    - Handler: x/inference/keeper/msg_server_join_training.go
    - [ ] Implement ValidateBasic
    - [ ] Tests
- MsgTrainingHeartbeat
    - Types: x/inference/types/message_training_heartbeat.go
    - Handler: x/inference/keeper/msg_server_training_heartbeat.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgSetBarrier
    - Types: x/inference/types/message_set_barrier.go
    - Handler: x/inference/keeper/msg_server_set_barrier.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgJoinTrainingStatus
    - Types: x/inference/types/message_join_training_status.go
    - Handler: x/inference/keeper/msg_server_join_training_status.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgCreateDummyTrainingTask
    - Types: x/inference/types/message_create_dummy_training_task.go
    - Handler: x/inference/keeper/msg_server_create_dummy_training_task.go
    - [x] Implement ValidateBasic
    - [x] Tests
- MsgBridgeExchange
    - Types: x/inference/types/message_bridge_exchange.go
    - Handler: x/inference/keeper/msg_server_bridge_exchange.go
    - [x] Implement/extend ValidateBasic
    - [x] Tests
- MsgUpdateParams (inference)
    - Types: x/inference/types/msg_update_params.go
    - Handler: x/inference/keeper/msg_update_params.go
    - [ ] Ensure ValidateBasic (authority + params.Validate())
    - [ ] Tests (if missing)

bls module
- MsgUpdateParams
    - Types: x/bls/types/msg_update_params.go
    - Handler: x/bls/keeper/msg_update_params.go
    - [x] Ensure ValidateBasic
- MsgSubmitDealerPart
    - Types: x/bls/types/message_submit_dealer_part.go
    - Handler: x/bls/keeper/msg_server_dealer.go
    - [x] Implement ValidateBasic
- MsgSubmitVerificationVector
    - Types: x/bls/types/message_submit_verification_vector.go
    - Handler: x/bls/keeper/msg_server_verifier.go
    - [x] Implement ValidateBasic
- MsgSubmitGroupKeyValidationSignature
    - Types: x/bls/types/message_submit_group_key_validation_signature.go
    - Handler: x/bls/keeper/msg_server_group_validation.go
    - [x] Implement ValidateBasic
- MsgSubmitPartialSignature
    - Types: x/bls/types/message_submit_partial_signature.go
    - Handler: x/bls/keeper/msg_server_threshold_signing.go
    - [x] Implement ValidateBasic
- MsgRequestThresholdSignature
    - Types: x/bls/types/message_request_threshold_signature.go
    - Handler: x/bls/keeper/msg_server_threshold_signing.go
    - [x] Implement ValidateBasic

collateral module
- MsgUpdateParams
    - Types: x/collateral/types/msg_update_params.go
    - Handler: x/collateral/keeper/msg_update_params.go
    - [x] Ensure ValidateBasic
- MsgDepositCollateral
    - Types: x/collateral/types/msg_deposit_collateral.go
    - Handler: x/collateral/keeper/msg_server_deposit_collateral.go
    - [x] Implement ValidateBasic
- MsgWithdrawCollateral
    - Types: x/collateral/types/msg_withdraw_collateral.go
    - Handler: x/collateral/keeper/msg_server_withdraw_collateral.go
    - [x] Implement ValidateBasic

bookkeeper module
- MsgUpdateParams
    - Types: x/bookkeeper/types/msg_update_params.go
    - Handler: x/bookkeeper/keeper/msg_update_params.go
    - [ ] Ensure ValidateBasic

streamvesting module
- MsgUpdateParams
    - Types: x/streamvesting/types/msg_update_params.go
    - Handler: x/streamvesting/keeper/msg_update_params.go
    - [x] Ensure ValidateBasic

---

### Testing checklist (for each message)

- [ ] Valid case passes
- [ ] Invalid bech32 for each address field fails with ErrInvalidAddress
- [ ] Empty/oversized strings fail with ErrInvalidRequest and specific message
- [ ] Invalid hashes/signatures encodings fail with ErrInvalidRequest
- [ ] Out-of-range numbers fail with ErrInvalidRequest
- [ ] Repeated field size violations fail with ErrInvalidRequest
- [ ] Cross-field stateless mismatch (e.g., length mismatches) fails with ErrInvalidRequest
- [ ] Coins with zero/negative amounts fail with SDK coin validation error

---

### Process discipline & governance

- Never read store, context, or block time in ValidateBasic.
- Keep message-level limits consistent via centralized constants.
- Update docs/specs when introducing new public-facing limits or format constraints.
- After implementation, run: go test ./... from inference-chain directory.

---

### Notes linking back to guide

All checks above are derived from proposals/cleanup_1/validate_basic.md: addresses/signers; IDs/numerics; coins/denoms; strings/bytes; URIs; temporal hints; enums/oneof; cross-field relations; crypto material length/encoding; DOS guards; and param/genesis validation patterns for UpdateParams.
