package host

import (
	"errors"

	"devshard/types"
)

var errEmptySnapshot = errors.New("empty snapshot")

func MarshalStateSnapshot(state *types.EscrowState) ([]byte, error) {
	return types.MarshalStateSnapshotProto(state, nil, nil)
}

func MarshalStateSnapshotWithCommitted(state *types.EscrowState, committedEntries map[uint64][]byte, sealedNonces map[uint64]uint64) ([]byte, error) {
	return types.MarshalStateSnapshotProto(state, committedEntries, sealedNonces)
}

func UnmarshalStateSnapshot(data []byte) (*types.EscrowState, error) {
	state, _, _, err := UnmarshalStateSnapshotWithCommitted(data)
	return state, err
}

func UnmarshalStateSnapshotWithCommitted(data []byte) (*types.EscrowState, map[uint64][]byte, map[uint64]uint64, error) {
	if len(data) == 0 {
		return nil, nil, nil, errEmptySnapshot
	}
	return types.UnmarshalStateSnapshotProto(data)
}
