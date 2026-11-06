package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func newChallengeLimitMachine(t *testing.T, n int) (*StateMachine, []*signing.Secp256k1Signer, string) {
	sm, hosts, escrowID, _ := newChallengeLimitMachines(t, n)
	return sm, hosts, escrowID
}

// newChallengeLimitMachines also returns a constructor for an empty machine
// of the same session, for restore tests.
func newChallengeLimitMachines(t *testing.T, n int) (*StateMachine, []*signing.Secp256k1Signer, string, func() *StateMachine) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, 4)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	// ValidationRate 0 leaves the limit at challengeLimitBase.
	config := types.SessionConfig{
		TokenPrice: 1, VoteThreshold: 1,
		InferenceSealGraceNonces: 1 << 30, InferenceSealGraceSeconds: 1 << 30,
	}
	const escrowID = "escrow-limit"
	fresh := func() *StateMachine {
		sm, err := NewStateMachine(escrowID, config, group, 1<<40, user.Address(), signing.NewSecp256k1Verifier(),
			testutil.MustMemoryStore(t, escrowID, user.Address(), config, group, 1<<40))
		require.NoError(t, err)
		return sm
	}
	sm := fresh()
	nonce := sm.SnapshotState().LatestNonce
	for id := uint64(1); id <= uint64(n); id++ {
		nonce++
		_, err := sm.ApplyLocal(nonce, []*types.DevshardTx{startTx(id), confirmTx(t, hosts, escrowID, id), finishTx(t, hosts, escrowID, id)})
		require.NoError(t, err)
	}
	return sm, hosts, escrowID, fresh
}

func applyNext(sm *StateMachine, txs ...*types.DevshardTx) error {
	_, err := sm.ApplyLocal(sm.SnapshotState().LatestNonce+1, txs)
	return err
}

// One member must not be able to challenge all foreign work: past its limit a
// Valid:false on Finished work is rejected, and a resolved challenge frees room.
func TestChallengeLimit(t *testing.T) {
	sm, hosts, escrowID := newChallengeLimitMachine(t, 40)
	const flooder = uint32(3)
	addr := sm.slotToAddress[flooder]

	var opened []uint64
	var rejectedAt uint64
	for id := uint64(1); id <= 40; id++ {
		if id%4 == uint64(flooder) {
			continue
		}
		err := applyNext(sm, validationTx(t, hosts, escrowID, id, flooder, false))
		if err != nil {
			require.ErrorIs(t, err, types.ErrChallengeLimit)
			rejectedAt = id
			break
		}
		opened = append(opened, id)
	}
	require.Len(t, opened, challengeLimitBase)
	require.NotZero(t, rejectedAt)
	require.Equal(t, uint32(challengeLimitBase), sm.OpenChallenges(addr))
	rec, _ := sm.GetInference(opened[0])
	require.Equal(t, types.StatusChallenged, rec.Status)
	require.Equal(t, flooder+1, rec.Challenger)

	// A Valid:true from the same member is not limited.
	require.NoError(t, applyNext(sm, validationTx(t, hosts, escrowID, rejectedAt, flooder, true)))

	// Two honest votes resolve one challenge as Validated and free a slot.
	first := opened[0]
	var voters []uint32
	for s := uint32(0); s < 4 && len(voters) < 2; s++ {
		if s != flooder && s != uint32(first%4) {
			voters = append(voters, s)
		}
	}
	require.NoError(t, applyNext(sm, voteTx(t, hosts, escrowID, first, voters[0], true), voteTx(t, hosts, escrowID, first, voters[1], true)))
	rec, _ = sm.GetInference(first)
	require.Equal(t, types.StatusValidated, rec.Status)
	require.Equal(t, uint32(challengeLimitBase-1), sm.OpenChallenges(addr))

	var next uint64
	for id := rejectedAt + 1; id <= 40; id++ {
		if id%4 != uint64(flooder) {
			next = id
			break
		}
	}
	require.NoError(t, applyNext(sm, validationTx(t, hosts, escrowID, next, flooder, false)))
	require.Equal(t, uint32(challengeLimitBase), sm.OpenChallenges(addr))

	// Other members have their own limit.
	other := uint32(0)
	for id := uint64(1); id <= 40; id++ {
		r, _ := sm.GetInference(id)
		if r.Status == types.StatusFinished && id%4 != uint64(other) && !r.ValidatedBy.IsSet(other) {
			require.NoError(t, applyNext(sm, validationTx(t, hosts, escrowID, id, other, false)))
			break
		}
	}
	require.Equal(t, uint32(1), sm.OpenChallenges(sm.slotToAddress[other]))
}

// A rejected diff must not leave its challenge counted, and a restored
// machine must count the same open challenges with the same state root.
func TestChallengeLimit_UndoAndRestore(t *testing.T) {
	sm, hosts, escrowID, fresh := newChallengeLimitMachines(t, 8)
	const challenger = uint32(1)
	addr := sm.slotToAddress[challenger]

	bad := validationTx(t, hosts, escrowID, 2, 2, false) // executor validating itself
	require.Error(t, applyNext(sm, validationTx(t, hosts, escrowID, 4, challenger, false), bad))
	require.Zero(t, sm.OpenChallenges(addr))
	rec, _ := sm.GetInference(4)
	require.Zero(t, rec.Challenger)

	require.NoError(t, applyNext(sm, validationTx(t, hosts, escrowID, 4, challenger, false)))
	require.Equal(t, uint32(1), sm.OpenChallenges(addr))
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)

	data, err := types.MarshalStateSnapshotProto(sm.ExportState(), nil, nil, sm.ExportHeightSyncFloor())
	require.NoError(t, err)
	st, _, _, floorProto, err := types.UnmarshalStateSnapshotProto(data)
	require.NoError(t, err)
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, floorProto)
	require.NoError(t, err)
	restored := fresh()
	require.NoError(t, restored.RestoreStateWithFloor(st, floor))
	require.Equal(t, uint32(1), restored.OpenChallenges(addr))
	rec, _ = restored.GetInference(4)
	require.Equal(t, challenger+1, rec.Challenger)
	got, err := restored.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, root, got)
}
