package user

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"devshard/types"
)

const refusalSnapshotInterval = 100

type refusalSnapshot struct {
	n          uint64
	data       []byte
	diffs      []types.Diff
	signatures map[uint64]map[uint32][]byte
}

// Called after each committed diff, with s.mu held.
func (s *Session) retainRefusalDiffLocked(diff types.Diff) {
	for _, snap := range []*refusalSnapshot{s.refusalUsable, s.refusalCandidate} {
		if snap != nil && diff.Nonce > snap.n && len(snap.diffs) < types.RefusalTailLimit {
			snap.diffs = append(snap.diffs, diff)
		}
	}
	if s.refusalUsable != nil && s.nonce-s.refusalUsable.n > types.RefusalTailLimit {
		s.refusalUsable = nil
	}
	if s.refusalCandidate != nil && s.nonce-s.refusalCandidate.n > types.RefusalTailLimit {
		s.refusalCandidate = nil
	}
	if s.refusalCandidate != nil || s.refusalCapturing || s.sm.Phase() != types.PhaseActive {
		return
	}
	if s.refusalUsable != nil && s.nonce%refusalSnapshotInterval != 0 {
		return
	}
	st := s.sm.ExportState()
	// Empty sealed accumulators are encoded canonically for network snapshots.
	if len(st.SealedAcc) == 0 {
		st.SealedAcc = make([]byte, 32)
	}
	snap := &refusalSnapshot{n: s.nonce, signatures: map[uint64]map[uint32][]byte{}}
	s.refusalCandidate = snap
	s.refusalCapturing = true
	go func() {
		data, err := types.MarshalStateSnapshotProto(st, nil, nil)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.refusalCapturing = false
		if err != nil {
			if s.refusalCandidate == snap {
				s.refusalCandidate = nil
			}
			return
		}
		snap.data = data
		s.promoteRefusalSnapshotLocked()
	}()
}

func (s *Session) retainRefusalSignatureLocked(n uint64, slot uint32, sig []byte) {
	for _, snap := range []*refusalSnapshot{s.refusalUsable, s.refusalCandidate} {
		if snap == nil || n < snap.n || n-snap.n > types.RefusalTailLimit {
			continue
		}
		if snap.signatures[n] == nil {
			snap.signatures[n] = map[uint32][]byte{}
		}
		snap.signatures[n][slot] = append([]byte(nil), sig...)
	}
	s.promoteRefusalSnapshotLocked()
}

func (s *Session) promoteRefusalSnapshotLocked() {
	c := s.refusalCandidate
	if c == nil || c.data == nil {
		return
	}
	owners := [2]map[string]bool{{}, {}}
	for n, sigs := range c.signatures {
		window := 0
		if n > c.n {
			window = 1
		}
		for slot := range sigs {
			owners[window][s.sm.SlotAddress(slot)] = true
		}
	}
	for _, set := range owners {
		var weight uint32
		for owner := range set {
			weight += s.sm.AddressSlotCount(owner)
		}
		if weight >= s.sm.QuorumThreshold() {
			s.refusalUsable = c
			s.refusalCandidate = nil
			return
		}
	}
}

// BuildRefusalPackage copies the tail and reuses the serialized snapshot.
func (s *Session) BuildRefusalPackage(inferenceID uint64) (*types.RefusalPackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.refusalUsable
	if c == nil || s.nonce < c.n || s.nonce-c.n > types.RefusalTailLimit || uint64(len(c.diffs)) != s.nonce-c.n {
		return nil, fmt.Errorf("no certified refusal snapshot")
	}
	rec, ok := s.sm.GetInference(inferenceID)
	if !ok || rec.Status != types.StatusPending {
		return nil, fmt.Errorf("refusal target is not pending")
	}
	p := &types.RefusalPackage{EscrowID: s.escrowID, Version: s.sm.SnapshotStateNoInferences().StateRootAndProtocolVersion, N: c.n, T: s.nonce, Snapshot: c.data, Signatures: map[uint64]map[uint32][]byte{}}
	for _, d := range c.diffs {
		cp := types.Diff{Nonce: d.Nonce, UserSig: append([]byte(nil), d.UserSig...), PostStateRoot: append([]byte(nil), d.PostStateRoot...)}
		for _, tx := range d.Txs {
			cp.Txs = append(cp.Txs, proto.Clone(tx).(*types.DevshardTx))
		}
		p.Diffs = append(p.Diffs, cp)
	}
	for n, sigs := range c.signatures {
		p.Signatures[n] = map[uint32][]byte{}
		for slot, sig := range sigs {
			p.Signatures[n][slot] = append([]byte(nil), sig...)
		}
	}
	return p, nil
}
