package heightsync

import "devshard/types"

// EscrowHintsFromState builds DecideHints escrow attachment from session state.
// defaultK/defaultSlots come from the local AnchorScheduler when escrow snapshots are zero.
func EscrowHintsFromState(st *types.EscrowState, defaultK, defaultSlots uint64) *EscrowHeightSyncHints {
	if st == nil {
		return nil
	}
	if st.HeightSyncForcedEnd == 0 && st.HeightSyncCadenceSwallowUntil == 0 {
		return nil
	}
	k, slots := defaultK, defaultSlots
	if st.HeightSyncTurnK != 0 {
		k = st.HeightSyncTurnK
	}
	if st.HeightSyncTurnSlots != 0 {
		slots = st.HeightSyncTurnSlots
	}
	return &EscrowHeightSyncHints{
		ForcedStart:         st.HeightSyncForcedStart,
		ForcedEnd:           st.HeightSyncForcedEnd,
		CadenceSwallowUntil: st.HeightSyncCadenceSwallowUntil,
		SwallowFe:           st.HeightSyncSwallowFe,
		TurnK:               k,
		TurnSlots:           slots,
	}
}
