package state

import (
	"fmt"
	"math/bits"

	"github.com/productscience/inference/devshardversion"

	"devshard/types"
)

// validateAssignedSnapshot rejects missing v7 accounting rather than silently
// treating an old prototype's nonempty history as zero work. Legacy snapshots
// keep their original roots and do not acquire an Assigned counter.
func validateAssignedSnapshot(st *types.EscrowState) error {
	exact := devshardversion.UsesAssignedCount(st.StateRootAndProtocolVersion)
	if !exact {
		for _, hs := range st.HostStats {
			if hs != nil && hs.Assigned != 0 {
				return fmt.Errorf("assigned count requires devshard v7 or later")
			}
		}
		return nil
	}
	live := make(map[uint32]uint64)
	for _, rec := range st.Inferences {
		if rec != nil {
			live[rec.ExecutorSlot]++
		}
	}
	var total uint64
	for _, slot := range st.Group {
		hs := st.HostStats[slot.SlotID]
		if hs == nil {
			return fmt.Errorf("missing host stats for slot %d", slot.SlotID)
		}
		if hs.Assigned < live[slot.SlotID] || hs.Assigned < uint64(hs.Missed) || uint64(hs.Finished) > hs.Assigned-uint64(hs.Missed) {
			return fmt.Errorf("inconsistent assigned count for slot %d", slot.SlotID)
		}
		next, carry := bits.Add64(total, hs.Assigned, 0)
		if carry != 0 || next > st.LatestNonce {
			return fmt.Errorf("assigned count exceeds snapshot nonce")
		}
		total = next
	}
	if exact && total == 0 {
		for _, b := range st.SealedAcc {
			if b != 0 {
				return fmt.Errorf("sealed v7 history is missing assigned counts; use a new v7 escrow")
			}
		}
	}
	return nil
}
