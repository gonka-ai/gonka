package inference

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

// carryableParticipant applies the eligibility checks shared by preserved-node
// carry-over and epoch fallback. Fresh PoC validation does not use these checks:
// exclusion is per epoch and must not prevent a participant from proving anew.
func (am AppModule) carryableParticipant(
	ctx context.Context,
	epochIndex uint64,
	address string,
	liveSet map[string]bool,
) (types.Participant, bool) {
	if !liveSet[address] {
		am.LogWarn("EpochCarryover: skipping participant removed from current epoch group", types.PoC,
			"participant", address, "epochIndex", epochIndex)
		return types.Participant{}, false
	}
	if am.wasExcludedInEpoch(ctx, epochIndex, address) {
		am.LogWarn("EpochCarryover: skipping participant excluded during current epoch", types.PoC,
			"participant", address, "epochIndex", epochIndex)
		return types.Participant{}, false
	}
	participant, found := am.keeper.GetParticipant(ctx, address)
	if !found {
		am.LogError("EpochCarryover: participant record not found", types.PoC, "participant", address)
		return types.Participant{}, false
	}
	if participant.Status == types.ParticipantStatus_INVALID ||
		participant.Status == types.ParticipantStatus_INACTIVE {
		am.LogWarn("EpochCarryover: skipping participant with non-carryable status", types.PoC,
			"participant", address, "status", participant.Status)
		return types.Participant{}, false
	}
	return participant, true
}
