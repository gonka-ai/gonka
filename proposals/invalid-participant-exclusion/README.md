# [IMPLEMENTED]: Invalid Participant Exclusion – Feature Specification

## **Overview**

This feature refines, fixes and fully tests the mechanism for handling **invalid participants** in the Gonka network. Invalid participants are nodes that have misbehaved (e.g., submitted bad inferences, misconfigured models, attempted cheating, or failed other behavioral criteria). The goal is to ensure they are **excluded from all network responsibilities and consensus mechanisms**, without retroactively altering cryptographically signed data.

---

## **Problem Statement**

Currently, the list of **active participants** retrieved from the chain **could include nodes that are technically invalid** for the current epoch. This list is **signed and committed cryptographically** each epoch, making it immutable and essential for trust and traceability via Merkle proofs.

However, since some participants may be no longer trustworthy (due to detected invalid behavior during the epoch), relying solely on the active list is not sufficient for selecting endpoints to use.

Additionally, when a participant is marked as invalid, we need to ensure and test that they are excluded from:
* Task assignment (inference or validation)
* Voting weight calculation
* Consensus power allocation
* Inference routing via the decentralized API (DAPI)
* Model group membership logic (EpochGroup)
* Clients selecting transfer agents

---

## **Proposed Solution**

### 1. **Introduce a New Query and data structure: `ExcludedParticipants`**

* A new chain query will return a list of **excluded participants for the current epoch** only.
* This query will include:
    * Participant identifier
    * EffectiveHeight — the block height at which the participant was added to the exclusion list
    * Epoch index for when they are excluded
    * Reason for exclusion (e.g., invalidation due to bad inference, wrong model, configuration issue; maintenance; operator request)
* No cryptographic proof is necessary (for now) as it’s only relevant to the current epoch and used for filtering.
* The list will be added to whenever a participant is added to the exclusion list
* There should be no need for specific pruning
* There should be no write access to the list via queries or other endpoints.

### 2. **Update DAPI Logic to Respect Invalid Participants**

* When querying for active participants via the DAPI:

    * Return a separate top-level field named `invalidatedParticipants` containing the list of all invalidated participants for the current epoch.
    * Do not add per-participant "invalidated" flags to the active participants list.
    * Clients must use this top-level list to filter invalidated participants from the cryptographically secured active list at selection time.
    * (We cannot mutate the cryptographically secured list itself.)

### 3. **Recursive Removal from All Model Group Memberships**

* An invalidated participant must be **removed from all models they serve**, not just the model they were invalidated for.
* Treat invalidation as a **global disqualification** from participation for the epoch.

### 4. **Ensure Invalidated Participants Have No Voting or Consensus Power**

* Only participants whose exclusion reason is "invalidated" are stripped of consensus-related influence
    * No voting rights in governance
    * No consensus power in Tendermint
* Participants excluded for other reasons remain with their voting and consensus power intact

---

## **Testing & Validation Plan**

* The invalidation mechanism was previously disabled during development and was under tested. Now that the full behavior is enabled:

    * Ensure that participants are:
        * Properly listed in the `ExcludedParticipants` query with `EffectiveHeight` and `reason`
        * Reflected by the DAPI as a top-level `invalidatedParticipants` list for the current epoch
        * For invalidated participants only:
            * Excluded from tasks, routing, and all model group memberships
            * Not receiving rewards, work, or assignments
            * Removed from voting and consensus mechanisms
    * Ensure that participants excluded for non-invalidation reasons retain voting and consensus power
* Extend **Testermint** tests to cover these scenarios

---

## **Client & Consumer Requirements**

* All example clients (and production consumers) must:
    * Use the top-level `invalidatedParticipants` list from the DAPI GetActiveParticipants response to filter out invalidated participants when selecting an endpoint

---

## **Terminology Clarification**

* **Excluded Participant**: A participant present in the on-chain `ExcludedParticipants` list for the current epoch. Each entry includes a `reason` and an `EffectiveHeight` indicating when the exclusion took effect. Reasons can include invalidation (due to bad inference, wrong model, configuration issue), temporary maintenance, operator request, etc.
* **Invalidated Participant**: A subset of excluded participants that have been deemed untrustworthy for the current epoch due to failed validations, model misalignment, or malicious behavior. Only this subset loses voting rights and consensus power.
* **Active Participant**: A participant still cryptographically listed as active, but may need filtering at runtime if they’re invalidated.

