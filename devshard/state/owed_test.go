package state

import (
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/types"
)

// TestOwedValidationSetMatchesScan drives random diffs through direct apply,
// rejected apply, trial apply (preview and validate, committed and abandoned),
// seal, and restore. After every step the journaled set must equal a fresh
// scan with the same predicate, and the owed gauge must report that size.
func TestOwedValidationSetMatchesScan(t *testing.T) {
	const escrowID = "escrow-owed"
	hosts := make([]*signing.Secp256k1Signer, 5)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		TokenPrice:                1,
		VoteThreshold:             0,
		ValidationRate:            5000,
		InferenceSealGraceNonces:  4,
		InferenceSealGraceSeconds: testutil.TestInferenceSealGraceSeconds,
		AutoSealEveryNNonces:      1,
		ExecutionTimeout:          0,
	}
	newSM := func() *StateMachine {
		t.Helper()
		sm, err := NewStateMachine(escrowID, config, group, 1_000_000, user.Address(),
			signing.NewSecp256k1Verifier(),
			testutil.MustMemoryStore(t, escrowID, user.Address(), config, group, 1_000_000))
		require.NoError(t, err)
		return sm
	}

	// Slot 0 is the host. Sampling uses a fixed seed so the scan and the set
	// share one rule, including ids this host must not validate.
	const seed int64 = 42
	mySlots := map[uint32]bool{0: true}
	pred := func(id uint64, rec *types.InferenceRecord, rate uint32) bool {
		return OwedValidation(seed, id, rec, mySlots, 1, 1, uint32(len(group)), rate)
	}

	sm := newSM()
	sm.SetOwedValidationPredicate(pred)
	assertOwedMatchesScan(t, sm, pred)

	// Confirm then execution-timeout. The confirm gives the state clock a
	// ConfirmedAt, which auto-seal requires before it will fold anything.
	// The terminal record then seals once the nonce gate clears.
	require.NoError(t, applyOwed(sm, 1, []*types.DevshardTx{startTx(1)}))
	assertOwedMatchesScan(t, sm, pred)
	require.NoError(t, applyOwed(sm, 2, []*types.DevshardTx{confirmTx(t, hosts, escrowID, 1)}))
	assertOwedMatchesScan(t, sm, pred)
	require.NoError(t, applyOwed(sm, 3, []*types.DevshardTx{
		timeoutTx(t, hosts, escrowID, 1, types.TimeoutReason_TIMEOUT_REASON_EXECUTION),
	}))
	assertOwedMatchesScan(t, sm, pred)
	for nonce := uint64(4); nonce <= 5; nonce++ {
		require.NoError(t, applyOwed(sm, nonce, nil))
		assertOwedMatchesScan(t, sm, pred)
	}
	require.Greater(t, sm.SealedNonceCount(), 0, "terminal inference must seal and leave the set")
	require.NotContains(t, sm.OwedValidationIDs(), uint64(1))

	rng := rand.New(rand.NewPCG(42, 7))
	var rejected, abandoned, previewCommits, validateCommits int
	nonce := uint64(6)
	for step := 0; step < 48; step++ {
		action := nextOwedAction(t, rng, sm, hosts, escrowID, nonce)
		switch {
		case action.reject:
			before := owedIDSet(sm)
			_, err := sm.ApplyLocal(nonce, action.txs)
			require.Error(t, err, "step %d %s", step, action.name)
			require.Equal(t, before, owedIDSet(sm), "rejected %s mutated the owed set", action.name)
			assertOwedMatchesScan(t, sm, pred)
			rejected++
			continue
		}

		before := owedIDSet(sm)
		vd, err := sm.PreviewLocalBestEffort(nonce, action.txs)
		require.NoError(t, err, "step %d preview %s", step, action.name)
		require.Len(t, vd.Applied, len(action.txs), "step %d preview %s dropped a tx", step, action.name)
		require.Equal(t, before, owedIDSet(sm), "preview %s changed the live set", action.name)
		assertOwedMatchesScan(t, sm, pred)

		switch rng.IntN(3) {
		case 0:
			_, err = sm.ApplyLocal(nonce, action.txs)
			require.NoError(t, err, "step %d apply %s", step, action.name)
			abandoned++
		case 1:
			require.True(t, sm.CommitValidated(vd), "step %d commit %s", step, action.name)
			previewCommits++
		default:
			signed := testutil.SignDiffWithRoot(t, user, escrowID, nonce, action.txs, vd.Root)
			validated, err := sm.ValidateDiff(signed)
			require.NoError(t, err, "step %d validate %s", step, action.name)
			require.Equal(t, before, owedIDSet(sm), "validate %s changed the live set", action.name)
			require.True(t, sm.CommitValidated(validated), "step %d validate-commit %s", step, action.name)
			validateCommits++
		}
		assertOwedMatchesScan(t, sm, pred)
		require.Nil(t, sm.journal)
		nonce++
	}

	require.NotZero(t, rejected)
	require.NotZero(t, abandoned)
	require.NotZero(t, previewCommits)
	require.NotZero(t, validateCommits)
	require.Greater(t, sm.SealedNonceCount(), 0)

	// Restore onto a machine that already owes something else. The rebuilt
	// set must follow the restored live map, not the entries it had before.
	sm2 := newSM()
	sm2.SetOwedValidationPredicate(pred)
	require.NoError(t, applyOwed(sm2, 1, []*types.DevshardTx{startTx(1)}))
	require.NoError(t, applyOwed(sm2, 2, []*types.DevshardTx{confirmTx(t, hosts, escrowID, 1)}))
	require.NoError(t, applyOwed(sm2, 3, []*types.DevshardTx{finishTx(t, hosts, escrowID, 1)}))
	assertOwedMatchesScan(t, sm2, pred)
	sm2.RestoreState(sm.ExportState())
	assertOwedMatchesScan(t, sm2, pred)
	require.Equal(t, owedIDSet(sm), owedIDSet(sm2))

	// Finalize with a challenged record still owed. The settlement drain seals
	// every live record, so the set must empty with it.
	for nonce%uint64(len(hosts)) == 0 {
		require.NoError(t, applyOwed(sm, nonce, nil))
		nonce++
	}
	disputed := nonce
	require.NoError(t, applyOwed(sm, nonce, []*types.DevshardTx{startTx(disputed)}))
	require.NoError(t, applyOwed(sm, nonce+1, []*types.DevshardTx{confirmTx(t, hosts, escrowID, disputed)}))
	challenger := uint32((disputed + 1) % uint64(len(hosts)))
	if challenger == 0 {
		challenger = 1
	}
	require.NoError(t, applyOwed(sm, nonce+2, []*types.DevshardTx{
		finishTx(t, hosts, escrowID, disputed),
		validationTx(t, hosts, escrowID, disputed, challenger, false),
	}))
	nonce += 3
	assertOwedMatchesScan(t, sm, pred)
	require.Contains(t, sm.OwedValidationIDs(), disputed)

	require.NoError(t, applyOwed(sm, nonce, []*types.DevshardTx{txFinalize()}))
	for sm.Phase() != types.PhaseSettlement {
		assertOwedMatchesScan(t, sm, pred)
		nonce++
		require.NoError(t, applyOwed(sm, nonce, nil))
	}
	assertOwedMatchesScan(t, sm, pred)
	require.Empty(t, sm.OwedValidationIDs())
	require.Zero(t, owedGauge(t, escrowID))
}

type owedAction struct {
	name   string
	txs    []*types.DevshardTx
	reject bool
}

func nextOwedAction(t *testing.T, rng *rand.Rand, sm *StateMachine, hosts []*signing.Secp256k1Signer, escrowID string, nonce uint64) owedAction {
	t.Helper()
	st := sm.SnapshotState()
	var pending, started, finished, challenged []uint64
	for id, rec := range st.Inferences {
		switch rec.Status {
		case types.StatusPending:
			pending = append(pending, id)
		case types.StatusStarted:
			started = append(started, id)
		case types.StatusFinished:
			finished = append(finished, id)
		case types.StatusChallenged:
			challenged = append(challenged, id)
		}
	}
	roll := rng.IntN(100)
	switch {
	case roll < 15:
		if len(started) > 0 {
			id := started[rng.IntN(len(started))]
			return owedAction{"finish-then-missing-validation", []*types.DevshardTx{
				finishTx(t, hosts, escrowID, id),
				validationTx(t, hosts, escrowID, 999_999, 0, true),
			}, true}
		}
		return owedAction{"missing-validation", []*types.DevshardTx{
			validationTx(t, hosts, escrowID, 999_999, 0, true),
		}, true}
	case roll < 30 && len(pending) > 0:
		return owedAction{"confirm", []*types.DevshardTx{confirmTx(t, hosts, escrowID, pending[rng.IntN(len(pending))])}, false}
	case roll < 45 && len(started) > 0:
		id := started[rng.IntN(len(started))]
		if rng.IntN(2) == 0 {
			slot := owedOtherSlot(rng, st.Inferences[id].ExecutorSlot, len(hosts))
			return owedAction{"finish-and-validate", []*types.DevshardTx{
				finishTx(t, hosts, escrowID, id),
				validationTx(t, hosts, escrowID, id, slot, rng.IntN(2) == 0),
			}, false}
		}
		return owedAction{"finish", []*types.DevshardTx{finishTx(t, hosts, escrowID, id)}, false}
	case roll < 60 && len(finished) > 0:
		id := finished[rng.IntN(len(finished))]
		slot := owedOtherSlot(rng, st.Inferences[id].ExecutorSlot, len(hosts))
		return owedAction{"validation", []*types.DevshardTx{
			validationTx(t, hosts, escrowID, id, slot, rng.IntN(3) != 0),
		}, false}
	case roll < 72 && len(challenged) > 0:
		id := challenged[rng.IntN(len(challenged))]
		slot, ok := owedFreeSlot(rng, st.Inferences[id], len(hosts))
		if !ok {
			return owedAction{"tick", nil, false}
		}
		return owedAction{"vote", []*types.DevshardTx{
			voteTx(t, hosts, escrowID, id, slot, rng.IntN(2) == 0),
		}, false}
	case roll < 82 && len(pending) > 0:
		return owedAction{"timeout-refused", []*types.DevshardTx{
			timeoutTx(t, hosts, escrowID, pending[rng.IntN(len(pending))], types.TimeoutReason_TIMEOUT_REASON_REFUSED),
		}, false}
	case roll < 90 && len(started) > 0:
		return owedAction{"timeout-execution", []*types.DevshardTx{
			timeoutTx(t, hosts, escrowID, started[rng.IntN(len(started))], types.TimeoutReason_TIMEOUT_REASON_EXECUTION),
		}, false}
	case roll < 97:
		return owedAction{"tick", nil, false}
	default:
		return owedAction{"start", []*types.DevshardTx{startTx(nonce)}, false}
	}
}

func owedOtherSlot(rng *rand.Rand, executor uint32, n int) uint32 {
	slot := uint32(rng.IntN(n - 1))
	if slot >= executor {
		slot++
	}
	return slot
}

func owedFreeSlot(rng *rand.Rand, rec *types.InferenceRecord, n int) (uint32, bool) {
	var free []uint32
	for slot := uint32(0); slot < uint32(n); slot++ {
		if !rec.ValidatedBy.IsSet(slot) {
			free = append(free, slot)
		}
	}
	if len(free) == 0 {
		return 0, false
	}
	return free[rng.IntN(len(free))], true
}

func applyOwed(sm *StateMachine, nonce uint64, txs []*types.DevshardTx) error {
	_, err := sm.ApplyLocal(nonce, txs)
	return err
}

func owedIDSet(sm *StateMachine) map[uint64]struct{} {
	out := make(map[uint64]struct{})
	for _, id := range sm.OwedValidationIDs() {
		out[id] = struct{}{}
	}
	return out
}

func assertOwedMatchesScan(t *testing.T, sm *StateMachine, pred OwedValidationPredicate) {
	t.Helper()
	require.Nil(t, sm.journal)
	rate := sm.Config().ValidationRate
	st := sm.SnapshotState()
	want := make(map[uint64]struct{})
	for id, rec := range st.Inferences {
		if pred(id, rec, rate) {
			want[id] = struct{}{}
		}
	}
	require.Equal(t, want, owedIDSet(sm))
	require.Equal(t, float64(len(want)), owedGauge(t, st.EscrowID))
}

func owedGauge(t *testing.T, escrowID string) float64 {
	t.Helper()
	families, err := observability.Registry().Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "devshard_validation_owed" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "escrow_id" && label.GetValue() == escrowID {
					return metric.GetGauge().GetValue()
				}
			}
		}
	}
	return 0
}
