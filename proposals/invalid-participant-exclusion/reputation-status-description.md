# Status and Reputation
For clarity, this is the current status of how status and reputation are being calculated.

## Status
### Values
Status is stored as part of the `Participant` record. The possible values are:
- ACTIVE - a participant running correctly 
- INVALID - a participant that has done enough invalid inferences to be taken out of voting/rewards/participation
- RAMPING - a newly added participant that hasn't reached statistical significance in the number of inferences to be marked ACTIVE
- INACTIVE - Not currently used

### Effect
The only meaningful Status at the moment is INVALID. An INVALID participant will:
1. Be removed from all EpochGroups
2. Be excluded from all rewards at Epoch claim time
3. Lose payments for all work done in the current Epoch
3. Have all voting and validation power removed
4. Have collateral slashed by slash_fraction_invalid (currently 20%)
5. Lose out on gaining EpochsCompleted

### Calculations
Status is calculated every time a Participant is updated. 
The calculation is currently based entirely on Inference validations. 
The participant will be marked invalid if:
- The number of consecutive failures being random exceeds a probability of 1 in a million
- The total failure rate exceeds a Z score of 1 AND there are at least 10 measurements or enough measurements to ensure statistical significance.
- The failure rate significance is calculated based on the FalsePositiveRate

To put this all in slightly less mathematical terms:
- If a participant is failing every time, they get marked invalid quickly
- If a participant is failing more than the expected amount in a statistically significant way, they get marked as invalid

## Reputation
### Values
Reputation is a value from 0 to 100. It represents the long term reliability of a participant. The max of 100 can only be reached after participation in at least 100 epochs.

Reputation is stored in EpochGroup.GroupData.ValidationWeights, and is calculated and set at the start of an Epoch only.
### Effect
Reputation is currently used for two things:
1. Probability of inference validation. At 0, an average of 1 validation happens per inference, and at 100 an average of .01 times.
2. Allowance for concurrent invalidations - The higher reputation allows for more invalidations at a time, as a way ensuring the network will not be "griefed" by bad invalidation requests.

### Calculations
A full exposition on the math involved can be found [here](inference-chain/x/inference/calculations/reputation.md)

The TL;DR is that reputation starts at 0 for all participants and, assuming good behavior, goes to 100 over 100 epochs. It will be hit for missed Inferences beyond just not increasing the count.