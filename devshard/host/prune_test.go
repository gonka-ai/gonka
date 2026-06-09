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
	"devshard/storage"
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
	store    *storage.Memory
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
		RefusalTimeout:             60,
		ExecutionTimeout:           1200,
		TokenPrice:                 1,
		VoteThreshold:              uint32(numHosts) / 2,
		ValidationRate:             0,
		InferenceClearGraceSeconds: testutil.TestInferenceClearGraceSeconds,
		// ValidationRate=0 + no WithValidator means no async validation
		// will sneak in and emit unrelated mempool entries.
	}
	verifier := signing.NewSecp256k1Verifier()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		EpochID:        7,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 1_000_000,
	}))
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), verifier, store,
	)
	require.NoError(t, err)

	sink := &recordingPruneSink{}
	stubEngine := stub.NewInferenceEngine()
	const epochID uint64 = 7
	allOpts := []HostOption{
		WithPruneSink(sink),
		WithEpochID(epochID),
		WithGrace(0),
		// v2 defers terminal prune until grace gates clear; tests override as needed.
		WithPruneTuning(1, 10*time.Millisecond),
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
		store:    store,
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

func (r *pruneTestRig) inferenceMissing(inferenceID uint64) {
	r.t.Helper()
	st := r.host.SnapshotState()
	_, ok := st.Inferences[inferenceID]
	require.False(r.t, ok, "inference %d should be evicted from RAM", inferenceID)
}

func (r *pruneTestRig) sealedRow(inferenceID uint64) storage.InferenceRow {
	r.t.Helper()
	row, ok, err := r.store.GetSealedInference(r.escrowID, inferenceID)
	require.NoError(r.t, err)
	require.True(r.t, ok, "sealed inference %d should exist in storage", inferenceID)
	return row
}

// sealDiff signs and applies a seal-bearing diff (no txs) at the given nonce,
// folding sealedIDs into the accumulator. This is what the user sequencer emits
// once an inference has cleared its seal gates. Returns the next nonce.
func (r *pruneTestRig) sealDiff(nonce uint64, sealedIDs ...uint64) uint64 {
	r.t.Helper()
	d := testutil.SignDiffSealed(r.t, r.user, r.escrowID, nonce, nil, sealedIDs)
	_, err := r.host.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d}})
	require.NoError(r.t, err)
	return nonce + 1
}

// trySealDiff is like sealDiff but returns the HandleRequest error instead of
// asserting success, so admission rejections can be inspected.
func (r *pruneTestRig) trySealDiff(nonce uint64, sealedIDs ...uint64) error {
	r.t.Helper()
	d := testutil.SignDiffSealed(r.t, r.user, r.escrowID, nonce, nil, sealedIDs)
	_, err := r.host.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d}})
	return err
}

// driveToValidated brings inference 1 (executor slot 1) through to
// StatusValidated by collecting 3 valid votes (threshold=2). Returns next nonce.
func (r *pruneTestRig) driveToValidated(startNonce uint64) uint64 {
	r.t.Helper()
	nonce := r.driveStartConfirmFinish(1, startNonce)
	r.applyDiff(nonce, []*types.DevshardTx{r.signValidation(1, 2, false)})
	nonce++
	r.applyDiff(nonce, []*types.DevshardTx{r.signValidationVote(1, 0, true)})
	nonce++
	r.applyDiff(nonce, []*types.DevshardTx{r.signValidationVote(1, 3, true)})
	nonce++
	r.applyDiff(nonce, []*types.DevshardTx{r.signValidationVote(1, 4, true)})
	nonce++
	require.Equal(r.t, types.StatusValidated, r.inferenceStatus(1))
	return nonce
}

// driveToInvalidated brings inference 1 to StatusInvalidated with 3 invalid
// votes (threshold=2). Returns next nonce.
func (r *pruneTestRig) driveToInvalidated(startNonce uint64) uint64 {
	r.t.Helper()
	nonce := r.driveStartConfirmFinish(1, startNonce)
	r.applyDiff(nonce, []*types.DevshardTx{r.signValidation(1, 2, false)})
	nonce++
	r.applyDiff(nonce, []*types.DevshardTx{r.signValidationVote(1, 0, false)})
	nonce++
	r.applyDiff(nonce, []*types.DevshardTx{r.signValidationVote(1, 3, false)})
	nonce++
	require.Equal(r.t, types.StatusInvalidated, r.inferenceStatus(1))
	return nonce
}

// Pruning is now driven entirely by the seal set carried in the diff: the host
// emits a payload-prune event for each id in diff.SealedInferenceIDs after the
// deterministic fold. It no longer seals state autonomously on a wall clock.

func TestHost_PruneSink_EmitsOnSealedTerminal_Validated(t *testing.T) {
	rig := newPruneRig(t, 0, 5)
	nonce := rig.driveToValidated(1)

	// No prune until the diff explicitly seals the inference.
	require.Empty(t, rig.sink.findFor(1), "host must not prune before the diff seals")

	// Terminal seals are admitted with no grace.
	rig.sealDiff(nonce, 1)

	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonTerminal, events[0].Reason)
	require.Equal(t, rig.escrowID, events[0].EscrowID)
	require.Equal(t, rig.epochID, events[0].PayloadEpoch)
	require.True(t, events[0].PayloadEpochKnown)
	rig.inferenceMissing(1)
	require.NotZero(t, rig.sealedRow(1).SealedNonce)
}

func TestHost_PruneSink_EmitsOnSealedTerminal_Invalidated(t *testing.T) {
	rig := newPruneRig(t, 0, 5)
	nonce := rig.driveToInvalidated(1)
	require.Empty(t, rig.sink.findFor(1))

	rig.sealDiff(nonce, 1)

	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonTerminal, events[0].Reason)
	rig.inferenceMissing(1)
	require.NotZero(t, rig.sealedRow(1).SealedNonce)
}

func TestHost_PruneSink_EmitsOnSealedTerminal_TimedOut(t *testing.T) {
	rig := newPruneRig(t, 0, 5)

	rig.applyDiff(1, []*types.DevshardTx{testutil.StartTx(1)})
	require.Equal(t, types.StatusPending, rig.inferenceStatus(1))
	timeoutTx := rig.signTimeoutInference(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, []uint32{0, 2, 3})
	rig.applyDiff(2, []*types.DevshardTx{timeoutTx})
	require.Equal(t, types.StatusTimedOut, rig.inferenceStatus(1))
	require.Empty(t, rig.sink.findFor(1))

	rig.sealDiff(3, 1)

	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonTerminal, events[0].Reason)
	rig.inferenceMissing(1)
	require.NotZero(t, rig.sealedRow(1).SealedNonce)
}

func TestHost_PruneSink_EmitsOnSealedStaleFinished(t *testing.T) {
	// Small grace gates so the admission check accepts a Finished seal quickly.
	rig := newPruneRig(t, 0, 5,
		WithPruneTuning(2, 10*time.Millisecond),
	)
	nonce := rig.driveStartConfirmFinish(1, 1) // Finish at nonce 3, finishedAt=3.
	require.Equal(t, types.StatusFinished, rig.inferenceStatus(1))
	require.Empty(t, rig.sink.findFor(1), "Finish itself does not prune")

	// Clear the wall-clock grace (10ms, well within the 10s admission tolerance)
	// and advance to satisfy the nonce floor (finishedAt 3 + grace 2 = 5).
	time.Sleep(20 * time.Millisecond)
	rig.applyDiff(nonce, nil) // nonce 4 -> next 5
	nonce++

	// Now a Finished seal is admissible (nonce 5 >= 5).
	rig.sealDiff(nonce, 1)

	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonStaleFinished, events[0].Reason)
	require.Equal(t, rig.epochID, events[0].PayloadEpoch)
	rig.inferenceMissing(1)
	require.NotZero(t, rig.sealedRow(1).SealedNonce)
}

func TestHost_PruneSink_DedupesRepeatedSeal(t *testing.T) {
	rig := newPruneRig(t, 0, 5)
	nonce := rig.driveToValidated(1)
	nonce = rig.sealDiff(nonce, 1)
	require.Len(t, rig.sink.findFor(1), 1)

	// A later diff that names the same (already sealed) id must not re-emit:
	// it is no longer live, the fold is a no-op, and prunedFired dedupes.
	rig.sealDiff(nonce, 1)
	require.Len(t, rig.sink.findFor(1), 1, "prune must dedupe across diffs")
}

func TestHost_PruneSink_AdmissionRejectsTooEarlyFinished(t *testing.T) {
	// Large wall-clock grace so a freshly Finished inference cannot be sealed
	// yet: the host must reject the diff before applying or pruning it.
	rig := newPruneRig(t, 0, 5,
		WithPruneTuning(0, time.Hour),
	)
	nonce := rig.driveStartConfirmFinish(1, 1)
	require.Equal(t, types.StatusFinished, rig.inferenceStatus(1))

	err := rig.trySealDiff(nonce, 1)
	require.ErrorIs(t, err, types.ErrSealTooEarly)
	require.Empty(t, rig.sink.findFor(1), "rejected seal must not prune")
	require.Equal(t, types.StatusFinished, rig.inferenceStatus(1), "rejected seal must not mutate state")
}

func TestHost_PruneSink_NilSafe_NoEmission(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout:             60,
		ExecutionTimeout:           1200,
		TokenPrice:                 1,
		VoteThreshold:              2,
		ValidationRate:             0,
		InferenceClearGraceSeconds: testutil.TestInferenceClearGraceSeconds,
	}
	verifier := signing.NewSecp256k1Verifier()
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000)
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), verifier, store)
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithEpochID(7),
		WithGrace(0),
	)
	require.NoError(t, err)

	// Drive a full Finish and then seal without panicking. Sink is nil so this
	// exercises the emitSealPrunesLocked early-return path while the seal still
	// folds into state.
	rig := &pruneTestRig{
		t: t, hosts: hosts, user: user, group: group, config: config,
		host: h, stub: stub.NewInferenceEngine(), escrowID: "escrow-1", epochID: 7,
	}
	nonce := rig.driveToValidated(1)
	require.Equal(t, types.StatusValidated, rig.inferenceStatus(1))
	rig.sealDiff(nonce, 1)
	rig.inferenceMissing(1)
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
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000))
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
