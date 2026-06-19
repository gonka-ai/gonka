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

const (
	// pruneTestInferenceSealGraceNonces is the nonce gate used by prune tests: an
	// inference id may be sealed only once nonce >= id + this.
	pruneTestInferenceSealGraceNonces = 2
	// pruneTestInferenceSealGraceSeconds is the clock gate: an inference may be sealed
	// only once stateClock - ConfirmedAt >= this many "seconds".
	pruneTestInferenceSealGraceSeconds = 5
	// pruneTestBaseConfirmedAt anchors the ConfirmedAt values the helpers use.
	pruneTestBaseConfirmedAt = 2000
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
	return newPruneRigGrace(t, observerIdx, numHosts, pruneTestInferenceSealGraceNonces, pruneTestInferenceSealGraceSeconds, opts...)
}

// newPruneRigGrace is newPruneRig with explicit seal-grace gates, replacing the
// old host-local WithPruneTuning: the gates now live in SessionConfig and drive
// the deterministic state-machine seal.
func newPruneRigGrace(t *testing.T, observerIdx, numHosts int, sealGraceNonces, clearGraceSeconds uint32, opts ...HostOption) *pruneTestRig {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	// Small, explicit seal gates so tests can deterministically drive the
	// state-clock seal: nonce gate = id+2, clock gate = +5 "seconds" of
	// ConfirmedAt progress. ConfirmedAt values in the helpers below advance in
	// steps larger than 5 so a single newer confirmed inference clears the gate.
	config := types.SessionConfig{
		RefusalTimeout:             60,
		ExecutionTimeout:           1200,
		TokenPrice:                 1,
		VoteThreshold:              uint32(numHosts) / 2,
		ValidationRate:             0,
		InferenceSealGraceNonces:            sealGraceNonces,
		InferenceSealGraceSeconds: clearGraceSeconds,
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

func (r *pruneTestRig) advanceToNextAutoSealNonce(after uint64) uint64 {
	r.t.Helper()
	target := state.NextAutoSealNonce(after)
	for n := after + 1; n <= target; n++ {
		r.applyDiff(n, nil)
	}
	return target + 1
}

// driveStartConfirmFinish brings inferenceID through Pending -> Started ->
// Finished using a default ConfirmedAt of base+inferenceID. startNonce is
// consumed for MsgStartInference; the next two diffs use startNonce+1/+2.
func (r *pruneTestRig) driveStartConfirmFinish(inferenceID, startNonce uint64) uint64 {
	return r.driveStartConfirmFinishAt(inferenceID, startNonce, pruneTestBaseConfirmedAt+int64(inferenceID))
}

// driveStartConfirmFinishAt is driveStartConfirmFinish with an explicit
// executor-signed ConfirmedAt, which is what advances the deterministic state
// clock for seal gating.
func (r *pruneTestRig) driveStartConfirmFinishAt(inferenceID, startNonce uint64, confirmedAt int64) uint64 {
	r.t.Helper()
	r.startConfirm(inferenceID, startNonce, confirmedAt)

	executorSlot := uint32(inferenceID % uint64(len(r.group)))
	finishMsg := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		ResponseHash: r.stub.ResponseHash,
		InputTokens:  r.stub.InputTokens,
		OutputTokens: r.stub.OutputTokens,
		ExecutorSlot: executorSlot,
		EscrowId:     r.escrowID,
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(r.t, r.hosts[executorSlot], finishMsg)
	finishTx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}
	r.applyDiff(startNonce+2, []*types.DevshardTx{finishTx})

	return startNonce + 3
}

// startConfirm brings inferenceID through Pending -> Started with the given
// executor-signed ConfirmedAt, leaving it live (Started). Consumes startNonce
// (start) and startNonce+1 (confirm).
func (r *pruneTestRig) startConfirm(inferenceID, startNonce uint64, confirmedAt int64) {
	r.t.Helper()
	executorSlot := uint32(inferenceID % uint64(len(r.group)))
	r.applyDiff(startNonce, []*types.DevshardTx{testutil.StartTx(inferenceID)})
	execSig := testutil.SignExecutorReceipt(r.t, r.hosts[executorSlot], r.escrowID, inferenceID,
		testutil.TestPromptHash[:], "llama", 100, 50, 1000, confirmedAt)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: confirmedAt,
	}}}
	r.applyDiff(startNonce+1, []*types.DevshardTx{confirmTx})
}

// bumpClock starts and confirms a fresh inference (id == startNonce) with the
// given ConfirmedAt. The inference is left in StatusStarted (not seal-eligible).
// Returns the next nonce.
//
// Under the min-ConfirmedAt state clock, one high-ConfirmedAt bump does not
// advance the clock while older live inferences remain in the tail window.
// Use advanceClockPastGrace to drive Finished sealing in tests.
func (r *pruneTestRig) bumpClock(startNonce uint64, confirmedAt int64) uint64 {
	r.t.Helper()
	r.startConfirm(startNonce, startNonce, confirmedAt)
	return startNonce + 2
}

// advanceClockPastGrace bumps the state clock until inferenceID seals. The
// min-ConfirmedAt clock is taken over the latest N*stateClockWindowFactor live
// ids; inferenceID must fall out of that tail (or the tail's minimum
// ConfirmedAt must clear inferenceID's grace) before a stale Finished record
// can seal.
func (r *pruneTestRig) advanceClockPastGrace(startNonce uint64, inferenceID uint64) uint64 {
	r.t.Helper()
	window := len(r.group) * 3 // state.stateClockWindowFactor
	targetConfirmedAt := pruneTestBaseConfirmedAt + int64(inferenceID) + int64(pruneTestInferenceSealGraceSeconds) + 1
	for bump := 0; bump < window+5; bump++ {
		startNonce = r.bumpClock(startNonce, targetConfirmedAt+int64(bump))
		st := r.host.SnapshotState()
		if _, live := st.Inferences[inferenceID]; !live {
			return startNonce
		}
		startNonce = r.advanceToNextAutoSealNonce(startNonce - 1)
		st = r.host.SnapshotState()
		if _, live := st.Inferences[inferenceID]; !live {
			return startNonce
		}
	}
	r.t.Fatalf("inference %d did not seal after %d clock bumps", inferenceID, window+5)
	return startNonce
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

// driveToValidated brings inference 1 (executor slot 1) to StatusValidated with
// 3 valid votes (threshold=2). Terminal statuses seal on the validating diff
// once the nonce gate clears (no state-clock grace). Returns next nonce.
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
	return nonce + 1
}

// The seal is a deterministic function of state. Terminal statuses
// (Validated/Invalidated/TimedOut) seal once nonce >= id+InferenceSealGraceNonces on
// an auto-seal nonce (every AutoSealEveryNNonces). Finished (stale-finished) also
// requires the state clock to advance >= InferenceSealGraceSeconds past ConfirmedAt;
// those tests use advanceClockPastGrace to advance the clock without sleeps.

func TestHost_PruneSink_SealsTerminal_Validated(t *testing.T) {
	rig := newPruneRig(t, 0, 5)
	next := rig.driveToValidated(1) // inference 1 Validated; seals on next auto-seal nonce.
	rig.advanceToNextAutoSealNonce(next - 1)

	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonTerminal, events[0].Reason)
	require.Equal(t, rig.escrowID, events[0].EscrowID)
	require.Equal(t, rig.epochID, events[0].PayloadEpoch)
	require.True(t, events[0].PayloadEpochKnown)
	rig.inferenceMissing(1)
	require.NotZero(t, rig.sealedRow(1).SealedNonce)
}

func TestHost_PruneSink_SealsTerminal_Invalidated(t *testing.T) {
	rig := newPruneRig(t, 0, 5)
	nonce := rig.driveStartConfirmFinish(1, 1)
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidation(1, 2, false)})
	nonce++
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidationVote(1, 0, false)})
	nonce++
	rig.applyDiff(nonce, []*types.DevshardTx{rig.signValidationVote(1, 3, false)})
	rig.advanceToNextAutoSealNonce(nonce)

	events := rig.sink.findFor(1)
	require.Equal(t, PruneReasonTerminal, events[0].Reason)
	rig.inferenceMissing(1)
	require.NotZero(t, rig.sealedRow(1).SealedNonce)
}

func TestHost_PruneSink_SealsTerminal_TimedOut(t *testing.T) {
	rig := newPruneRig(t, 0, 5)

	// Start inference 1, then timeout from Pending (no ConfirmStart -> ConfirmedAt=0).
	rig.applyDiff(1, []*types.DevshardTx{testutil.StartTx(1)})
	require.Equal(t, types.StatusPending, rig.inferenceStatus(1))
	timeoutTx := rig.signTimeoutInference(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, []uint32{0, 2, 3})
	rig.applyDiff(2, []*types.DevshardTx{timeoutTx})
	require.Equal(t, types.StatusTimedOut, rig.inferenceStatus(1))
	require.Empty(t, rig.sink.findFor(1), "nonce gate not yet cleared at timeout diff")

	// Terminal short path skips the clock gate, but auto-seal still requires a
	// non-zero state clock (confirmed record in the tail). Establish clock, then
	// advance nonce to clear id+InferenceSealGraceNonces.
	nonce := rig.bumpClock(3, pruneTestBaseConfirmedAt+100)
	rig.advanceToNextAutoSealNonce(nonce - 1)

	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonTerminal, events[0].Reason)
	rig.inferenceMissing(1)
	require.NotZero(t, rig.sealedRow(1).SealedNonce)
}

func TestHost_PruneSink_SealsStaleFinished(t *testing.T) {
	rig := newPruneRig(t, 0, 5)

	// Finish inference 1 (ConfirmedAt=2001). Finishing alone never prunes.
	nonce := rig.driveStartConfirmFinish(1, 1)
	require.Equal(t, types.StatusFinished, rig.inferenceStatus(1))
	require.Empty(t, rig.sink.findFor(1), "Finish itself does not prune")

	// Advance the min-ConfirmedAt clock past inference 1's grace. One bump is not
	// enough while id 1 stays in the tail window; fill the window first.
	nonce = rig.advanceClockPastGrace(nonce, 1)

	events := rig.sink.findFor(1)
	require.Len(t, events, 1)
	require.Equal(t, PruneReasonStaleFinished, events[0].Reason)
	require.Equal(t, rig.epochID, events[0].PayloadEpoch)
	rig.inferenceMissing(1)
	require.NotZero(t, rig.sealedRow(1).SealedNonce)

	// Later diffs must not re-emit for the same inference.
	rig.applyDiff(nonce, nil)
	require.Len(t, rig.sink.findFor(1), 1, "prune must dedupe across later diffs")
}

func TestHost_PruneSink_DoesNotSealWhenNonceGateUnmet(t *testing.T) {
	// Large nonce gate, tiny clock gate: the clock advances freely but the nonce
	// floor (id + 50) is never reached, so the inference must not seal.
	rig := newPruneRigGrace(t, 0, 5, 50, 1)
	nonce := rig.driveStartConfirmFinish(1, 1)
	// Advance the clock well past the grace several times.
	for i := 0; i < 5; i++ {
		nonce = rig.bumpClock(nonce, pruneTestBaseConfirmedAt+100+int64(i)*10)
	}
	require.Empty(t, rig.sink.findFor(1), "nonce gate not yet crossed")
	require.Equal(t, types.StatusFinished, rig.inferenceStatus(1))
}

func TestHost_PruneSink_DoesNotSealWhenClockGateUnmet(t *testing.T) {
	// Tiny nonce gate, large clock gate: nonces advance but ConfirmedAt never
	// moves far enough ahead, so the clock gate keeps the inference live.
	rig := newPruneRigGrace(t, 0, 5, 2, 100_000)
	nonce := rig.driveStartConfirmFinish(1, 1)
	for i := 0; i < 4; i++ {
		// Bump the clock by only +1 each time: well under the 100000 grace.
		nonce = rig.bumpClock(nonce, pruneTestBaseConfirmedAt+1+int64(i)+1)
	}
	require.Empty(t, rig.sink.findFor(1), "clock gate prevents seal")
	require.Equal(t, types.StatusFinished, rig.inferenceStatus(1))
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
		InferenceSealGraceNonces:            pruneTestInferenceSealGraceNonces,
		InferenceSealGraceSeconds: pruneTestInferenceSealGraceSeconds,
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

	// Drive a full Finish, then seal via the deterministic state clock. The sink
	// is nil, so this exercises the emitSealPrunes early-return path while the
	// state machine still folds the inference into SealedAcc.
	rig := &pruneTestRig{
		t: t, hosts: hosts, user: user, group: group, config: config,
		host: h, stub: stub.NewInferenceEngine(), store: store, escrowID: "escrow-1", epochID: 7,
	}
	nonce := rig.driveStartConfirmFinish(1, 1)
	require.Equal(t, types.StatusFinished, rig.inferenceStatus(1))
	rig.advanceClockPastGrace(nonce, 1)
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
