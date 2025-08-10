package types

// Effectively whether we've gotten both the FinishInference and the StartInference
func (i *Inference) IsCompleted() bool {
	return i.Model != "" && i.RequestedBy != "" && i.ExecutedBy != ""
}

func (i *Inference) StartProcessed() bool {
	return i.PromptHash != ""
}

func (i *Inference) FinishedProcessed() bool {
	return i.ExecutedBy != ""
}
