Now I'll provide you with a comprehensive guide to the Validator Consensus Mechanism for Artifact Validation in the inference-chain:

VALIDATOR CONSENSUS MECHANISM FOR ARTIFACT VALIDATION
Overview
The consensus mechanism validates participants' Proof of Computation (PoC) artifacts through a weighted voting system where multiple validators check artifact distributions and vote on acceptance/rejection. The system uses a majority consensus approach: participants receiving valid votes from >50% of total validator power are accepted; those receiving fraud votes from >50% are rejected.

PHASE 1: VALIDATION SUBMISSION
File: /home/staaason/Desktop/gonka/inference-chain/x/inference/keeper/msg_server_submit_poc_validation.go

What Happens:
Validators check artifacts during the PoC validation exchange window
Each validator submits validation data for each participant's PoC batch
Key Data Fields Submitted:
message PoCValidation {
  string participant_address = 1;           // The participant being validated
  string validator_participant_address = 2; // The validator doing validation
  repeated int64 nonces = 5;                // Distribution nonce data
  repeated double dist = 6;                 // Original distribution
  repeated double received_dist = 7;        // Received distribution (for comparison)
  double r_target = 8;                      // Target R value
  double fraud_threshold = 9;               // Fraud detection threshold
  int64 n_invalid = 10;                     // Invalid count
  double probability_honest = 11;           // Probability of honesty
  bool fraud_detected = 12;                 // ✅ CRITICAL: Fraud vote
}
Code Flow (lines 13-60):
func (k msgServer) SubmitPocValidation(goCtx context.Context, msg *types.MsgSubmitPocValidation) {
    // 1. Validate submission is within validation exchange window
    if !epochContext.IsValidationExchangeWindow(currentBlockHeight) {
        return error "PoC validation exchange window is closed"
    }
    
    // 2. Store validation with fraud_detected flag
    validation := toPoCValidation(msg, currentBlockHeight)
    k.SetPoCValidation(ctx, *validation)  // Stored in chain state
}
PHASE 2: CONSENSUS VOTING LOGIC
File: /home/staaason/Desktop/gonka/inference-chain/x/inference/module/chainvalidation.go

Core Voting Function: calculateValidationOutcome
Lines 724-740:

func calculateValidationOutcome(
    currentValidatorsSet map[string]int64,  // Validator → Power mapping
    validations []types.PoCValidation       // All submitted validations
) validationOutcome {
    validWeight := int64(0)
    invalidWeight := int64(0)
    
    for _, v := range validations {
        if weight, ok := currentValidatorsSet[v.ValidatorParticipantAddress]; ok {
            if v.FraudDetected {  // ✅ THIS IS THE VOTE
                invalidWeight += weight  // Count as fraud vote
            } else {
                validWeight += weight    // Count as valid vote
            }
        }
    }
    
    return validationOutcome{
        ValidWeight:   validWeight,
        InvalidWeight: invalidWeight,
    }
}
Consensus Decision: pocValidated
Lines 615-664:

func (wc *WeightCalculator) pocValidated(
    vals []types.PoCValidation,
    participantAddress string
) bool {
    // Calculate voting thresholds
    totalWeight := calculateTotalWeight(wc.CurrentValidatorWeights)
    halfWeight := int64(totalWeight / 2)
    
    valOutcome := calculateValidationOutcome(wc.CurrentValidatorWeights, vals)
    
    // ✅ CONSENSUS DECISION LOGIC:
    if valOutcome.ValidWeight > halfWeight {
        // ACCEPTED: More than 50% of validators voted "valid"
        return true
    } else if valOutcome.InvalidWeight > halfWeight {
        // REJECTED: More than 50% of validators detected fraud
        return false
    } else {
        // NO CONSENSUS: Reject by default
        return false
    }
}
PHASE 3: WEIGHT CALCULATION & PARTICIPANT SELECTION
File: /home/staaason/Desktop/gonka/inference-chain/x/inference/module/chainvalidation.go - ComputeNewWeights function

Data Aggregation (Lines 233-330):
func (am AppModule) ComputeNewWeights(ctx context.Context, upcomingEpoch types.Epoch) {
    // STEP 1: Get current validator weights
    currentValidatorWeights := am.getCurrentValidatorWeights(ctx)
    // Maps validator address → power/stake
    
    // STEP 2: Get all PoC batches from participants
    originalBatches := am.keeper.GetPoCBatchesByStage(ctx, epochStartBlockHeight)
    // Contains: participant → their PoC batch submissions
    
    // STEP 3: Get all validation submissions
    validations := am.keeper.GetPoCValidationByStage(ctx, epochStartBlockHeight)
    // Contains: participant → [all validators' votes on that participant]
    
    // STEP 4: Create WeightCalculator
    calculator := NewWeightCalculator(
        currentValidatorWeights,  // How much power each validator has
        originalBatches,          // What participants submitted
        validations,              // What validators voted
        participants,
        seeds,
        epochStartBlockHeight,
        logger,
    )
    
    // STEP 5: Calculate which participants pass consensus
    activeParticipants := calculator.Calculate()
}
Participant Filtering (Lines 499-512):
func (wc *WeightCalculator) Calculate() []*types.ActiveParticipant {
    for _, participantAddress := range sortedBatchKeys {
        activeParticipant := wc.validatedParticipant(participantAddress)
        
        if activeParticipant != nil {
            // Only include participants that passed consensus
            activeParticipants = append(activeParticipants, activeParticipant)
        }
    }
    return activeParticipants
}

func (wc *WeightCalculator) validatedParticipant(participantAddress string) *types.ActiveParticipant {
    vals := wc.getParticipantValidations(participantAddress)
    
    // ✅ KEY CHECK: Does participant pass consensus?
    if !wc.pocValidated(vals, participantAddress) {
        return nil  // Rejected - not included in next epoch
    }
    
    // Passed consensus - include with their weight
    return &types.ActiveParticipant{
        Index:        participant.Address,
        Weight:       claimedWeight,
        ValidatorKey: participant.ValidatorKey,
        // ... other fields
    }
}
PHASE 4: EPOCH TRANSITION & EXECUTION
File: /home/staaason/Desktop/gonka/inference-chain/x/inference/module/module.go

EndBlock Handler (Lines 347-454):
func (am AppModule) onEndOfPoCValidationStage(ctx context.Context, blockHeight int64) {
    // At IsEndOfPoCValidationStage(blockHeight):
    
    // 1. Trigger consensus voting and weight calculation
    activeParticipants := am.ComputeNewWeights(ctx, *upcomingEpoch)
    //    ↓
    //    Internally calls pocValidated() for each participant
    //    Only participants with >50% valid votes are returned
    
    // 2. Set thesed participants for validate the upcoming epoch
    am.keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
        Participants:        activeParticipants,  // Only consensually validated
        EpochGroupId:        upcomingEpoch.Index,
        PocStartBlockHeight: upcomingEpoch.PocStartBlockHeight,
    })
    
    // 3. These become the new validator set in the next epoch
}
DATA FLOW DIAGRAM
EPOCH N: Artifacts Submitted
├── Participants submit PoC batches
│   └── PoCBatch: participant_address, nonces, dist, batch_id, node_id
│
EPOCH N: Validation Phase
├── Each validator checks artifacts during validation window
├── Validator submits validation for EACH participant:
│   └── PoCValidation: {
│       participant_address,
│       validator_participant_address,
│       fraud_detected: true/false,  ← THE VOTE
│       dist, received_dist, r_target, n_invalid, etc.
│   }
│
EPOCH N: End of PoC Validation Stage
├── System aggregates all validator votes per participant
├── calculateValidationOutcome():
│   ├── For each validator's vote:
│   │   ├── if fraud_detected = true  → invalidWeight += validator_power
│   │   └── if fraud_detected = false → validWeight += validator_power
│   └── Returns (validWeight, invalidWeight)
│
├── pocValidated() decision:
│   ├── if validWeight > totalWeight/2    → ACCEPT ✅
│   ├── elif invalidWeight > totalWeight/2 → REJECT ❌
│   └── else                              → REJECT by default ❌
│
├── Only accepted participants → activeParticipants list
└── SetActiveParticipants() for EPOCH N+1
    └── These become new validators with their weights

EPOCH N+1: New Validator Set
└── Only consensually validated participants are active
    └── With weights based on their PoC submissions
VOTING WEIGHT CALCULATION
Each validator has a power/weight determined by their stake:

func (am AppModule) getCurrentValidatorWeights(ctx context.Context) map[string]int64 {
    currentGroup := am.keeper.GetCurrentEpochGroup(ctx)
    currentMembers := currentGroup.GetGroupMembers(ctx)
    
    weights := make(map[string]int64)
    for _, member := range currentMembers {
        weights[member.Member.Address] = ParseInt(member.Member.Weight)
    }
    return weights
}
Example Scenario:

Total validator power = 100
- Validator A: power 40, votes fraud_detected=false (VALID)
- Validator B: power 35, votes fraud_detected=true  (INVALID)
- Validator C: power 25, votes fraud_detected=false (VALID)

Result:
- validWeight = 40 + 25 = 65
- invalidWeight = 35
- halfWeight = 50
- Decision: 65 > 50 → ACCEPT ✅
KEY POINTS
| Aspect | Details | |--------|---------| | Submission Window | During PoC validation exchange window | | Vote Field | fraud_detected: bool in PoCValidation | | Vote Weight | Validator's current stake/power in epoch | | Acceptance Threshold | validWeight > totalWeight / 2 (>50%) | | Rejection Threshold | invalidWeight > totalWeight / 2 (>50%) | | No Consensus | Participant is rejected by default | | Output | List of accepted participants for next epoch | | Execution | onEndOfPoCValidationStage() at epoch end |

This consensus mechanism ensures that artifacts are validated by multiple independent validators weighted by their stake, providing Byzantine fault tolerance through majority voting on fraud detection.