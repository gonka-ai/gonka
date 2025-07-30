## Inference Validation Flow with Secret Seeds

### Overview of the Validation Process

The validation system uses a sophisticated secret seed mechanism to ensure fair and unpredictable validator assignment while maintaining verifiable accountability. Here's the complete flow:

### Phase 1: Secret Seed Generation and Private Decision Making

**Seed Generation:**
Each API node generates and maintains its own private random seed for each epoch using `createNewSeed` in `decentralized-api/internal/poc/random_seed.go`. This seed is kept secret in the local configuration and used for private decision-making.

**Private Validation Selection:**
When inferences complete, API nodes use their current private seed to determine which inferences they should validate:

1. **SampleInferenceToValidate**: The `InferenceValidator` calls this function with a list of finished inference IDs
2. **Private Calculation**: Using `s.configManager.GetCurrentSeed().Seed`, each validator runs the `ShouldValidate` algorithm locally with their secret seed
3. **Deterministic Selection**: The combination of the secret seed, inference ID, and validator's model-specific power produces a consistent decision of whether to validate each inference
4. **Validation Execution**: Validators immediately perform verification for inferences they're selected to validate

**Critical Point**: At this stage, the seed is private - other participants cannot predict or verify which inferences a validator should validate.

### Phase 2: Validation Result Publication

**Immediate Publication:**
When validators complete verification, they immediately publish their results via `MsgValidation` transactions to the blockchain. These validation results are public and recorded in the `EpochGroupValidations` structure.

**No Seed Verification Yet:**
During this phase, there's no verification of whether the validator was actually supposed to validate the inference - the system accepts validation results from any eligible validator for the model.

### Phase 3: Seed Revelation and Claim Verification

**Seed Revelation:**
At the end of each epoch during the claim rewards stage, validators reveal their previously secret seed when submitting `MsgClaimRewards` transactions. The seed that was private during validation decision-making now becomes public.

**Retroactive Validation Check:**
The blockchain performs comprehensive verification in `getMustBeValidatedInferences`:

1. **Reconstruct Required Validations**: Using the now-revealed seed, the system re-runs the exact same `ShouldValidate` calculation that the validator used privately
2. **Compare Expected vs Actual**: The system checks whether the validator performed validation for exactly the inferences they were supposed to validate based on their seed
3. **Enforce Compliance**: If any required validations are missing, the claim is rejected with `ErrValidationsMissed`

**Seed Signature Verification:**
The system also verifies that the revealed seed is authentic using cryptographic signatures in `validateSeedSignature`, ensuring validators cannot retroactively choose favorable seeds.

### Security and Integrity Mechanisms

**Prevents Gaming:**
- Validators cannot predict which inferences they'll need to validate until they've already committed to their seed
- Retroactive verification ensures validators cannot selectively skip difficult validations
- Model-specific weight calculations prevent validators from avoiding certain models

**Cryptographic Accountability:**
- Seeds are cryptographically signed when generated, preventing post-hoc manipulation
- The deterministic `ShouldValidate` function ensures the same seed always produces the same validation requirements
- Public verification during claims provides transparent accountability

**Model-Specific Enforcement:**
- Validation requirements are calculated using model-specific power within the appropriate subgroup
- Only validators supporting the inference's model are eligible for validation assignment
- Cross-model validation assignments are prevented by the weight map filtering

### Implementation Timeline

**Current Epoch**: Private seed-based decision making
**Validation Phase**: Public validation result publication  
**Next Epoch Transition**: Seed revelation and retroactive verification
**Reward Distribution**: Only compliant validators receive rewards

This system ensures both unpredictable validator assignment (due to secret seeds) and verifiable compliance (through retroactive checking), creating a robust validation mechanism that's difficult to game while maintaining transparency and accountability.
