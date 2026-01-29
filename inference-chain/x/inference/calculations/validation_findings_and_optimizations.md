# Validation: Findings and Optimizations

This document summarizes security findings, fixes, and performance optimizations applied to the inference validation flow (including revalidation and invalidation throttling).

---

## 1. Caching: EpochData and Seeds

### Implemented

- **EpochGroupData cache**  
  A hot cache for `EpochGroupData` keyed by `(epochIndex, modelId)`:
  - **Scope:** Current and previous effective epoch only. Other epochs are not cached.
  - **Lifecycle:** Cache is refreshed when the effective epoch changes (`SetEffectiveEpochIndex`). The old “previous” epoch is dropped; the old “current” becomes the new “previous.”
  - **Write-through:** Updates for current/previous epoch go to cache; current-epoch cache is flushed to storage in EndBlock (`FlushCurrentEpochGroupCache`).
  - **Relevance:** Addresses the issue where repeated reads of epoch group data for the same epoch/model were hitting storage on every validation path.

- **RandomSeed cache**  
  A warm cache for participant seeds per epoch:
  - **Scope:** Current effective epoch only.
  - **Lifecycle:** Cleared and re-initialized when the effective epoch changes (`refreshRandomSeedCache`).
  - **Usage:** `GetRandomSeed` / `GetParticipantEpochSeed` use the cache so that `calculations.ShouldValidate` can be run without extra storage reads for seeds in the current epoch.

### Planned extension

- **Revalidation vote weights**  
  The cache layer will be extended with structures that hold revalidation vote weights. These structures will be scoped by a fixed **N blocks** (time-bound), not by epoch, and will be cleared after N blocks to avoid unbounded growth.

---

## 2. Moving `addInferenceToEpochGroupValidations` After Checks

### Problem

Previously, `addInferenceToEpochGroupValidations` was invoked **before** the main validation checks in `Validation`. That caused possible load:

1. **Spam / griefing:** A non-participant (or any account) could send a `MsgValidation` for an inference.
2. **Early storage write:** `addInferenceToEpochGroupValidations` created the inference validation record in chain storage (relatively heavy operation).

### Fix

`addInferenceToEpochGroupValidations` is now called **only after** all of the following have passed:

- Participant and inference existence and status checks
- Executor and model checks
- Epoch group and group data checks
- **Designated-validator check** (see Section 3)
- For non-revalidation: only then is the inference added to epoch group validations

So storage is only updated when the sender is allowed to validate and other preconditions hold. Spam messages no longer create persistent records that block legitimate validators.

---

## 3. Designated-Validator Check (Eligibility to Validate)

### Problem

There was no check that the sender of a validation or revalidation message was the **designated validator** for that inference (as determined by the PoC logic). That enabled:

1. **Free-riding:** Any participant could send validation messages for inferences they were not assigned to validate, get them accepted, and share rewards without doing the work.
2. **Sybil / strike evasion:** An attacker with multiple addresses could have one address execute the inference and others “validate” it. Invalid inferences could be “validated” by colluding addresses and the executor could avoid being striked.
3. **Revalidation DoS:** Any participant could trigger the revalidation procedure. Because revalidation is computationally costly, this could be used as a DoS vector.

### Fix

Before performing validation or revalidation, the handler now:

1. Loads **inference validation details** (epoch, inference id, total power, executor power, etc.) and the **participant’s seed** for that epoch (using the seed cache when possible).
2. Calls **`calculations.ShouldValidate(participantSeed, inferenceDetails, totalWeight, validatorPower, executorPower, params, false)`** to determine if this participant is the designated validator for this inference.
3. If `ShouldValidate` returns false, the message is rejected with **`types.ErrNotDesignatedValidator`** and no state change is applied (no validation record, no revalidation).

So only the participant selected by the deterministic PoC logic can validate or trigger revalidation for a given inference.

### Proposal: Strike for Ineligible Validation/Revalidation

**Recommendation:** Treat submission of validation or revalidation messages by a participant who is **not** the designated validator as misbehavior and **strike** that participant (e.g. reputation penalty or slashing, according to chain policy). This would:

- Discourage probing or abuse once the eligibility check is in place.
- Align incentives with the design (only the designated validator should ever send such messages).

Implementation would hook into the existing strike/slashing machinery and trigger when `ShouldValidate` is false for the sender of a `MsgValidation` (including revalidation).

---

## 4. Tiny Performance Optimizations

- **EpochGroupData:** Repeated reads for the same `(epochIndex, modelId)` in the validation path use the EpochGroupData cache instead of storage.
- **Seeds:** Participant seed for the current epoch is served from the RandomSeed cache when available, reducing storage reads in `GetParticipantEpochSeed` used by `ShouldValidate`.
- **Invalidations:** Existing TODOs note possible caching for `CountInvalidations` and for `GetSummaryByModelAndTime` to further reduce work in the invalidation-throttling path; these can be addressed in follow-up changes.

---

## Summary

| Area | Change | Purpose |
|------|--------|--------|
| Caches | EpochGroupData + RandomSeed caches (current/previous epoch) | Fewer storage reads; EpochData cache tied to the related issue. |
| Ordering | `addInferenceToEpochGroupValidations` after all checks | Prevents spam from creating validation records and blocking legitimate validators. |
| Eligibility | `ShouldValidate` before accepting validation/revalidation | Stops free-riding, Sybil/strike evasion, and revalidation DoS. |
| Proposal | Strike when sender is not designated validator | Deter abuse and align with PoC. |
| Perf | Cache usage in validation path + future invalidation caches | Lower latency and load. |
