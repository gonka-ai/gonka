package host

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

// referenceValidationJobs is the selection collectValidationJobs made before
// the owed set: a walk over a full state snapshot. Caller must hold h.mu.
func referenceValidationJobs(h *Host) map[uint64]validationFlow {
	st := h.sm.SnapshotState()
	want := make(map[uint64]validationFlow)
	for id, rec := range st.Inferences {
		if !devshard.CanValidate(h.validator, rec.Model) {
			continue
		}
		if rec.Status != types.StatusFinished && rec.Status != types.StatusChallenged {
			continue
		}
		if h.slotIDs[rec.ExecutorSlot] {
			continue
		}
		participated := false
		for slot := range h.slotIDs {
			if rec.ValidatedBy.IsSet(slot) {
				participated = true
			}
		}
		if participated {
			continue
		}
		if _, ok := h.validating[id]; ok {
			continue
		}
		if time.Now().Before(h.validationRetryAt[id]) {
			continue
		}
		if h.hasMempoolValidationOrVote(id) {
			continue
		}
		flow := validationFlowChallenged
		if rec.Status == types.StatusFinished {
			executorSlots := h.sm.AddressSlotCount(h.slotToAddr[rec.ExecutorSlot])
			if !state.ShouldValidate(h.ownSeed, id, uint32(len(h.slotIDs)), executorSlots, h.sm.TotalSlots(), st.Config.ValidationRate) {
				continue
			}
			flow = validationFlowShouldValidate
		}
		want[id] = flow
	}
	return want
}

// TestHost_CollectValidationJobs_MatchesFullScan checks the owed-set walk
// against the old snapshot walk while inferences finish, get validated,
// challenged, voted, and sealed. The host owns two slots, so sampling and
// participation run over a multi-slot address, and sampling is below 100%.
// Each step also seeds in-flight, cooldown, hold, and mempool entries.
func TestHost_CollectValidationJobs_MatchesFullScan(t *testing.T) {
	const escrowID = "escrow-1"
	a, b, c, d := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	slotSigners := []*signing.Secp256k1Signer{a, b, c, a, d}
	group := testutil.MakeGroup(slotSigners)
	user := testutil.MustGenerateKey(t)
	config := types.SessionConfig{
		RefusalTimeout:            60,
		ExecutionTimeout:          1200,
		TokenPrice:                1,
		VoteThreshold:             3,
		ValidationRate:            4000,
		InferenceSealGraceNonces:  6,
		InferenceSealGraceSeconds: testutil.TestInferenceSealGraceSeconds,
		AutoSealEveryNNonces:      1,
	}
	const balance = 100_000_000
	sm, err := state.NewStateMachine(escrowID, config, group, balance, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, escrowID, user.Address(), config, group, balance))
	require.NoError(t, err)
	h, err := NewHost(sm, a, stub.NewInferenceEngine(), escrowID, group, nil,
		WithGrace(10), WithValidator(stub.NewValidationEngine()), WithEpochID(1))
	require.NoError(t, err)
	require.Equal(t, map[uint32]bool{0: true, 3: true}, h.slotIDs)
	h.validationLifecycleMu.Lock()
	h.validationQueue = make(chan validateJob, defaultValidationQueueSize)
	h.validationLifecycleMu.Unlock()

	executorOf := func(id uint64) uint32 { return uint32(id % uint64(len(group))) }
	confirmTx := func(id uint64) *types.DevshardTx {
		confirmedAt := int64(2000 + 100*id)
		sig := testutil.SignExecutorReceipt(t, slotSigners[executorOf(id)], escrowID, id,
			testutil.TestPromptHash[:], "llama", 100, 50, 1000, confirmedAt)
		return &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: id, ExecutorSig: sig, ConfirmedAt: confirmedAt,
		}}}
	}
	finishTx := func(id uint64) *types.DevshardTx {
		slot := executorOf(id)
		msg := &types.MsgFinishInference{
			InferenceId: id, ResponseHash: []byte("response"), InputTokens: 80, OutputTokens: 40,
			ExecutorSlot: slot, EscrowId: escrowID,
		}
		msg.ProposerSig = testutil.SignProposerTx(t, slotSigners[slot], msg)
		return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
	}
	validationTx := func(id uint64, slot uint32, valid bool) *types.DevshardTx {
		msg := &types.MsgValidation{InferenceId: id, ValidatorSlot: slot, Valid: valid, EscrowId: escrowID}
		msg.ProposerSig = testutil.SignProposerTx(t, slotSigners[slot], msg)
		return &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}
	}
	voteTx := func(id uint64, slot uint32, valid bool) *types.DevshardTx {
		msg := &types.MsgValidationVote{InferenceId: id, VoterSlot: slot, VoteValid: valid, EscrowId: escrowID}
		msg.ProposerSig = testutil.SignProposerTx(t, slotSigners[slot], msg)
		return &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: msg}}
	}

	rng := rand.New(rand.NewPCG(7, 11))
	const steps = 90
	var everFinished, everChallenged, everOwnParticipation bool
	flows := make(map[validationFlow]int)
	for n := uint64(1); n <= steps; n++ {
		txs := []*types.DevshardTx{testutil.StartTx(n)}
		if n > 1 {
			txs = append(txs, confirmTx(n-1))
		}
		if n > 2 {
			txs = append(txs, finishTx(n-2))
		}
		var targets []uint64
		for id, rec := range sm.SnapshotInferences() {
			if rec.Status == types.StatusFinished || rec.Status == types.StatusChallenged {
				targets = append(targets, id)
			}
		}
		slices.Sort(targets)
		if len(targets) > 0 {
			for k := rng.IntN(3); k > 0; k-- {
				id := targets[rng.IntN(len(targets))]
				// Mostly other addresses, so challenges stay open long
				// enough for this host to owe them.
				others := []uint32{1, 2, 4}
				slot := others[rng.IntN(len(others))]
				if rng.IntN(6) == 0 {
					slot = []uint32{0, 3}[rng.IntN(2)]
				}
				if rng.IntN(2) == 0 {
					txs = append(txs, validationTx(id, slot, rng.IntN(4) != 0))
				} else {
					txs = append(txs, voteTx(id, slot, rng.IntN(3) != 0))
				}
			}
		}
		_, _, err := sm.ApplyLocalBestEffort(n, txs)
		require.NoError(t, err, "nonce %d", n)

		live := sm.SnapshotInferences()
		ids := make([]uint64, 0, len(live))
		for id, rec := range live {
			ids = append(ids, id)
			switch rec.Status {
			case types.StatusFinished:
				everFinished = true
			case types.StatusChallenged:
				everChallenged = true
			}
			if rec.ValidatedBy.IsSet(0) || rec.ValidatedBy.IsSet(3) {
				everOwnParticipation = true
			}
		}

		h.mu.Lock()
		clear(h.validating)
		if h.validationRetryAt == nil {
			h.validationRetryAt = make(map[uint64]time.Time)
		} else {
			clear(h.validationRetryAt)
		}
		h.mempool.RemoveIncluded(h.mempool.Txs())
		if len(ids) > 0 {
			pick := func() uint64 { return ids[rng.IntN(len(ids))] }
			h.validating[pick()] = struct{}{}
			h.validationRetryAt[pick()] = time.Now().Add(time.Hour)
			h.validationRetryAt[pick()] = time.Now().Add(-time.Second)
			h.mempool.AddTx(validationTx(pick(), 3, true))
			h.mempool.AddTx(voteTx(pick(), 0, true))
			h.mempool.AddTx(validationTx(pick(), 1, true))
		}
		want := referenceValidationJobs(h)
		jobs := h.collectValidationJobs()
		h.mu.Unlock()

		got := make(map[uint64]validationFlow, len(jobs))
		for _, job := range jobs {
			_, dup := got[job.inferenceID]
			require.False(t, dup, "nonce %d: inference %d collected twice", n, job.inferenceID)
			got[job.inferenceID] = job.flow
			require.Equal(t, uint32(0), job.validatorSlot)
			require.Equal(t, h.slotToAddr[executorOf(job.inferenceID)], job.executorAddress)
		}
		require.Equal(t, want, got, "nonce %d", n)
		for _, flow := range got {
			flows[flow]++
		}
	}
	require.Positive(t, flows[validationFlowShouldValidate], "sampled finished records must be collected")
	require.Positive(t, flows[validationFlowChallenged], "challenged records must be collected")

	require.Positive(t, sm.SealedNonceCount(), "the run must seal records out of the owed set")
	require.NotEmpty(t, sm.SnapshotInferences())
	require.True(t, everFinished && everChallenged && everOwnParticipation,
		"the run must reach finished, challenged, and own-participation records")
}
