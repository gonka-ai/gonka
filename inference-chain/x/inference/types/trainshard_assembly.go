package types

// TrainshardAssemblyOpensAt returns the first height a shard may be assembled at, height itself when
// neither PoC nor a confirmation PoC event is running. A node reserved inside either is pulled out of
// work the network counts on. Shared with trainshardctl, which waits for this height instead of
// sending an assemble the chain refuses
func TrainshardAssemblyOpensAt(height int64, latest Epoch, params EpochParams, event *ConfirmationPoCEvent) int64 {
	opens := height
	epoch := NewEpochContext(latest, params)
	if epoch.GetCurrentPhase(height) != InferencePhase {
		opens = epoch.EndOfPoCValidation()
	}
	// the next epoch is stored only at the end of its first PoC block
	if next := epoch.NextEpochContext(); height >= next.StartOfPoC() {
		opens = next.EndOfPoCValidation()
	}
	if event != nil && event.Phase != ConfirmationPoCPhase_CONFIRMATION_POC_INACTIVE &&
		event.Phase != ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED {
		opens = max(opens, event.GetValidationEnd(&params)+1)
	}
	return opens
}
