### BLS Key Generation Interaction Flow

This section outlines the step-by-step interaction for Distributed Key Generation (DKG) using the **BLS12-381 elliptic curve**, aiming for Ethereum-compatible BLS threshold signatures. This DKG establishes a system of **`I_total` slot shares** (e.g., `I_total = 100` for PoC). The DKG polynomial will have a degree `t_slots` such that `t_slots + 1` (e.g., `floor(I_total / 2) + 1`) distinct slot shares are required to reconstruct a secret/signature. This design ensures that a validly reconstructed signature inherently signifies participation equivalent to holding >50% of the total slots (which represent the total weight).

**Note on Timing:** All phase durations and deadlines in this flow are defined in **block numbers** (int64), not time durations. This follows the existing inference module pattern and ensures deterministic, consensus-based timing. For example, a dealing phase might last 5 blocks, meaning participants have until `current_block_height + 5` to submit their dealer parts.

Key cryptographic elements will adhere to common conventions for this scheme:
*   Each of the `I_total` slots `i` has an associated secret share `s_i` (scalar).
*   The group public key (`GroupPublicKey`) and dealer commitments to their primary secret (`C_k0 = g * a_k0`) are points in the **G2** group of BLS12-381. The generator `g` is the standard G2 generator.
*   (For subsequent signing operations, not detailed in this DKG plan, signatures would be points in G1 and would involve hashing messages to G1.)

**Pre-Step 0: Using Existing secp256k1 Keys for Encryption**

Before the DKG process for an epoch can commence, controllers must ensure their secp256k1 public keys are registered on-chain. These keys are used by dealers to encrypt individual key shares for other participants using ECIES.

*   **Initial Registration:** When a new controller registers itself as a participant on the chain (e.g., via `MsgSubmitNewParticipant`), it MUST include its secp256k1 public key. This key is already available from the participant's registration process.
*   **Key Usage:** The existing secp256k1 public key will be used for ECIES encryption of shares during the DKG process. No additional key generation is needed.

1.  **Validator Set Finalization, Slot Assignment & DKG Initiation (On-Chain):**
    *   **`inference` Module (`EndBlock` logic):**
        *   This process is anchored to the `inference` module's `EndBlock` logic. Specifically, it occurs after the `onSetNewValidatorsStage` function (or an equivalent procedure within the `EndBlock` routine) successfully completes for a given Proof-of-Concept (PoC) period.
        *   The completion of `onSetNewValidatorsStage` finalizes the active validator set for epoch `E_next`, providing a list of: `(participant_address, percentage_weight, secp256k1_public_key)`.
    *   **Triggering BLS Key Generation by `inference` Module:**
        *   Immediately after finalizing the validator set, the `inference` module makes an internal, trusted call to the `bls` module's keeper: `blsKeeper.InitiateKeyGenerationForEpoch(ctx, E_next, finalized_validator_set_with_weights)`.
        *   The `finalized_validator_set_with_weights` is the list of `(address, percentage_weight, secp256k1_pub_key)` tuples.
    *   **`bls` Module (`keeper.InitiateKeyGenerationForEpoch` method):**
        *   Receives the epoch ID (`E_next`) and the `finalized_validator_set_with_weights` from the `inference` module.
        *   **Authenticates the caller** to ensure it originates from a permitted source (e.g., the `inference` module).
        *   **Slot Assignment & DKG Parameterization (Internal to `bls` module):**
            *   The `bls` module uses its own configured parameters for `I_total` (e.g., 1000) and `t_slots` (e.g., 500, so `t_slots + 1 = 501` slot shares needed for signing).
            *   It performs a deterministic slot assignment: for each participant in `finalized_validator_set_with_weights`, it maps their `percentage_weight` to a specific range of slot indices `[start_idx, end_idx]` out of `I_total`. This algorithm must ensure all slots are assigned proportionally and without overlap.
        *   Initializes `EpochBLSData` for `E_next`, storing `I_total`, `t_slots`, the full participant list (including their original percentage weights, secp256k1_pub_keys), and their newly assigned slot ranges.
        *   Sets the DKG phase to `DEALING`.
        *   Establishes and records a deadline block height (e.g., `current_block_height + dealing_phase_duration_blocks`) for the `DEALING` phase.
        *   Emits `EventKeyGenerationInitiated`. This event includes the epoch ID, `I_total`, `t_slots`, and the list of participants with their assigned slot ranges and secp256k1 public keys, so controllers know the structure of the DKG.

2.  **Dealing Phase:**
    *   **Controller (each validator `P_k` for epoch `E_next`, acting as a dealer):**
        *   Listens for `EventKeyGenerationInitiated` to get `I_total`, `t_slots`, and the full list of participants (including their assigned slot ranges and secp256k1 public keys).
        *   Generates its secret BLS polynomial `Poly_k(x)` of degree `t_slots` (coefficients `a_kj` are scalars).
        *   Computes public commitments to `Poly_k(x)`'s coefficients (e.g., `C_kj = g * a_kj`, which are G2 points on BLS12-381).
        *   Prepares a collection of encrypted shares: For each *other* participating controller `P_m` (who is responsible for slot range `[start_m, end_m]`):
            *   For each slot index `i` in `P_m`'s range `[start_m, end_m]`:
                *   Computes the scalar share `share_ki = Poly_k(i)`.
                *   Encrypts `share_ki` using `P_m`'s secp256k1 public key with ECIES, creating `encrypted_share_ki_for_m`.
        *   **Client:** Submits `MsgSubmitDealerPart`. This message contains `P_k`'s public commitments (G2 points) and all the `encrypted_share_ki_for_m` values it generated, structured so that each participant `P_m` can later identify and retrieve the shares intended for each slot `i` it is responsible for.
    *   **Chain (`bls` module):**
        *   Receives `MsgSubmitDealerPart` from dealer `P_k`.
        *   Verifies the sender is an active validator for this DKG round, phase is `DEALING`, and it's within the deadline.
        *   Stores `P_k`'s commitments and its collection of encrypted slot shares in association with `EpochBLSData`.
        *   Emits `EventDealerPartSubmitted` (identifying dealer `P_k`).

3.  **Transition to Verification Phase:**
    *   **Chain (`bls` module - EndBlocker/Timed Logic):**
        *   When the `DEALING` phase deadline block height is reached (i.e., `current_block_height >= dealing_phase_deadline_block`):
            *   Calls `TransitionToVerifyingPhase`.
            *   Calculates the total number of slots covered by actual validator participants who successfully submitted `MsgSubmitDealerPart`.
            *   Checks if this sum of covered slots is `> I_total / 2`.
            *   If yes: Transitions `EpochBLSData.Phase` to `VERIFYING`, sets a new deadline block height (e.g., `current_block_height + verification_phase_duration_blocks`). Validators who did not submit a dealer part are marked as non-participating for this DKG and cannot proceed.
            *   If no: Marks the DKG process for epoch `E_next` as `FAILED`.

4.  **Verification Phase:**
    *   **Controller (each participating validator `P_m`, responsible for slot range `[start_m, end_m]` who successfully acted as a dealer or is otherwise still active):**
        *   Upon detecting the transition to the `VERIFYING` phase for epoch `E_next` (e.g., by listening for `EventVerifyingPhaseStarted` or querying phase state):
            *   **Queries the chain** (e.g., via `QueryAllDealerParts(epoch_id = E_next)` call to the `bls` module) to fetch all `MsgSubmitDealerPart` data (commitments and collections of encrypted slot shares) from all dealers (`P_k`) who successfully submitted in the Dealing Phase.
            *   For each slot index `i` in its *own* assigned range `[start_m, end_m]`:
                *   Initializes its slot secret share `s_i = 0` (scalar).
                *   For each dealer `P_k` whose parts were successfully submitted:
                    *   Retrieves `encrypted_share_ki_for_m` (the encrypted share dealer `P_k` made for slot `i` intended for `P_m`).
                    *   Decrypts it using its own secp256k1 private key with ECIES to get `share_ki` (scalar).
                    *   Verifies `share_ki` against `P_k`'s public polynomial commitments (i.e., check `g * share_ki == Poly_k(i)` using the commitments `C_kj`).
                    *   If valid, adds to its slot secret share: `s_i = (s_i + share_ki) mod q` (where `q` is the scalar field order).
                *   `P_m` now holds the final secret share `s_i` (a scalar) for slot `i`.
            *   After processing all its assigned slots, `P_m` has a set of secret slot shares: `{s_i | i in [start_m, end_m]}`.
            *   **Client:** Submits `MsgSubmitVerificationVector` (this confirms `P_m` successfully verified and reconstructed all secret shares for its assigned slots).
    *   **Chain (`bls` module):**
        *   Receives `MsgSubmitVerificationVector` from `P_m`.
        *   Verifies sender, phase (`VERIFYING`), deadline. Records that `P_m` has successfully verified its shares.
        *   Emits `EventVerificationVectorSubmitted` (for participant `P_m`).

5.  **Group Public Key Computation & Completion (On-Chain):**
    *   **Chain (`bls` module - EndBlocker/Timed Logic):**
        *   When the `VERIFYING` phase deadline block height is reached (i.e., `current_block_height >= verifying_phase_deadline_block`):
            *   Calculates the total number of slots covered by actual validator participants who successfully submitted `MsgSubmitVerificationVector`.
            *   Checks if this sum of covered slots by verifying participants is `> I_total / 2`.
            *   If yes:
                *   Retrieves the `C_k0` commitment (a G2 point, representing `g * a_k0`) from each dealer `P_k` who successfully submitted parts in the Dealing Phase.
                *   Aggregates these: `GroupPublicKey = sum(C_k0)` (sum over successful dealers `P_k`). This `GroupPublicKey` is a G2 point.
                *   Stores `GroupPublicKey` in `EpochBLSData`.
                *   Transitions `EpochBLSData.Phase` to `COMPLETED`.
                *   Emits `EventGroupPublicKeyGenerated`, including epoch ID, `GroupPublicKey` (G2 point), `I_total`, and `t_slots`.
            *   If no (not enough slot coverage by verifying participants): Marks DKG as `FAILED`.

6.  **Controller Post-DKG:**
    *   **Controller (validator `P_m` responsible for slots `[start_m, end_m]` who successfully completed verification):**
        *   Listens for `EventGroupPublicKeyGenerated`.
        *   Retrieves and stores `GroupPublicKey`, `I_total`, `t_slots`.
        *   Each controller `P_m` now possesses its set of private BLS slot shares `{s_i | i in [start_m, end_m]}` and the group's public key.
        *   They are ready to participate in threshold signing by providing partial signatures derived from each of their `s_i` when requested. A signature reconstructed from `t_slots + 1` distinct slot shares will be valid against `GroupPublicKey`.

This flow ensures that key material (for `I_total` slots) is generated collaboratively and verified, with clear transitions for actual participants. The use of `t_slots + 1` (e.g., >50% of `I_total`) for signing ensures the weighted property is cryptographically embedded. All timing is based on deterministic block heights rather than wall-clock time.