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
