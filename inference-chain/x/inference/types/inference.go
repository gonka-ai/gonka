package types

// returns true if we've gotten data we can only get from both StartInference and FinishInference
func (i *Inference) IsCompleted() bool {
	return i.Model != "" && i.RequestedBy != "" && i.ExecutedBy != ""
}
