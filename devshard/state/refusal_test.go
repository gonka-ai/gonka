package state

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

func TestRefusalRestoreWithoutSealedIndex(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, _, user, group := newSealTestSM(t, "escrow-sealed", hosts, true)
	driveSealInferenceToFinished(t, sm, "escrow-sealed", hosts)
	require.NoError(t, sm.SealInference(1))
	st := sm.ExportState()
	data, err := types.MarshalStateSnapshotProto(st, nil, nil)
	require.NoError(t, err)
	p := &types.RefusalPackage{EscrowID: st.EscrowID, Version: st.StateRootAndProtocolVersion, N: 3, T: 3, Snapshot: data, Signatures: map[uint64]map[uint32][]byte{3: {}}}
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	msg, _ := proto.Marshal(&types.StateSignatureContent{EscrowId: st.EscrowID, Nonce: 3, StateRoot: root})
	for slot, signer := range hosts {
		sig, err := signer.Sign(msg)
		require.NoError(t, err)
		p.Signatures[3][uint32(slot)] = sig
	}
	imported, err := sm.VerifyRefusalPackage(p)
	require.NoError(t, err)
	other, err := NewStateMachine(st.EscrowID, st.Config, group, 100000, user.Address(), signing.NewSecp256k1Verifier(), storage.NewMemory(), WithVersion(st.StateRootAndProtocolVersion))
	require.NoError(t, err)
	other.RestoreState(imported)
	late := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 1, ValidatorSlot: 2, Valid: true}}}
	_, err = sm.ApplyLocal(4, []*types.DevshardTx{late})
	require.ErrorIs(t, err, types.ErrInferenceSealed)
	_, err = other.ApplyLocal(4, []*types.DevshardTx{late})
	require.ErrorIs(t, err, types.ErrInferenceNotFound)
	rootA, err := sm.ApplyLocal(4, nil)
	require.NoError(t, err)
	rootB, err := other.ApplyLocal(4, nil)
	require.NoError(t, err)
	require.Equal(t, rootA, rootB)
	for n := uint64(5); n <= 150; n++ {
		rootA, err = sm.ApplyLocal(n, nil)
		require.NoError(t, err)
		rootB, err = other.ApplyLocal(n, nil)
		require.NoError(t, err)
		require.Equal(t, rootA, rootB)
	}
}

func TestRefusalSignatureCannotClaimAnotherSlot(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, _, _, _ := newSealTestSM(t, "escrow-quorum", hosts, true)
	st := sm.ExportState()
	st.SealedAcc = make([]byte, 32)
	data, err := types.MarshalStateSnapshotProto(st, nil, nil)
	require.NoError(t, err)
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	msg, _ := proto.Marshal(&types.StateSignatureContent{EscrowId: st.EscrowID, Nonce: 0, StateRoot: root})
	sig, err := hosts[0].Sign(msg)
	require.NoError(t, err)
	p := &types.RefusalPackage{EscrowID: st.EscrowID, Version: st.StateRootAndProtocolVersion, N: 0, T: 0, Snapshot: data, Signatures: map[uint64]map[uint32][]byte{0: {0: sig, 1: sig, 2: sig}}}
	_, err = sm.VerifyRefusalPackage(p)
	require.Error(t, err)
}

func TestRefusalOwnerWeightCountedOnceAcrossNonces(t *testing.T) {
	a, b, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup([]*signing.Secp256k1Signer{a, b}, []int{2, 1})
	cfg := testutil.DefaultConfig(3)
	sm, err := NewStateMachine("weighted", cfg, group, 10000, user.Address(), signing.NewSecp256k1Verifier(), storage.NewMemory())
	require.NoError(t, err)
	st := sm.ExportState()
	st.SealedAcc = make([]byte, 32)
	data, err := types.MarshalStateSnapshotProto(st, nil, nil)
	require.NoError(t, err)
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	content, _ := proto.Marshal(&types.StateSignatureContent{EscrowId: "weighted", StateRoot: root})
	sig, err := a.Sign(content)
	require.NoError(t, err)
	p := &types.RefusalPackage{EscrowID: "weighted", Version: st.StateRootAndProtocolVersion, T: 1, Snapshot: data, Signatures: map[uint64]map[uint32][]byte{0: {0: sig, 1: sig}, 1: {}}}
	root, err = sm.ApplyLocal(1, nil)
	require.NoError(t, err)
	p.Diffs = []types.Diff{testutil.SignDiffWithRoot(t, user, "weighted", 1, nil, root)}
	content, _ = proto.Marshal(&types.StateSignatureContent{EscrowId: "weighted", Nonce: 1, StateRoot: root})
	sig, err = a.Sign(content)
	require.NoError(t, err)
	p.Signatures[1][0] = sig
	_, err = sm.VerifyRefusalPackage(p)
	require.ErrorContains(t, err, "quorum")
	sig, err = b.Sign(content)
	require.NoError(t, err)
	p.Signatures[1][2] = sig
	_, err = sm.VerifyRefusalPackage(p)
	require.NoError(t, err)
}

func TestRefusalCheckpointQuorums(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(hosts, []int{2, 1, 1})
	sm, err := NewStateMachine("checkpoint", testutil.DefaultConfig(4), group, 10000, user.Address(), signing.NewSecp256k1Verifier(), storage.NewMemory())
	require.NoError(t, err)
	st := sm.ExportState()
	st.SealedAcc = make([]byte, 32)
	snapshot, err := types.MarshalStateSnapshotProto(st, nil, nil)
	require.NoError(t, err)
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	roots := [][]byte{root}
	var diffs []types.Diff
	for n := uint64(1); n <= 2; n++ {
		root, err := sm.ApplyLocal(n, nil)
		require.NoError(t, err)
		roots = append(roots, root)
		diffs = append(diffs, testutil.SignDiffWithRoot(t, user, "checkpoint", n, nil, root))
	}
	for _, tc := range []struct {
		name         string
		tail         int
		slots        map[uint64][]uint32
		badSignature bool
		badDiff      bool
		accept       bool
	}{
		{"at N without tail", 0, map[uint64][]uint32{0: {0, 2}}, false, false, true},
		{"after N across nonces", 2, map[uint64][]uint32{1: {0}, 2: {2}}, false, false, true},
		{"union only", 2, map[uint64][]uint32{0: {0}, 1: {2}}, false, false, false},
		{"duplicate owner slots and nonces", 2, map[uint64][]uint32{1: {0, 1}, 2: {0, 1}}, false, false, false},
		{"one set reaches quorum", 2, map[uint64][]uint32{0: {0, 2}, 2: {3}}, false, false, true},
		{"invalid extra signature", 2, map[uint64][]uint32{0: {0, 2}, 2: {3}}, true, false, false},
		{"invalid trailing diff", 2, map[uint64][]uint32{0: {0, 2}}, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &types.RefusalPackage{EscrowID: st.EscrowID, Version: st.StateRootAndProtocolVersion, T: uint64(tc.tail), Snapshot: snapshot, Diffs: append([]types.Diff(nil), diffs[:tc.tail]...), Signatures: map[uint64]map[uint32][]byte{}}
			for n, slots := range tc.slots {
				p.Signatures[n] = map[uint32][]byte{}
				content, err := proto.Marshal(&types.StateSignatureContent{EscrowId: st.EscrowID, Nonce: n, StateRoot: roots[n]})
				require.NoError(t, err)
				for _, slot := range slots {
					owner := 0
					if slot >= 2 {
						owner = int(slot) - 1
					}
					sig, err := hosts[owner].Sign(content)
					require.NoError(t, err)
					p.Signatures[n][slot] = sig
				}
			}
			if tc.badSignature {
				p.Signatures[2][3] = []byte("bad")
			}
			if tc.badDiff {
				p.Diffs[1] = testutil.SignDiffWithRoot(t, user, st.EscrowID, 2, nil, make([]byte, 32))
			}
			before, err := sm.ComputeStateRoot()
			require.NoError(t, err)
			result, err := sm.VerifyRefusalPackage(p)
			if tc.accept {
				require.NoError(t, err)
				got, err := SnapshotRoot(result)
				require.NoError(t, err)
				require.Equal(t, roots[tc.tail], got)
			} else {
				require.Error(t, err)
			}
			after, err := sm.ComputeStateRoot()
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
