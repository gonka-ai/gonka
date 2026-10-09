package host

import (
	"errors"

	"devshard/state"
	"devshard/types"
)

var errEmptySnapshot = errors.New("empty snapshot")

func MarshalStateSnapshot(state *types.EscrowState) ([]byte, error) {
	return types.MarshalStateSnapshotProto(state, nil, nil)
}

func MarshalStateSnapshotWithCommitted(st *types.EscrowState, committedEntries map[uint64][]byte, sealedNonces map[uint64]uint64) ([]byte, error) {
	data, err := types.MarshalStateSnapshotProto(st, committedEntries, sealedNonces)
	if err != nil {
		return nil, err
	}
	root, err := state.SnapshotRoot(st)
	if err != nil {
		return nil, err
	}
	return types.RootedSnapshot(data, root)
}

func UnmarshalStateSnapshot(data []byte) (*types.EscrowState, error) {
	state, _, _, err := UnmarshalStateSnapshotWithCommitted(data)
	return state, err
}

func UnmarshalStateSnapshotWithCommitted(data []byte) (*types.EscrowState, map[uint64][]byte, map[uint64]uint64, error) {
	if len(data) == 0 {
		return nil, nil, nil, errEmptySnapshot
	}
	payload, root, err := types.SnapshotData(data)
	if err != nil {
		return nil, nil, nil, err
	}
	st, entries, sealed, err := types.UnmarshalStateSnapshotProto(payload)
	if err == nil && root != nil {
		err = state.CheckSnapshotRoot(st, root)
	}
	return st, entries, sealed, err
}
