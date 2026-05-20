package host

import (
	"encoding/json"

	"devshard/types"
)

// StateSnapshot matches the proxy session snapshot envelope. Hosts do not use
// HostSyncNonce, but keeping the same shape makes snapshots portable across the
// shared storage/recovery code.
type StateSnapshot struct {
	State            *types.EscrowState `json:"state"`
	CommittedEntries map[uint64][]byte  `json:"committed_entries,omitempty"`
	SealedNonces     map[uint64]uint64  `json:"sealed_nonces,omitempty"`
	HostSyncNonce    map[int]uint64     `json:"host_sync_nonce,omitempty"`
}

func MarshalStateSnapshot(state *types.EscrowState) ([]byte, error) {
	return json.Marshal(StateSnapshot{State: state})
}

func MarshalStateSnapshotWithCommitted(state *types.EscrowState, committedEntries map[uint64][]byte, sealedNonces map[uint64]uint64) ([]byte, error) {
	return json.Marshal(StateSnapshot{
		State:            state,
		CommittedEntries: committedEntries,
		SealedNonces:     sealedNonces,
	})
}

func UnmarshalStateSnapshot(data []byte) (*types.EscrowState, error) {
	var wrapped StateSnapshot
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.State != nil {
		return wrapped.State, nil
	}

	var bare types.EscrowState
	if err := json.Unmarshal(data, &bare); err != nil {
		return nil, err
	}
	return &bare, nil
}

func UnmarshalStateSnapshotWithCommitted(data []byte) (*types.EscrowState, map[uint64][]byte, map[uint64]uint64, error) {
	var wrapped StateSnapshot
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.State != nil {
		return wrapped.State, wrapped.CommittedEntries, wrapped.SealedNonces, nil
	}

	var bare types.EscrowState
	if err := json.Unmarshal(data, &bare); err != nil {
		return nil, nil, nil, err
	}
	return &bare, nil, nil, nil
}
