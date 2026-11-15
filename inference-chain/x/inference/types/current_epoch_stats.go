package types

import "github.com/cosmos/gogoproto/proto"

func NewCurrentEpochStats() *CurrentEpochStats {
	return &CurrentEpochStats{
		InvalidLLR: &Decimal{
			Value:    0,
			Exponent: 0,
		},
		InactiveLLR: &Decimal{
			Value:    0,
			Exponent: 0,
		},
	}
}

// StoredCopy is a deep copy of stats as read, for SetParticipantFromStored.
// StatsHaveChanged compares ConfirmationPoCRatio by pointer, so a shallow copy
// would skip the status recompute a fresh read triggers.
func (m *CurrentEpochStats) StoredCopy() *CurrentEpochStats {
	if m == nil {
		return nil
	}
	return proto.Clone(m).(*CurrentEpochStats)
}
