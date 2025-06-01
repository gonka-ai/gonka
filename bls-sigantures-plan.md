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

### II.1 [ ] Proto Definition (`inference` module): `MsgSubmitNewParticipant`
*   Action: Ensure the existing `MsgSubmitNewParticipant` message includes the secp256k1 public key.
*   Fields: `creator` (string, participant's address), `secp256k1_public_key` (bytes or string).
*   Files: `proto/inference/tx.proto`, `x/inference/types/tx.pb.go`

### II.2 [ ] Chain-Side Handler (`inference` module): Verify `SubmitNewParticipant`
*   Action: Ensure the handler for `MsgSubmitNewParticipant` properly stores the secp256k1 public key.
*   Logic:
    *   Authenticate sender (`creator`).
    *   Store participant data including the secp256k1 public key.
*   Files: `x/inference/keeper/msg_server_submit_new_participant.go`

### II.3 [ ] Controller-Side (`decentralized-api`): Use Existing secp256k1 Key
*   Action: Ensure the controller uses its existing secp256k1 key for DKG operations.
*   Logic: When gathering data for `MsgSubmitNewParticipant`, use the existing secp256k1 public key.
*   Files: `decentralized-api/participant_registration/participant_registration.go`

### II.4 [ ] Test
*   Action: Create unit tests for the `SubmitNewParticipant` message handler in the `inference` module.
*   Action: Create integration tests where a controller registers using its secp256k1 key and verify chain state.
*   Action: Test the controller's key usage in DKG operations.

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
                *   Find the encrypted share `encrypted_share_ki_for_m` that `P_k` made for slot `i` intended for this controller `P_m`.
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