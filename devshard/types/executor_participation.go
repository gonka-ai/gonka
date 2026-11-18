package types

func ExecutorParticipated(st EscrowState, slot uint32) bool {
	owned := map[uint32]bool{slot: true}
	for _, g := range st.Group {
		if g.SlotID == slot && g.ValidatorAddress != "" {
			for _, o := range st.Group {
				if o.ValidatorAddress == g.ValidatorAddress {
					owned[o.SlotID] = true
				}
			}
			break
		}
	}
	for id := range owned {
		if hs := st.HostStats[id]; hs != nil && (hs.Finished > 0 || hs.CompletedValidations > 0 || hs.Validated > 0 || hs.Cost > 0) {
			return true
		}
	}
	for _, r := range st.Inferences {
		if r == nil || !owned[r.ExecutorSlot] {
			continue
		}
		if r.ConfirmedAtHeight > 0 || r.ConfirmedAtTimeMs > 0 || len(r.ResponseHash) > 0 {
			return true
		}
		switch r.Status {
		case StatusStarted, StatusFinished, StatusChallenged, StatusValidated, StatusInvalidated:
			return true
		}
	}
	return false
}
