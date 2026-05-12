package host

import (
	"context"
	"sync"
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

// recordingPruneSink captures all InferencePruneEvent emissions for assertions.
type recordingPruneSink struct {
	mu     sync.Mutex
	events []InferencePruneEvent
}

func (s *recordingPruneSink) OnInferencePrunable(event InferencePruneEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *recordingPruneSink) snapshot() []InferencePruneEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]InferencePruneEvent(nil), s.events...)
}

func (s *recordingPruneSink) findFor(inferenceID uint64) []InferencePruneEvent {
	out := []InferencePruneEvent{}
	for _, e := range s.snapshot() {
		if e.InferenceID == inferenceID {
			out = append(out, e)
		}
	}
	return out
}

// pruneTestRig owns the shared bookkeeping for prune-sink scenarios so each
// test can express only its session-level moves (finish, validate, timeout).
type pruneTestRig struct {
	t        *testing.T
	hosts    []*signing.Secp256k1Signer
	user     *signing.Secp256k1Signer
	group    []types.SlotAssignment
	config   types.SessionConfig
	host     *Host
	sink     *recordingPruneSink
	stub     *stub.InferenceEngine
	escrowID string
	epochID  uint64
}

// newPruneRig wires a Host backed by a recordingPruneSink. observerIdx selects
// which group member runs locally; pickng a non-executor avoids interference
// from the executor receipt path.
func newPruneRig(t *testing.T, observerIdx, numHosts int, opts ...HostOption) *pruneTestRig {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(numHosts) / 2,
		// ValidationRate=0 + no WithValidator means no async validation
		// will sneak in and emit unrelated mempool entries.
		ValidationRate: 0,
	}
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), verifier)
	require.NoError(t, err)

	sink := &recordingPruneSink{}
	stubEngine := stub.NewInferenceEngine()
	const epochID uint64 = 7
	allOpts := []HostOption{
		WithPruneSink(sink),
		WithEpochID(epochID),
		WithGrace(0),
	}
	allOpts = append(allOpts, opts...)
	h, err := NewHost(sm, hosts[observerIdx], stubEngine, "escrow-1", group, nil, allOpts...)
	require.NoError(t, err)
	return &pruneTestRig{
		t:        t,
		hosts:    hosts,
		user:     user,
		group:    group,
		config:   config,
		host:     h,
		sink:     sink,
		stub:     stubEngine,
		escrowID: "escrow-1",
		epochID:  epochID,
	}
}

// applyDiff signs and applies a diff at the given nonce, asserting success.
func (r *pruneTestRig) applyDiff(nonce uint64, txs []*types.DevshardTx) {
	r.t.Helper()
	d := testutil.SignDiff(r.t, r.user, r.escrowID, nonce, txs)
	_, err := r.host.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d}})
	require.NoError(r.t, err)
}

// driveStartConfirmFinish brings inferenceID through Pending -> Started -> Finished.
// startNonce is consumed for MsgStartInference; the next two diffs use startNonce+1
// and startNonce+2.
func (r *pruneTestRig) driveStartConfirmFinish(inferenceID, startNonce uint64) uint64 {
	r.t.Helper()
	executorSlot := uint32(inferenceID % uint64(len(r.group)))
	executorSigner := r.hosts[executorSlot]
	confirmedAt := int64(2000) + int64(inferenceID)

	r.applyDiff(startNonce, []*types.DevshardTx{testutil.StartTx(inferenceID)})

	execSig := testutil.SignExecutorReceipt(r.t, executorSigner, r.escrowID, inferenceID,
		testutil.TestPromptHash[:], "llama", 100, 50, 1000, confirmedAt)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: confirmedAt,
	}}}
	r.applyDiff(startNonce+1, []*types.DevshardTx{confirmTx})

	finishMsg := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		ResponseHash: r.stub.ResponseHash,
		InputTokens:  r.stub.InputTokens,
		OutputTokens: r.stub.OutputTokens,
		ExecutorSlot: executorSlot,
		EscrowId:     r.escrowID,
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(r.t, executorSigner, finishMsg)
	finishTx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}
	r.applyDiff(startNonce+2, []*types.DevshardTx{finishTx})

	return startNonce + 3
}

// signValidation builds a MsgValidation tx signed by validatorSlot's owner.
func (r *pruneTestRig) signValidation(inferenceID uint64, validatorSlot uint32, valid bool) *types.DevshardTx {
	r.t.Helper()
	signer := r.hosts[validatorSlot]
	msg := &types.MsgValidation{
		InferenceId:   inferenceID,
		ValidatorSlot: validatorSlot,
		Valid:         valid,
		EscrowId:      r.escrowID,
	}
	msg.ProposerSig = testutil.SignProposerTx(r.t, signer, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}
}

// signValidationVote builds a MsgValidationVote tx signed by voterSlot's owner.
func (r *pruneTestRig) signValidationVote(inferenceID uint64, voterSlot uint32, valid bool) *types.DevshardTx {
	r.t.Helper()
	signer := r.hosts[voterSlot]
	msg := &types.MsgValidationVote{
		InferenceId: inferenceID,
		VoterSlot:   voterSlot,
		VoteValid:   valid,
		EscrowId:    r.escrowID,
	}
	msg.ProposerSig = testutil.SignProposerTx(r.t, signer, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: msg}}
}

// signTimeoutInference builds a MsgTimeoutInference tx with accept votes from
// the supplied voter slots.
func (r *pruneTestRig) signTimeoutInference(inferenceID uint64, reason types.TimeoutReason, voterSlots []uint32) *types.DevshardTx {
	r.t.Helper()
	votes := make([]*types.TimeoutVote, 0, len(voterSlots))
	for _, slot := range voterSlots {
		v := testutil.SignTimeoutVote(r.t, r.hosts[slot], r.escrowID, inferenceID, reason, true)
		v.VoterSlot = slot
		votes = append(votes, v)
	}
	return &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
		InferenceId: inferenceID,
		Reason:      reason,
		Votes:       votes,
	}}}
}

// inferenceStatus reads the post-apply status of inferenceID via the host's
// state machine snapshot.
func (r *pruneTestRig) inferenceStatus(inferenceID uint64) types.InferenceStatus {
	st := r.host.SnapshotState()
	rec, ok := st.Inferences[inferenceID]
	require.True(r.t, ok, "inference %d should exist", inferenceID)
	return rec.Status
}

func TestHost_PruneSink_Terminal_OnValidated(t *testing.T) {
	rig := newPruneRig(t, 0, 5)

	// Inference 1: executor=slot 1; validators slot 0,2,3,4. Threshold=2.
	nonce := rig.driveStartConfirmFinish(1, 1)

	// Validation flips Finished -> Challenged with one invalid vote.
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidation(1, 2, false)})
	nonce++
	require.Equal(t, types.StatusChallenged, rig.inferenceStatus(1))
	require.Empty(t, rig.sink.findFor(1), "no Tier A while still Challenged")

	// Two valid votes are not enough (threshold=2, need >2).
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidationVote(1, 0, true)})
	nonce++
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidationVote(1, 3, true)})
	nonce++
	require.Equal(t, types.StatusChallenged, rig.inferenceStatus(1))
	require.Empty(t, rig.sink.findFor(1))

	// Third valid vote pushes VotesValid=3 > threshold=2 -> StatusValidated.
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidationVote(1, 4, true)})
	require.Equal(t, types.StatusValidated, rig.inferenceStatus(1))

	events := rig.sink.findFor(1)
	require.Len(t, events, 1, "exactly one Tier A on Validated flip")
	require.Equal(t, PruneReasonTerminal, events[0].Reason)
	require.Equal(t, rig.escrowID, events[0].EscrowID)
	require.Equal(t, rig.epochID, events[0].PayloadEpoch)
	require.True(t, events[0].PayloadEpochKnown)
}

func TestHost_PruneSink_Terminal_OnInvalidated(t *testing.T) {
	rig := newPruneRig(t, 0, 5)

	nonce := rig.driveStartConfirmFinish(1, 1)

	// Validation injects 1 invalid vote -> Challenged.
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidation(1, 2, false)})
	nonce++
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidationVote(1, 0, false)})
	nonce++
	require.Equal(t, types.StatusChallenged, rig.inferenceStatus(1))
	require.Empty(t, rig.sink.findFor(1))

	// Third invalid vote: VotesInvalid=3 > threshold=2 -> StatusInvalidated.
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidationVote(1, 3, false)})
	require.Equal(t, types.StatusInvalidated, rig.inferenceStatus(1))

	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonTerminal, events[0].Reason)
}

func TestHost_PruneSink_Terminal_OnTimedOut(t *testing.T) {
	rig := newPruneRig(t, 0, 5)

	// Start inference 1, then timeout from Pending (no ConfirmStart).
	rig.applyDiff(1, []*types.DevshardTx{testutil.StartTx(1)})
	require.Equal(t, types.StatusPending, rig.inferenceStatus(1))

	// Threshold=2 -> need >2 accept votes; collect 3 from non-executor slots.
	timeoutTx := rig.signTimeoutInference(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, []uint32{0, 2, 3})
	rig.applyDiff(2, []*types.DevshardTx{timeoutTx})
	require.Equal(t, types.StatusTimedOut, rig.inferenceStatus(1))

	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonTerminal, events[0].Reason)
}

func TestHost_PruneSink_StaleFinished_TierC(t *testing.T) {
	rig := newPruneRig(t, 0, 5,
		WithPruneTuning(2, 10*time.Millisecond),
	)

	// Finish inference 1 at nonce 3 (Start=1, Confirm=2, Finish=3).
	nonce := rig.driveStartConfirmFinish(1, 1)
	require.Equal(t, types.StatusFinished, rig.inferenceStatus(1))
	require.Empty(t, rig.sink.findFor(1), "Finish itself does not emit Tier A")

	// Wall-clock gate: wait past the configured grace.
	time.Sleep(20 * time.Millisecond)

	// Nonce gate not yet met: nonce=4 < finishNonce(3)+grace(2)=5.
	rig.applyDiff(nonce, nil)
	nonce++
	require.Empty(t, rig.sink.findFor(1), "nonce gate should still hold")

	// Crossing the gate (nonce=5 >= 5) should fire exactly one Tier C.
	rig.applyDiff(nonce, nil)
	nonce++
	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonStaleFinished, events[0].Reason)
	require.Equal(t, rig.epochID, events[0].PayloadEpoch)

	// Subsequent diffs must not re-emit for the same inference.
	rig.applyDiff(nonce, nil)
	require.Len(t, rig.sink.findFor(1), 1, "Tier C must dedupe across later diffs")
}

func TestHost_PruneSink_TierC_DoesNotFireWhenNonceGateUnmet(t *testing.T) {
	rig := newPruneRig(t, 0, 5,
		WithPruneTuning(50, 1*time.Microsecond),
	)
	nonce := rig.driveStartConfirmFinish(1, 1)
	time.Sleep(2 * time.Millisecond) // wall-clock gate easily satisfied.
	for i := 0; i < 5; i++ {
		rig.applyDiff(nonce, nil)
		nonce++
	}
	require.Empty(t, rig.sink.findFor(1), "nonce gate not yet crossed")
}

func TestHost_PruneSink_TierC_DoesNotFireWhenWallClockGateUnmet(t *testing.T) {
	rig := newPruneRig(t, 0, 5,
		WithPruneTuning(2, time.Hour),
	)
	nonce := rig.driveStartConfirmFinish(1, 1)
	for i := 0; i < 4; i++ {
		rig.applyDiff(nonce, nil)
		nonce++
	}
	require.Empty(t, rig.sink.findFor(1), "wall-clock gate prevents Tier C")
}

func TestHost_PruneSink_TerminalFlipPreemptsTierC(t *testing.T) {
	// If an inference terminalizes before the Tier C gates expire, only one
	// Tier A event should fire and the Tier C ledger entry must be cleared.
	rig := newPruneRig(t, 0, 5,
		WithPruneTuning(50, time.Hour),
	)
	nonce := rig.driveStartConfirmFinish(1, 1)
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidation(1, 2, false)})
	nonce++
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidationVote(1, 0, false)})
	nonce++
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidationVote(1, 3, false)})

	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonTerminal, events[0].Reason)

	// finishedAt entry must be gone.
	rig.host.mu.Lock()
	_, stillTracked := rig.host.finishedAt[1]
	rig.host.mu.Unlock()
	require.False(t, stillTracked, "Tier C ledger must drop terminalized inference")
}

func TestHost_PruneSink_NilSafe_NoEmission(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout: 60, ExecutionTimeout: 1200, TokenPrice: 1,
		VoteThreshold: 2, ValidationRate: 0,
	}
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), verifier)
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithEpochID(7),
		WithGrace(0),
	)
	require.NoError(t, err)

	// Drive a full Finish without panicking. Sink is nil so this exercises the
	// processPruneEventsLocked early-return path.
	rig := &pruneTestRig{
		t: t, hosts: hosts, user: user, group: group, config: config,
		host: h, stub: stub.NewInferenceEngine(), escrowID: "escrow-1", epochID: 7,
	}
	_ = rig.driveStartConfirmFinish(1, 1)
	require.Equal(t, types.StatusFinished, rig.inferenceStatus(1))
}

func TestHost_ValidateAsync_SkippedDoesNotEnqueueValidation(t *testing.T) {
	// 2 hosts so validation rate=100% always picks. Host 0 validates inferences
	// where host 1 (slot 1) is the executor.
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout: 60, ExecutionTimeout: 1200, TokenPrice: 1,
		VoteThreshold: 1, ValidationRate: 10000,
	}
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier)
	require.NoError(t, err)

	skipper := &skippingValidator{}
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil,
		WithGrace(10), WithValidator(skipper), WithEpochID(42))
	require.NoError(t, err)

	// Start inference 1 (executor = slot 1).
	rig := &pruneTestRig{
		t: t, hosts: hosts, user: user, group: group, config: config,
		host: h, stub: engine, escrowID: "escrow-1", epochID: 42,
	}
	_ = rig.driveStartConfirmFinish(1, 1)

	// Wait for the validator to be invoked (validation worker is async).
	require.Eventually(t, func() bool {
		return skipper.getCalls() > 0
	}, 2*time.Second, 10*time.Millisecond, "validator should be invoked")

	// Give the goroutine a moment to settle after returning the skip error.
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		_, stillFlagged := h.validating[1]
		return !stillFlagged
	}, 2*time.Second, 10*time.Millisecond, "validating[id] must be cleared on skip")

	// No MsgValidation/MsgValidationVote for inference 1 should reach the mempool.
	for _, tx := range h.MempoolTxs() {
		if v := tx.GetValidation(); v != nil && v.InferenceId == 1 {
			t.Fatalf("MsgValidation must not be queued when validator returns ErrValidationSkipped")
		}
		if v := tx.GetValidationVote(); v != nil && v.InferenceId == 1 {
			t.Fatalf("MsgValidationVote must not be queued when validator returns ErrValidationSkipped")
		}
	}
}

// skippingValidator wraps ErrValidationSkipped without an extra package.
type skippingValidator struct {
	mu    sync.Mutex
	calls int
}

func (e *skippingValidator) Validate(_ context.Context, _ devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	// Wrap the sentinel through %w so errors.Is matches it on the host side.
	return nil, errSkippedWrapped
}

func (e *skippingValidator) getCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// errSkippedWrapped mirrors what shared_runtime.ValidateInferenceWithExecutor
// returns when the executor reports a 404 on payload retrieval.
var errSkippedWrapped = wrapSkipped()

func wrapSkipped() error {
	// Wrap to ensure errors.Is(err, devshard.ErrValidationSkipped) is true.
	return wrappedErr{base: devshard.ErrValidationSkipped}
}

type wrappedErr struct{ base error }

func (w wrappedErr) Error() string { return "validation skipped: " + w.base.Error() }
func (w wrappedErr) Unwrap() error { return w.base }
