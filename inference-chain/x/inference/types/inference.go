package types

// Effectively whether we've gotten both the FinishInference and the StartInference
func (i *Inference) IsCompleted() bool {
	return i.Model != "" && i.RequestedBy != "" && i.ExecutedBy != ""
}
