# BLS Key Generation Module Development Plan (v2)

This document outlines the step-by-step plan to develop the BLS Key Generation module, integrating with the existing `inference-chain` and `decentralized-api` components.

## I. Initial Setup & Prerequisites

### I.1 [x] Create New Cosmos SDK Module (`bls`)
*   Action: Scaffold a new module named `bls` within the `inference-chain` codebase.
*   Details: This includes creating basic module structure (`module.go`, `keeper/`, `types/`, `handler.go`, etc.).
*   Files: `x/bls/...`

### I.2 [x] Register `bls` Module
*   Action: Register the new `bls` module in the application's main file (`app.go`).
*   Details: Add `bls` to `ModuleBasics`, `keepers`, `storeKeys`, `scopedKeepers` (if needed), and module manager.
*   Files: `app.go`

### I.3 [x] Define Basic BLS Configuration (Genesis State for `bls` module)
*   Action: Define parameters for the `bls` module that can be set at genesis.
*   Details: This might include `I_total_slots` (e.g., 100 for PoC), `t_slots_degree_offset` (e.g., `floor(I_total / 2)`), dealing phase duration in blocks, verification phase duration in blocks.
*   Note: Phase durations are defined in block numbers (int64) following the existing inference module pattern, not time durations.
*   Files: `x/bls/types/genesis.go`, `x/bls/genesis.go`

### I.4 [x] Test: Basic module setup verification
*   Action: Run `make node-test` to ensure the chain initializes correctly with the new BLS module and all chain-specific tests pass.
*   Details: This runs the official inference-chain unit tests (`go test ./... -v`) and verifies that the BLS module integration doesn't break existing functionality.
*   Expected: All tests pass, including new BLS module tests, with detailed output logged to `node-test-output.log`.

## II. Pre-Step 0: Using Existing secp256k1 Keys

### II.1 [x] Proto Definition (`inference` module): `MsgSubmitNewParticipant`
*   Action: Verify that the existing `MsgSubmitNewParticipant` message includes the secp256k1 public key. Add the field only if missing.
*   Fields: `creator` (string, participant's address), `secp256k1_public_key` (bytes or string).
*   Files: `proto/inference/tx.proto`, `x/inference/types/tx.pb.go`
*   Important: When verifying, check all existing key-related fields even if they have different names (e.g., `validator_key`, `pub_key`, `public_key`) to see if any contain the needed secp256k1 key format. If the field already exists with proper validation, add a note with the name of the field, and update task status to complete without code changes.
*   Note: ✅ Field already exists as `validator_key` (field 3, string) - this is the base64-encoded secp256k1 public key used for DKG operations.

### II.2 [x] Chain-Side Handler (`inference` module): Verify `SubmitNewParticipant`
*   Action: Ensure the handler for `MsgSubmitNewParticipant` properly stores the secp256k1 public key.
*   Logic:
    *   Authenticate sender (`creator`).
    *   Store participant data including the secp256k1 public key.
*   Files: `x/inference/keeper/msg_server_submit_new_participant.go`
*   Status: ✅ **COMPLETED** Handler verified working correctly - authenticates sender via `msg.GetCreator()` and stores secp256k1 public key via `ValidatorKey: msg.GetValidatorKey()` in the `Participant` struct.

### II.3 [x] Controller-Side (`decentralized-api`): Use Existing secp256k1 Key
*   Action: Ensure the controller uses its existing secp256k1 key for DKG operations.
*   Logic: When gathering data for `MsgSubmitNewParticipant`, use the existing secp256k1 public key.
*   Files: `decentralized-api/participant_registration/participant_registration.go`
*   Status: ✅ **COMPLETED** Controller verified working correctly - uses `getValidatorKey()` to retrieve secp256k1 public key from Tendermint RPC (`result.ValidatorInfo.PubKey`) and properly encodes it as `ValidatorKey` field in both `registerGenesisParticipant()` and `registerJoiningParticipant()` functions.

### II.4 [x] Test
*   Action: Create unit tests for the `SubmitNewParticipant` message handler in the `inference` module.
*   Action: Create integration tests where a controller registers using its secp256k1 key and verify chain state.
*   Action: Test the controller's key usage in DKG operations.
*   Status: ✅ **COMPLETED** Enhanced existing tests in `msg_server_submit_new_participant_test.go` with comprehensive secp256k1 key testing:
    *   `TestMsgServer_SubmitNewParticipant`: Tests full participant creation with valid secp256k1 ValidatorKey and WorkerKey
    *   `TestMsgServer_SubmitNewParticipant_WithEmptyKeys`: Tests graceful handling of empty keys
    *   `TestMsgServer_SubmitNewParticipant_ValidateSecp256k1Key`: Tests secp256k1 key validation, storage, and reconstruction
    *   All 359 chain tests still pass, confirming no regressions were introduced.
*   Status: ✅ **COMPLETED** Controller tests verified: All 56 decentralized-api tests pass via `make api-test`, including participant registration functionality that uses `getValidatorKey()` to retrieve and encode secp256k1 keys for DKG operations.

## III. Step 1: DKG Initiation (On-Chain `bls` and `inference` modules)

### III.1 [x] Proto Definition (`bls` module): `EpochBLSData`
*   Action: Define `EpochBLSData` Protobuf message.
*   Fields:
    *   `epoch_id` (uint64)
    *   `i_total_slots` (uint32)
    *   `t_slots_degree` (uint32) // Polynomial degree `t`
    *   `participants` (repeated `BLSParticipantInfo`)
        *   `BLSParticipantInfo`: `address` (string), `percentage_weight` (string/sdk.Dec), `secp256k1_public_key` (bytes), `slot_start_index` (uint32), `slot_end_index` (uint32)
    *   `dkg_phase` (enum: `UNDEFINED`, `DEALING`, `VERIFYING`, `COMPLETED`, `FAILED`)
    *   `dealing_phase_deadline_block` (int64) // Block height deadline, not duration
    *   `verifying_phase_deadline_block` (int64) // Block height deadline, not duration
    *   `group_public_key` (bytes, G2 point)
    *   `dealer_parts` (repeated DealerPartStorage) // Array indexed by participant order
        *   `DealerPartStorage`: `dealer_address` (string), `commitments` (repeated bytes), `participant_shares` (repeated EncryptedSharesForParticipant) // Index i = shares for participants[i]
        *   `EncryptedSharesForParticipant`: `encrypted_shares` (repeated bytes) // Index i = share for slot (participant.slot_start_index + i)
    *   `verification_vectors_submitters` (repeated string) // list of addresses who submitted verification vectors
*   Files: `proto/bls/types.proto`, `x/bls/types/types.pb.go`
*   Important: All structures use deterministic repeated arrays with direct indexing. `dealer_parts` array index matches `participants` array index. `participant_shares` array index i contains shares for `participants[i]`.
*   Note: ✅ Created complete protobuf definitions in `proto/inference/bls/types.proto` with simplified deterministic structures:
    *   `DKGPhase` enum with all phases (`UNDEFINED`, `DEALING`, `VERIFYING`, `COMPLETED`, `FAILED`)
    *   `BLSParticipantInfo` with address, weight (sdk.Dec), secp256k1 key, and slot indices
    *   `EncryptedSharesForParticipant` with `repeated bytes encrypted_shares` where index i = share for slot (participant.slot_start_index + i)
    *   `DealerPartStorage` with `repeated EncryptedSharesForParticipant participant_shares` where index i = shares for participants[i]
    *   `EpochBLSData` with all specified fields using deterministic array indexing
    *   Eliminated all map usage for consensus safety - uses direct array indexing throughout

### III.2 [x] Proto Definition (`bls` module): `EventKeyGenerationInitiated`
*   Action: Define `EventKeyGenerationInitiated` Protobuf message for events.
*   Fields: `epoch_id` (uint64), `i_total_slots` (uint32), `t_slots_degree` (uint32), `participants` (repeated `BLSParticipantInfo`).
*   Files: `proto/bls/events.proto`, `x/bls/types/events.pb.go`
*   Status: ✅ **COMPLETED** Created `proto/inference/bls/events.proto` with `EventKeyGenerationInitiated` event containing:
    *   `epoch_id` (uint64) - unique DKG round identifier
    *   `i_total_slots` (uint32) - total number of DKG slots
    *   `t_slots_degree` (uint32) - polynomial degree for threshold scheme
    *   `participants` (repeated BLSParticipantInfo) - complete participant info with slots and keys
    *   Generated Go code successfully (12KB events.pb.go), all 359 chain tests pass.

### III.3 [x] `bls` Module Keeper: `InitiateKeyGenerationForEpoch` Function
*   Action: Implement `InitiateKeyGenerationForEpoch` in `x/bls/keeper/dkg_initiation.go` (or `keeper.go`).
*   Signature: `func (k Keeper) InitiateKeyGenerationForEpoch(ctx sdk.Context, epochID uint64, finalizedParticipants []inferencekeeper.ParticipantWithWeightAndKey) error`
    *   `ParticipantWithWeightAndKey`: A temporary struct/type passed from `inference` module, containing `address`, `percentage_weight`, `secp256k1_public_key`.
*   Logic:
    *   Authenticate caller (e.g., ensure it's called by the `inference` module by checking capabilities or a pre-defined authority).
    *   Retrieve `I_total_slots` and calculate `t_slots_degree` from module params.
    *   Perform deterministic slot assignment based on `percentage_weight` to populate `slot_start_index` and `slot_end_index` for each participant. Ensure all slots are assigned proportionally and without overlap.
    *   Create and store `EpochBLSData` for `epochID`.
    *   Set `dkg_phase` to `DEALING`.
    *   Calculate and set `dealing_phase_deadline_block` based on current block height and configured duration.
    *   Emit `EventKeyGenerationInitiated` using `sdk.EventManager`.
*   Files: `x/bls/keeper/dkg_initiation.go`, `x/bls/keeper/keeper.go`
*   Status: ✅ **COMPLETED** - Function implemented with:
    *   `ParticipantWithWeightAndKey` struct defined locally in keeper package
    *   Deterministic slot assignment with proper weight-based distribution 
    *   `AssignSlots` helper function with comprehensive test coverage
    *   `EpochBLSData` creation and storage with proper deadline calculations
    *   Event emission for `EventKeyGenerationInitiated`
    *   Full unit test coverage for slot assignment edge cases
    *   All tests passing

### III.4 [x] `inference` Module Modification: Call `InitiateKeyGenerationForEpoch`
*   Action: In the `inference` module's `EndBlock` logic, after `onSetNewValidatorsStage` successfully completes.
*   Logic:
    *   Gather the `finalized_validator_set_with_weights` (including their secp256k1 public keys, fetched from `Participant` data).
    *   Make an internal call to `blsKeeper.InitiateKeyGenerationForEpoch(ctx, nextEpochID, finalized_validator_set_with_weights_and_keys)`.
*   Files: `x/inference/module/module.go` (or where `EndBlock` logic resides), `x/inference/keeper/keeper.go` (to add dependency on `blsKeeper`).
*   Status: ✅ **COMPLETED** Integration implemented successfully:
    *   Added `BlsKeeper` field to inference keeper with proper dependency injection
    *   Updated `ModuleInputs` and `ProvideModule` to include BLS keeper dependency
    *   Implemented `initiateBLSKeyGeneration` function in inference module that:
        *   Converts `ActiveParticipant` data to `ParticipantWithWeightAndKey` format
        *   Calculates percentage weights from absolute weights
        *   Decodes base64-encoded secp256k1 public keys
        *   Calls `BlsKeeper.InitiateKeyGenerationForEpoch` with proper context conversion
    *   Added call to `initiateBLSKeyGeneration` at end of `onSetNewValidatorsStage`
    *   Updated test utilities to include BLS keeper for testing
    *   Created comprehensive integration tests verifying:
        *   Successful BLS key generation with valid participants
        *   Proper handling of empty participant lists
        *   Graceful error handling for invalid secp256k1 keys
        *   Correct data conversion and slot assignment
    *   All 359+ chain tests pass, confirming no regressions introduced

### III.5 [x] End-to-End Epoch Transition Integration Test
*   Action: Create comprehensive integration test that simulates complete epoch transition and verifies inference module successfully triggers BLS key generation.
*   Action: Implement `TestCompleteEpochTransitionWithBLS` function that:
    *   Sets up realistic epoch conditions (participants with WorkerPublicKeys, epoch params, block heights).
    *   Sets up epoch group data and upcoming epoch group.
    *   Calls `onSetNewValidatorsStage()` (the real entry point for epoch transition).
    *   Verifies complete integration (ActiveParticipants storage + BLS initiation).
    *   Tests error scenarios (missing participants, invalid WorkerPublicKeys, epoch transition failures).
*   Action: Create helper functions for test setup (participants, epoch data, etc.).
*   Action: Verify test covers full data flow: epoch transition → participant conversion → BLS key generation → EpochBLSData creation.
*   Action: Ensure test validates error handling and logging verification.
*   Action: Run test to confirm inference → BLS integration works end-to-end before proceeding to dealing phase.
*   Files: `x/inference/module/end_to_end_test.go` (new file).
*   Status: ✅ **COMPLETED** - Created comprehensive end-to-end integration tests that validate complete inference → BLS integration:
    *   `TestCompleteEpochTransitionWithBLS`: Tests complete BLS integration flow with WorkerPublicKey validation
    *   `TestBLSIntegrationWithMissingParticipants`: Tests error handling for missing participants from store
    *   `TestBLSIntegrationWithInvalidWorkerKeys`: Tests error handling for invalid base64 WorkerPublicKeys
    *   Tests explicitly verify WorkerPublicKey usage (not ValidatorKey) with proper key type validation
    *   Comprehensive error scenarios with graceful failure handling and proper logging
    *   All 373 chain tests pass, confirming integration works without regressions

## IV. Step 2: Dealing Phase

### IV.1 [ ] Proto Definition (`bls` module): `DealerPart` and `MsgSubmitDealerPart`
*   Action: Define `DealerPart` (for storage) and `MsgSubmitDealerPart` Protobuf messages.
*   `EncryptedSharesForParticipant`: (deterministic shares for one participant)
    *   `encrypted_shares` (repeated bytes) // Index i = share for slot (participant.slot_start_index + i)
*   `DealerPartStorage`: (storage format using direct indexing)
    *   `dealer_address` (string)
    *   `commitments` (repeated bytes) // G2 points `C_kj = g * a_kj`
    *   `participant_shares` (repeated EncryptedSharesForParticipant) // Index i = shares for participants[i] from EpochBLSData
*   `MsgSubmitDealerPart`: (message format using direct indexing)
    *   `creator` (string, dealer's address)
    *   `epoch_id` (uint64)
    *   `commitments` (repeated bytes) // G2 points
    *   `encrypted_shares_for_participants` (repeated EncryptedSharesForParticipant) // Index i = shares for participants[i] in EpochBLSData order
*   Files: `proto/bls/tx.proto`, `proto/bls/types.proto`, `x/bls/types/tx.pb.go`, `x/bls/types/types.pb.go`
*   Important: Both message and storage use direct array indexing where index i corresponds to `EpochBLSData.participants[i]`. No address lookups or sorting needed.

### IV.2 [ ] Proto Definition (`bls` module): `EventDealerPartSubmitted`
*   Action: Define `EventDealerPartSubmitted` Protobuf message.
*   Fields: `epoch_id` (uint64), `dealer_address` (string).
*   Files: `proto/bls/events.proto`, `x/bls/types/events.pb.go`

### IV.3 [ ] Controller-Side Logic (`decentralized-api`): Dealing
*   Action: Implement logic for a controller to act as a dealer.
*   Location: `decentralized-api/internal/bls_dkg/dealer.go` (new package/file).
*   Logic:
    *   Listen for `EventKeyGenerationInitiated` from the `bls` module via chain event listener.
    *   If the controller is a participant in the DKG for `epoch_id`:
        *   Parse `I_total_slots`, `t_slots_degree`, and the list of all participants with their slot ranges and secp256k1 public keys.
        *   Generate its secret BLS polynomial `Poly_k(x)` of degree `t_slots_degree`. (Requires BLS library).
        *   Compute public commitments to coefficients (`C_kj = g * a_kj`, G2 points).
        *   For each *other* participating controller `P_m` (and their slot range `[start_m, end_m]`):
            *   For each slot index `i` in `P_m`'s range:
                *   Compute scalar share `share_ki = Poly_k(i)`.
                *   Encrypt `share_ki` using `P_m`'s secp256k1 public key with ECIES (Elliptic Curve Integrated Encryption Scheme). This involves:
                    *   Generate an ephemeral key pair
                    *   Perform ECDH key agreement
                    *   Derive a symmetric key
                    *   Encrypt the share using the derived key
                *   The resulting `encrypted_share_ki_for_m` contains both the ephemeral public key and the encrypted data.
        *   Construct `MsgSubmitDealerPart` with commitments and all encrypted shares in participant order.
        *   Create `encrypted_shares_for_participants` array with length = len(participants).
        *   For each participant at index i, compute and store their shares at `encrypted_shares_for_participants[i]`.
        *   Submit `MsgSubmitDealerPart` to the `bls` module via `cosmosClient`.
*   Files: `decentralized-api/internal/bls_dkg/dealer.go`, `decentralized-api/internal/event_listener/listener.go`, `decentralized-api/internal/cosmos/client.go` (add method to send `MsgSubmitDealerPart`).

### IV.4 [ ] Chain-Side Handler (`bls` module): `SubmitDealerPart` in `msg_server.go`
*   Action: Implement the gRPC message handler for `MsgSubmitDealerPart`.
*   Location: `x/bls/keeper/msg_server_dealer.go`.
*   Logic:
    *   Retrieve `EpochBLSData` for `msg.epoch_id`.
    *   Verify:
        *   Sender (`msg.creator`) is a registered participant for this DKG round in `EpochBLSData`.
        *   Current DKG phase is `DEALING`.
        *   Current block height is before `dealing_phase_deadline_block`.
        *   Dealer has not submitted their part already.
    *   Find the participant index in `EpochBLSData.participants` array for `msg.creator`.
    *   Convert `MsgSubmitDealerPart` to `DealerPartStorage` format:
        *   Verify array length: `len(msg.encrypted_shares_for_participants) == len(EpochBLSData.participants)`.
        *   Direct copy: `participant_shares = msg.encrypted_shares_for_participants` (indices already match).
    *   Store `DealerPartStorage` into `EpochBLSData.dealer_parts[participant_index]` (array position matching participant order).
    *   Emit `EventDealerPartSubmitted`.
*   Files: `x/bls/keeper/msg_server_dealer.go`.
*   Important: Message and storage use identical array indexing. Conversion is a simple array copy with length validation. No address lookups or sorting required.

### IV.5 [ ] Test
*   Action: Controller: Unit tests for polynomial generation, commitment calculation, share encryption, and `MsgSubmitDealerPart` construction. (Mock BLS and ECIES libraries).
*   Action: Chain: Unit tests for `SubmitDealerPart` handler (validations, data storage, event emission).
*   Action: Integration Test: A controller (as dealer) listens for `EventKeyGenerationInitiated`, prepares, and submits `MsgSubmitDealerPart`. Chain validates and stores it. Check `EpochBLSData` on chain.
*   Action: Run tests.

## V. Step 3: Transition to Verification Phase (On-Chain `bls` module)

### V.1 [ ] Proto Definition (`bls` module): `EventVerifyingPhaseStarted`
*   Action: Define `EventVerifyingPhaseStarted` Protobuf message.
*   Fields: `epoch_id` (uint64), `verifying_phase_deadline_block` (uint64).
*   Files: `proto/bls/events.proto`, `x/bls/types/events.pb.go`

### V.2 [ ] Proto Definition (`bls` module): `EventDKGFailed`
*   Action: Define `EventDKGFailed` Protobuf message.
*   Fields: `epoch_id` (uint64), `reason` (string).
*   Files: `proto/bls/events.proto`, `x/bls/types/events.pb.go`

### V.3 [ ] Chain-Side Logic (`bls` module): `EndBlocker` for Phase Transition
*   Action: Implement `EndBlocker` logic in `x/bls/abci.go` (or `module.go`).
*   Function: `TransitionToVerifyingPhase(ctx sdk.Context, epochBLSData types.EpochBLSData)` (called internally from EndBlocker).
*   Logic (in `EndBlocker`):
    *   Iterate through active DKGs (e.g., `EpochBLSData` not `COMPLETED` or `FAILED`).
    *   If DKG is in `DEALING` phase and `current_block_height >= dealing_phase_deadline_block`:
        *   Call `TransitionToVerifyingPhase`.
        *   Inside `TransitionToVerifyingPhase`:
            *   Calculate total number of slots covered by participants who successfully submitted `MsgSubmitDealerPart` (iterate through `EpochBLSData.dealer_parts` and sum slot ranges of their original `BLSParticipantInfo`).
            *   If `sum_covered_slots > EpochBLSData.i_total_slots / 2`:
                *   Update `EpochBLSData.dkg_phase` to `VERIFYING`.
                *   Set `EpochBLSData.verifying_phase_deadline_block` (current block + configured verification duration).
                *   Store updated `EpochBLSData`.
                *   Emit `EventVerifyingPhaseStarted`.
                *   (Optional: Mark dealers who didn't submit as non-participating if not already handled by lack of entry in `dealer_parts`).
            *   Else (not enough participation):
                *   Update `EpochBLSData.dkg_phase` to `FAILED`.
                *   Store updated `EpochBLSData`.
                *   Emit `EventDKGFailed` (reason: "Insufficient participation in dealing phase").
*   Files: `x/bls/abci.go`, `x/bls/keeper/phase_transitions.go` (for the helper function).

### V.4 [ ] Test
*   Action: Unit tests for `TransitionToVerifyingPhase` logic:
    *   Correct deadline check.
    *   Correct calculation of slot coverage.
    *   Correct phase transition to `VERIFYING` and event emission.
    *   Correct phase transition to `FAILED` and event emission.
    *   Test edge cases (e.g., exact deadline, just over/under participation threshold).
*   Action: Simulate chain progression in tests to trigger `EndBlocker`.
*   Action: Run tests.

## VI. Step 4: Verification Phase

### VI.1 [ ] Proto Definition (`bls` module): `QueryAllDealerParts`
*   Action: Define gRPC query for fetching all dealer parts.
*   Request: `QueryAllDealerPartsRequest` { `epoch_id` (uint64) }
*   Response: `QueryAllDealerPartsResponse` { `dealer_parts` (repeated `DealerPartStorage`) } // Use the `DealerPartStorage` defined earlier.
*   Files: `proto/bls/query.proto`, `x/bls/types/query.pb.go`

### VI.2 [ ] Chain-Side Querier (`bls` module): Implement `AllDealerParts`
*   Action: Implement the `AllDealerParts` gRPC querier method.
*   Location: `x/bls/keeper/query_dealer_parts.go`.
*   Logic:
    *   Retrieve `EpochBLSData` for `request.epoch_id`.
    *   Return all entries from `EpochBLSData.dealer_parts`.
*   Files: `x/bls/keeper/query_dealer_parts.go`.

### VI.3 [ ] Proto Definition (`bls` module): `MsgSubmitVerificationVector`
*   Action: Define `MsgSubmitVerificationVector`.
*   Fields: `creator` (string, participant's address), `epoch_id` (uint64). (No actual vector data needs to be on chain, just confirmation).
*   Files: `proto/bls/tx.proto`, `x/bls/types/tx.pb.go`

### VI.4 [ ] Proto Definition (`bls` module): `EventVerificationVectorSubmitted`
*   Action: Define `EventVerificationVectorSubmitted` Protobuf message.
*   Fields: `epoch_id` (uint64), `participant_address` (string).
*   Files: `proto/bls/events.proto`, `x/bls/types/events.pb.go`

### VI.5 [ ] Controller-Side Logic (`decentralized-api`): Verification
*   Action: Implement logic for a controller to verify shares and reconstruct its slot secrets.
*   Location: `decentralized-api/internal/bls_dkg/verifier.go` (new package/file).
*   Logic:
    *   Listen for `EventVerifyingPhaseStarted` or query DKG phase state for `epoch_id`.
    *   If in `VERIFYING` phase and the controller is a participant:
        *   Query the chain for all dealer parts: `blsQueryClient.AllDealerParts(epoch_id)`.
        *   For each slot index `i` in its *own* assigned slot range `[start_m, end_m]`:
            *   Initialize its slot secret share `s_i = 0` (scalar).
            *   For each dealer `P_k` whose parts were successfully submitted (from query response):
                *   Retrieve `P_k`'s commitments (`C_kj`).
                *   Find the encrypted share `encrypted_share_ki_for_m` that `P_k` made for slot `i` intended for this controller `P_m`:
                    *   Find this controller's index in `EpochBLSData.participants`.
                    *   Access `P_k.participant_shares[controller_index].encrypted_shares` array.
                    *   Calculate array index: `slot_offset = i - controller.slot_start_index`.
                    *   Get share: `encrypted_share_ki_for_m = encrypted_shares[slot_offset]`.
                *   Decrypt `encrypted_share_ki_for_m` using its own secp256k1 private key with ECIES:
                    *   Extract the ephemeral public key from the encrypted data
                    *   Perform ECDH key agreement
                    *   Derive the same symmetric key
                    *   Decrypt the share using the derived key
                *   This yields the original scalar share `share_ki`.
                *   Verify `share_ki` against `P_k`'s public polynomial commitments (i.e., check `g_scalar_mult(share_ki) == eval_poly_commitments(i, C_kj)`). (Requires BLS library).
                *   If valid, add to its slot secret share: `s_i = (s_i + share_ki) mod q` (where `q` is the BLS scalar field order).
            *   Store the final secret share `s_i` for slot `i` locally (e.g., in memory or secure storage).
        *   After processing all its assigned slots, if all successful, construct and submit `MsgSubmitVerificationVector` to the `bls` module.
*   Files: `decentralized-api/internal/bls_dkg/verifier.go`, `decentralized-api/internal/cosmos/query_client.go` (add method for `AllDealerParts` query), `decentralized-api/internal/cosmos/client.go` (add method to send `MsgSubmitVerificationVector`).

### VI.6 [ ] Chain-Side Handler (`bls` module): `SubmitVerificationVector` in `msg_server.go`
*   Action: Implement the gRPC message handler for `MsgSubmitVerificationVector`.
*   Location: `x/bls/keeper/msg_server_verifier.go`.
*   Logic:
    *   Retrieve `EpochBLSData` for `msg.epoch_id`.
    *   Verify:
        *   Sender (`msg.creator`) is a registered participant for this DKG round.
        *   Current DKG phase is `VERIFYING`.
        *   Current block height is before `verifying_phase_deadline_block`.
        *   Participant has not submitted their vector already.
    *   Add `msg.creator` to `EpochBLSData.verification_vectors_submitters`.
    *   Store updated `EpochBLSData`.
    *   Emit `EventVerificationVectorSubmitted`.
*   Files: `x/bls/keeper/msg_server_verifier.go`.

### VI.7 [ ] Test
*   Action: Chain: Unit test for `AllDealerParts` querier.
*   Action: Chain: Unit test for `SubmitVerificationVector` handler (validations, data storage, event emission).
*   Action: Controller: Unit tests for share decryption, verification against commitments, and aggregation of slot secrets. (Mock BLS and ECIES).
*   Action: Integration Test: Controller queries dealer parts, performs verification, computes its shares, and submits `MsgSubmitVerificationVector`. Chain validates and records it.
*   Action: Run tests.

## VII. Step 5: Group Public Key Computation & Completion (On-Chain `bls` module)

### VII.1 [ ] Proto Definition (`bls` module): `EventGroupPublicKeyGenerated`
*   Action: Define `EventGroupPublicKeyGenerated` Protobuf message.
*   Fields: `epoch_id` (uint64), `group_public_key` (bytes, G2 point), `i_total_slots` (uint32), `t_slots_degree` (uint32).
*   Files: `proto/bls/events.proto`, `x/bls/types/events.pb.go` (already defined if done for controller post-DKG earlier, ensure consistency).

### VII.2 [ ] Chain-Side Logic (`bls` module): `EndBlocker` for DKG Completion
*   Action: Extend `EndBlocker` logic in `x/bls/abci.go`.
*   Function: `CompleteDKG(ctx sdk.Context, epochBLSData types.EpochBLSData)` (called internally from EndBlocker).
*   Logic (in `EndBlocker`):
    *   Iterate through active DKGs.
    *   If DKG is in `VERIFYING` phase and `current_block_height >= verifying_phase_deadline_block`:
        *   Call `CompleteDKG`.
        *   Inside `CompleteDKG`:
            *   Calculate total number of slots covered by actual validator participants who successfully submitted `MsgSubmitVerificationVector`. (Iterate through `EpochBLSData.verification_vectors_submitters`, get their original `BLSParticipantInfo` and sum their slot ranges).
            *   If `sum_covered_slots_verified > EpochBLSData.i_total_slots / 2`:
                *   Initialize `GroupPublicKey` as identity G2 point.
                *   Retrieve the `C_k0` commitment (first commitment, `g * a_k0`) from each dealer `P_k` in `EpochBLSData.dealer_parts` (ensure these dealers were part of the successful set if there was a filter step).
                *   Aggregate these: `GroupPublicKey = sum(C_k0)` (G2 point addition). (Requires BLS library).
                *   Store computed `GroupPublicKey` in `EpochBLSData.group_public_key`.
                *   Update `EpochBLSData.dkg_phase` to `COMPLETED`.
                *   Store updated `EpochBLSData`.
                *   Emit `EventGroupPublicKeyGenerated`.
            *   Else (not enough verification):
                *   Update `EpochBLSData.dkg_phase` to `FAILED`.
                *   Store updated `EpochBLSData`.
                *   Emit `EventDKGFailed` (reason: "Insufficient participation in verification phase").
*   Files: `x/bls/abci.go`, `x/bls/keeper/phase_transitions.go` (for `CompleteDKG`).

### VII.3 [ ] Test
*   Action: Unit tests for `CompleteDKG` logic:
    *   Correct deadline check.
    *   Correct calculation of verified slot coverage.
    *   Correct aggregation of `C_k0` commitments to form `GroupPublicKey`.
    *   Correct phase transition to `COMPLETED` and event emission.
    *   Correct phase transition to `FAILED` and event emission.
*   Action: Simulate chain progression in tests to trigger `EndBlocker` for DKG completion.
*   Action: Run tests.

## VIII. Step 6: Controller Post-DKG Operations

### VIII.1 [ ] Controller-Side Logic (`decentralized-api`): Storing DKG Results
*   Action: Implement logic for a controller to finalize its DKG state.
*   Location: `decentralized-api/internal/bls_dkg/manager.go` (or similar).
*   Logic:
    *   Listen for `EventGroupPublicKeyGenerated` for the relevant `epoch_id`.
    *   Retrieve and store the `GroupPublicKey`, `I_total_slots`, `t_slots_degree` from the event.
    *   The controller should already have its set of private BLS slot shares `{s_i}` for its assigned slots from Step VI.5. Ensure these are securely stored and associated with the `epoch_id` and `GroupPublicKey`.
    *   The controller is now ready for threshold signing for this epoch.
*   Files: `decentralized-api/internal/bls_dkg/manager.go`, `decentralized-api/internal/event_listener/listener.go`.

### VIII.2 [ ] Test
*   Action: Controller: Unit tests for handling `EventGroupPublicKeyGenerated` and correctly associating its local slot shares with the group public key and epoch details.
*   Action: Consider an end-to-end test scenario involving multiple controllers, a full DKG cycle, and verification that each participating controller has the correct group public key and its respective private shares. This is a larger integration effort.
*   Action: Run tests.

## IX. General Considerations & Libraries

### IX.1 [ ] BLS Cryptography Library
*   Action: Choose and integrate a robust Go library for BLS12-381 operations.
*   Examples: `herumi/bls`, `kilic/bls12-381`, `ConsenSys/gnark-crypto/ecc/bls12-381`.
*   Considerations: Ethereum compatibility for G2 points (public keys, commitments) and G1 points (signatures, if applicable later). Scalar field operations.

### IX.2 [ ] secp256k1 Key Usage
*   Action: Ensure proper integration with the existing secp256k1 key infrastructure.
*   Considerations: 
    *   Use the existing key management system for encryption/decryption operations
    *   Implement ECIES (Elliptic Curve Integrated Encryption Scheme) for share encryption/decryption
    *   Ensure consistent ECIES parameters across all participants (e.g., KDF, symmetric cipher, MAC)
    *   Consider using a standardized ECIES implementation (e.g., from a well-audited library)

### IX.3 [ ] Error Handling and Logging
*   Action: Implement comprehensive error handling and logging throughout the new module and controller logic.

### IX.4 [ ] Gas Costs
*   Action: Be mindful of gas costs for on-chain operations, especially storage and complex computations. Optimize where possible.

### IX.5 [ ] Security
*   Action: Ensure private keys (secp256k1 private keys, BLS polynomial coefficients, final slot shares) are handled securely on the controller side.
*   Action: Thoroughly review cryptographic operations for correctness.

This plan provides a structured approach. Each major step includes development tasks for proto definitions, chain-side logic (keepers, message handlers, queriers, EndBlocker), controller-side logic, and testing. Remember to iterate and refine as development progresses.

## X. Deterministic Storage Considerations

### X.1 Map Remediation for Consensus Safety
*   **Issue**: Golang maps have non-deterministic iteration order, which can cause consensus failures when stored in blockchain state.
*   **Solution**: All data structures stored in state use deterministic `repeated` arrays instead of `map` fields.
*   **Implementation**:
    *   `EncryptedSharesForParticipant.encrypted_shares`: Changed from `map<uint32, bytes>` to `repeated bytes` where index i = share for slot (participant.slot_start_index + i).
    *   `DealerPartStorage.participant_shares`: Changed from `map<string, EncryptedSharesForParticipant>` to `repeated EncryptedSharesForParticipant` with direct indexing where `participant_shares[i]` contains shares for `EpochBLSData.participants[i]`.
    *   `EpochBLSData.dealer_parts`: Uses `repeated DealerPartStorage` with array indexing matching participant order.
*   **Controller Logic**: Ensures deterministic message construction by sorting arrays before submission.
*   **Chain Logic**: Converts incoming messages to deterministic storage format using direct array indexing before persisting to state. 