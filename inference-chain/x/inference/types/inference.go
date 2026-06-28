package types

// returns true if we've gotten data we can only get from both StartInference and FinishInference
func (i *Inference) IsCompleted() bool {
	return i.Model != "" && i.RequestedBy != "" && i.ExecutedBy != ""
}

func (i *Inference) StartProcessed() bool {
	// StartInference always assigns AssignedTo (required by ValidateBasic).
	// Symmetric with FinishedProcessed which checks ExecutedBy.
	return i.AssignedTo != ""
}

func (i *Inference) FinishedProcessed() bool {
	return i.ExecutedBy != ""
}

// CappedActualCost returns the cost allowed to move value out of the module pool (executor
// payment, ShareWork, refunds). It never exceeds the escrow actually collected and floors
// negatives to 0 — the single source of truth for the ActualCost <= EscrowAmount invariant.
func (i *Inference) CappedActualCost() int64 {
	cost := i.ActualCost
	if cost < 0 {
		cost = 0
	}
	escrowCap := i.EscrowAmount
	if escrowCap < 0 {
		escrowCap = 0
	}
	if cost > escrowCap {
		return escrowCap
	}
	return cost
}
