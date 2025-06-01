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
*   Note: ✅ Handler verified working correctly - authenticates sender via `msg.GetCreator()` and stores secp256k1 public key via `ValidatorKey: msg.GetValidatorKey()` in the `Participant` struct.

### II.3 [x] Controller-Side (`decentralized-api`): Use Existing secp256k1 Key
*   Action: Ensure the controller uses its existing secp256k1 key for DKG operations.
*   Logic: When gathering data for `MsgSubmitNewParticipant`, use the existing secp256k1 public key.
*   Files: `decentralized-api/participant_registration/participant_registration.go`
*   Note: ✅ Controller verified working correctly - uses `getValidatorKey()` to retrieve secp256k1 public key from Tendermint RPC (`result.ValidatorInfo.PubKey`) and properly encodes it as `ValidatorKey` field in both `registerGenesisParticipant()` and `registerJoiningParticipant()` functions.

### II.4 [x] Test
*   Action: Create unit tests for the `SubmitNewParticipant` message handler in the `inference` module.
*   Action: Create integration tests where a controller registers using its secp256k1 key and verify chain state.
*   Action: Test the controller's key usage in DKG operations.
*   Note: ✅ Enhanced existing tests in `msg_server_submit_new_participant_test.go` with comprehensive secp256k1 key testing:
    *   `TestMsgServer_SubmitNewParticipant`: Tests full participant creation with valid secp256k1 ValidatorKey and WorkerKey
    *   `TestMsgServer_SubmitNewParticipant_WithEmptyKeys`: Tests graceful handling of empty keys
    *   `TestMsgServer_SubmitNewParticipant_ValidateSecp256k1Key`: Tests secp256k1 key validation, storage, and reconstruction
    *   All 359 chain tests still pass, confirming no regressions were introduced.
*   Note: ✅ Controller tests verified: All 56 decentralized-api tests pass via `make api-test`, including participant registration functionality that uses `getValidatorKey()` to retrieve and encode secp256k1 keys for DKG operations.

## III. Step 1: DKG Initiation (On-Chain `bls` and `inference` modules)

### III.1 [ ] Proto Definition (`bls` module): `EpochBLSData`
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
    *   `dealer_parts` (map<string, DealerPartStorage>) // dealer_address -> DealerPartStorage
    *   `verification_vectors_submitters` (repeated string) // list of addresses who submitted verification vectors
*   Files: `proto/bls/types.proto`, `x/bls/types/types.pb.go`

### III.2 [ ] Proto Definition (`bls` module): `EventKeyGenerationInitiated`
*   Action: Define `EventKeyGenerationInitiated` Protobuf message for events.
*   Fields: `epoch_id` (uint64), `i_total_slots` (uint32), `t_slots_degree` (uint32), `participants` (repeated `BLSParticipantInfo`).
*   Files: `proto/bls/events.proto`, `x/bls/types/events.pb.go`

### III.3 [ ] `bls` Module Keeper: `InitiateKeyGenerationForEpoch` Function
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

### III.4 [ ] `inference` Module Modification: Call `InitiateKeyGenerationForEpoch`
*   Action: In the `inference` module's `EndBlock` logic, after `onSetNewValidatorsStage` successfully completes.
*   Logic:
    *   Gather the `finalized_validator_set_with_weights` (including their secp256k1 public keys, fetched from `Participant` data).
    *   Make an internal call to `blsKeeper.InitiateKeyGenerationForEpoch(ctx, nextEpochID, finalized_validator_set_with_weights_and_keys)`.
*   Files: `x/inference/module/module.go` (or where `EndBlock` logic resides), `x/inference/keeper/keeper.go` (to add dependency on `blsKeeper`).

### III.5 [ ] Test
*   Action: Unit tests for `blsKeeper.InitiateKeyGenerationForEpoch`:
    *   Correct slot assignment logic for various participant weights.
    *   Correct `EpochBLSData` creation and storage.
    *   Correct event emission.
    *   Deadline calculation.
*   Action: Integration test to ensure `inference` module correctly calls `blsKeeper.InitiateKeyGenerationForEpoch` with the right data at the right time.
*   Action: Run tests using `go test ./...` in the chain's root.

## IV. Step 2: Dealing Phase

### IV.1 [ ] Proto Definition (`bls` module): `DealerPart` and `MsgSubmitDealerPart`
*   Action: Define `DealerPart` (for storage) and `MsgSubmitDealerPart` Protobuf messages.
*   `DealerPartStorage`:
    *   `dealer_address` (string)
    *   `commitments` (repeated bytes) // G2 points `C_kj = g * a_kj`
    *   `encrypted_shares_map` (map<string, EncryptedSharesForParticipant>) // participant_address -> EncryptedSharesForParticipant
        *   `EncryptedSharesForParticipant`: `shares` (map<uint32, bytes>) // slot_index -> encrypted_share_ki_for_m
*   `MsgSubmitDealerPart`:
    *   `creator` (string, dealer's address)
    *   `epoch_id` (uint64)
    *   `commitments` (repeated bytes) // G2 points
    *   `encrypted_shares_for_participants` (repeated `TargettedEncryptedShares`)
        *   `TargettedEncryptedShares`: `target_participant_address` (string), `shares_for_slots` (map<uint32, bytes>) // slot_index -> encrypted_share_ki_for_m
*   Files: `proto/bls/tx.proto`, `proto/bls/types.proto`, `x/bls/types/tx.pb.go`, `x/bls/types/types.pb.go`

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
        *   Construct `MsgSubmitDealerPart` with commitments and all encrypted shares, structured per participant.
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
    *   Store `DealerPartStorage` (commitments and structured encrypted shares) into `EpochBLSData.dealer_parts[msg.creator]`.
    *   Emit `EventDealerPartSubmitted`.
*   Files: `x/bls/keeper/msg_server_dealer.go`.

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
*   Action: Implement `EndBlocker` logic in `x/bls/abci.go`