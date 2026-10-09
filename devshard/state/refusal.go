package state

import (
	"bytes"
	"fmt"
	"reflect"

	"google.golang.org/protobuf/proto"

	"devshard/storage"
	"devshard/types"
)

// VerifyRefusalPackage never mutates the receiving machine or its store.
func (sm *StateMachine) VerifyRefusalPackage(p *types.RefusalPackage) (*types.EscrowState, error) {
	if err := p.CheckBounds(); err != nil {
		return nil, err
	}
	if len(p.Snapshot) == 0 {
		return nil, fmt.Errorf("refusal verification requires a snapshot")
	}
	sm.mu.RLock()
	binding := *sm.state
	sm.mu.RUnlock()
	if p.EscrowID != binding.EscrowID || p.Version != binding.StateRootAndProtocolVersion {
		return nil, fmt.Errorf("refusal binding mismatch")
	}
	snapshot, entries, sealed, err := types.UnmarshalStateSnapshotProto(p.Snapshot)
	if err != nil {
		return nil, err
	}
	if snapshot.EscrowID != p.EscrowID || snapshot.StateRootAndProtocolVersion != p.Version || snapshot.LatestNonce != p.N || snapshot.Phase != types.PhaseActive || snapshot.FinalizeNonce != 0 || len(snapshot.SealedAcc) != 32 {
		return nil, fmt.Errorf("invalid refusal snapshot")
	}
	if snapshot.Config != binding.Config || !reflect.DeepEqual(snapshot.Group, binding.Group) || len(entries) > 0 || len(sealed) > 0 {
		return nil, fmt.Errorf("untrusted snapshot binding or caches")
	}
	// Use private memory for diagnostic reads and suppress all observability writes.
	trial, err := NewStateMachine(binding.EscrowID, binding.Config, binding.Group, binding.Config.CreateDevshardFee, sm.userAddress, sm.verifier, storage.NewMemory(), WithVersion(p.Version), WithWarmKeyResolver(sm.warmResolver))
	if err != nil {
		return nil, err
	}
	var ignored []deferredObsWrite
	trial.obsDeferred = &ignored
	trial.RestoreState(snapshot)
	owners := [2]map[string]bool{{}, {}}
	for nonce, sigs := range p.Signatures {
		if nonce < p.N || nonce > p.T || len(sigs) > len(binding.Group) {
			return nil, fmt.Errorf("signature outside refusal range")
		}
	}
	for i := p.N; ; i++ {
		ignored = ignored[:0]
		var root []byte
		if i == p.N {
			root, err = trial.ComputeStateRoot()
		} else {
			root, err = trial.ApplyDiff(p.Diffs[i-p.N-1])
		}
		if err != nil {
			return nil, err
		}
		if trial.state.Phase != types.PhaseActive || trial.state.FinalizeNonce != 0 {
			return nil, fmt.Errorf("refusal state is not active")
		}
		for slot, sig := range p.Signatures[i] {
			owner, ok := sm.slotToAddress[slot]
			if !ok {
				return nil, types.ErrSlotNotInGroup
			}
			data, _ := proto.Marshal(&types.StateSignatureContent{EscrowId: p.EscrowID, Nonce: i, StateRoot: root})
			addr, e := sm.verifier.RecoverAddress(data, sig)
			if e != nil || (addr != owner && !sm.CheckWarmKey(addr, owner)) {
				return nil, types.ErrInvalidStateSig
			}
			window := 0
			if i > p.N {
				window = 1
			}
			owners[window][owner] = true
		}
		if i == p.T {
			break
		}
	}
	// At N and after N are separate quorum proofs.
	for _, set := range owners {
		var weight uint32
		for owner := range set {
			weight += sm.addressToSlotCount[owner]
		}
		if weight >= sm.QuorumThreshold() {
			return trial.state, nil
		}
	}
	return nil, fmt.Errorf("refusal proof lacks quorum at or after snapshot nonce")
}

// SnapshotRoot computes from records, never from a supplied derived cache.
func SnapshotRoot(st *types.EscrowState) ([]byte, error) {
	hs, err := ComputeHostStatsHash(st.HostStats)
	if err != nil {
		return nil, err
	}
	rest, err := ComputeRestHashV2(st.Balance, sealedAccBytes32(st.SealedAcc), st.Inferences, st.WarmKeys)
	if err != nil {
		return nil, err
	}
	return ComputeStateRootFromRestHash(hs, rest, st.Fees, st.Phase, st.StateRootAndProtocolVersion), nil
}

func CheckSnapshotRoot(st *types.EscrowState, root []byte) error {
	got, err := SnapshotRoot(st)
	if err != nil {
		return err
	}
	if len(root) != 32 || !bytes.Equal(got, root) {
		return fmt.Errorf("snapshot root mismatch")
	}
	return nil
}
