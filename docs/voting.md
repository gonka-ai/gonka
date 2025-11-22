# Voting
Voting has two levels. Governance voting and operational voting.

### Governance Voting
This is voting for changing the "big" parameters, such as inflation rate, rewards and punishments for bad inferences, length of Epochs, etc. Voting period is likely to be days.

It is not implemented yet. We plan to use the x/gov module from the Cosmos SDK.


### Operational Voting
Operational voting is voting on short-lived proposals when something seems wrong. Voting period is minutes. For example:

1. An inference does not appear to be using the right model or parameters
2. A PoC does not appear to be valid
3. ?

This is implemented for inference validation, and will be implemented for PoC validation shortly. It leverages the x/group module, with the results of each PoC creating a new group with the weights from the period.

### Diagram
Below is a Mermaid diagram of the inference validation voting process:

```mermaid
flowchart TD
    start(["Validator receives event: Inference finished"])
    decision1{"Should Validator validate?"}
    validation["Validator validates inference"]
    resultMsg["Validator sends result message to chain"]
    chainLogic{"Chain logic determines result"}
    validatedStatus["Inference marked as VALIDATED"]
    votingStatus["Inference moved to VOTING status"]
    proposals["Two proposals created:
    - Invalidate
    - Revalidate"]
    notifyNetwork["Network notified for confirm validation"]
    networkValidation["Network members validate inference"]
    networkMsg["Network members send validation result to chain"]
    voteDecision{"Chain logic determines result"}
    invalidateMsg["Chain sends InvalidateInference message"]
    revalidateMsg["Chain sends RevalidateInference message"]
    validated["Member Validated Inference"]
    invalidated["Member Invalidated Inference"]
    noVoteOnInvalidated["Vote No on Invalidation"]
    noVoteOnRevalidated["Vote No on Revalidation"]
    yesVoteOnInvalidated["Vote Yes on Invalidation"]
    yesVoteOnRevalidation["Vote Yes on Revalidation"]
    revalidateDecision{"Yes votes for revalidate > 50%?"}
    invalidateDecision{"Yes votes for invalidate > 50%?"}
    finalValidated("Inference marked as VALIDATED")
    finalInvalidated("Inference marked as INVALIDATED")
    finalIgnored["Inference will not be validated or invalidated"]
    %% Decision paths
    start --> decision1
    decision1 -->|Yes| validation
    decision1 -->|No| finalIgnored
    validation --> resultMsg
    resultMsg --> chainLogic
    chainLogic -->|Validated| validatedStatus
    chainLogic -->|Invalidated| votingStatus
    votingStatus --> proposals
    proposals --> notifyNetwork
    notifyNetwork --> networkValidation
    networkValidation --> networkMsg
    networkMsg --> voteDecision
    voteDecision --> |Validated| validated
    voteDecision --> |Invalidated| invalidated
    validated --> noVoteOnInvalidated
    validated --> yesVoteOnRevalidation
    invalidated --> yesVoteOnInvalidated
    invalidated --> noVoteOnRevalidated
    noVoteOnInvalidated --> networkValidation
    noVoteOnRevalidated --> networkValidation
    yesVoteOnInvalidated --> invalidateDecision
    yesVoteOnRevalidation --> revalidateDecision
    invalidateDecision --> |No| networkValidation
    revalidateDecision --> |No| networkValidation
    invalidateDecision --> |Yes| invalidateMsg
    revalidateDecision --> |Yes| revalidateMsg
    invalidateMsg --> finalInvalidated
    revalidateMsg --> finalValidated
```
