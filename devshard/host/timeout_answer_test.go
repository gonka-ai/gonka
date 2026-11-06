package host

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/headertime"
	"devshard/internal/testutil"
	"devshard/logging"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

// scriptMap is a verifier map whose clock and timer the test drives.
type scriptMap struct {
	mu       sync.Mutex
	now      time.Time
	missing  bool
	stamp    time.Time
	interval time.Duration
	afters   []time.Duration
	timers   []chan time.Time
	watches  []func()
	marks    map[deferralKey]answerMark
	asked    []uint64
}

func (s *scriptMap) CheckEvidence(*types.InferenceRecord, ...*types.DevshardTx) error {
	return nil
}

func (s *scriptMap) TimeoutDue(uint64, types.TimeoutReason) error { return nil }

func (s *scriptMap) VerifierNow() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

func (s *scriptMap) HasHeaderFeed() bool { return true }

func (s *scriptMap) StampHeaderTime(height uint64, _ []byte) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, height)
	if s.missing {
		return 0, false
	}
	return s.stamp.UnixMilli(), true
}

func (s *scriptMap) TimeoutInterval(types.TimeoutReason) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interval
}

func (s *scriptMap) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	s.mu.Lock()
	s.afters = append(s.afters, d)
	s.timers = append(s.timers, ch)
	s.mu.Unlock()
	return ch
}

func (s *scriptMap) WatchHeaders(fn func()) func() {
	s.mu.Lock()
	s.watches = append(s.watches, fn)
	s.mu.Unlock()
	return func() {}
}

func (s *scriptMap) TryHold(id uint64, reason types.TimeoutReason) (func(bool), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.marks == nil {
		s.marks = make(map[deferralKey]answerMark)
	}
	key := deferralKey{inferenceID: id, reason: reason}
	if mark, ok := s.marks[key]; ok {
		warnDuplicateChallenge(id, reason, mark == answerVoted)
		return nil, ErrDuplicateChallenge
	}
	held := 0
	for _, mark := range s.marks {
		if mark == answerHeld {
			held++
		}
	}
	if held >= maxConcurrentAnswers {
		return nil, ErrAnswerBusy
	}
	s.marks[key] = answerHeld
	return func(voted bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if voted {
			s.marks[key] = answerVoted
			return
		}
		if s.marks[key] == answerHeld {
			delete(s.marks, key)
		}
	}, nil
}

func (s *scriptMap) ForgetTimeoutVote(id uint64, reason types.TimeoutReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.marks, deferralKey{inferenceID: id, reason: reason})
}

func (s *scriptMap) voted(id uint64, reason types.TimeoutReason) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marks[deferralKey{inferenceID: id, reason: reason}] == answerVoted
}

func (s *scriptMap) fire(i int) {
	s.mu.Lock()
	ch := s.timers[i]
	s.mu.Unlock()
	ch <- time.Unix(0, 0)
}

func (s *scriptMap) notify() {
	s.mu.Lock()
	fns := append([]func(){}, s.watches...)
	s.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (s *scriptMap) afterCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.afters)
}

type countingExecutor struct {
	calls atomic.Int32
}

func (c *countingExecutor) GetMempool(context.Context) ([]*types.DevshardTx, error) {
	return nil, nil
}

func (c *countingExecutor) ChallengeCatchUp(context.Context, uint64, []types.DiffWire, string) error {
	return nil
}

func (c *countingExecutor) ChallengeReceipt(context.Context, uint64, *InferencePayload, []types.Diff) ([]byte, []*types.DevshardTx, error) {
	c.calls.Add(1)
	return nil, nil, nil
}

func TestVerifierRefusalUsesTheStartStamp(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{
		now:      now,
		stamp:    now.Add(-headertime.AnswerWait - time.Millisecond),
		interval: headertime.AnswerWait,
	}
	st := stateWithPendingFull(1, 1)
	st.Inferences[1].StartedAtHeight = 40
	st.Inferences[1].StartedAtBlockHash = []byte{0x0a}
	exec := &countingExecutor{}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, st.Inferences[1], testPayload(), nil, exec, nil, m)
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, int32(1), exec.calls.Load())
	require.Equal(t, 0, m.afterCount())
	require.Contains(t, m.asked, uint64(40))
}

func TestVerifierExecutionUsesCarriedConfirmTime(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{
		now:      now,
		missing:  true,
		stamp:    now,
		interval: time.Minute,
	}
	st := stateWithStarted(1, 1)
	st.Inferences[1].ConfirmedAtHeight = 70
	st.Inferences[1].ConfirmedAtTimeMs = now.Add(-2 * time.Minute).UnixMilli()
	exec := &countingExecutor{}

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, st.Inferences[1], nil, exec, nil, m)
	require.NoError(t, err)
	require.True(t, accept, "a missing map row still votes from ConfirmedAtTimeMs")
	require.Equal(t, int32(1), exec.calls.Load())
	require.Equal(t, 0, m.afterCount())

	m.ForgetTimeoutVote(1, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	m.missing = false
	m.stamp = now.Add(-time.Hour)
	m.interval = 2 * time.Minute
	st.Inferences[1].ConfirmedAtTimeMs = now.UnixMilli()
	capLog := &capturingLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(loggingDiscard{}) })
	accept, err = VerifyExecutionTimeout(context.Background(), st, 1, st.Inferences[1], nil, exec, nil, m)
	require.NoError(t, err)
	require.False(t, accept, "a map row that is already due does not open before the carried confirm time")
	require.Equal(t, int32(1), exec.calls.Load())
	_, found := capLog.find("claimed confirm time disagrees with the header map; a later dispute rolls back a timeout decided on this time")
	require.True(t, found)
}

func TestVerifierExecutionUsesConfirmedAtHeight(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{
		now:      now,
		stamp:    now.Add(-2 * time.Minute),
		interval: time.Minute,
	}
	st := stateWithStarted(1, 1)
	st.Inferences[1].ConfirmedAtHeight = 70
	exec := &countingExecutor{}

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, st.Inferences[1], nil, exec, nil, m)
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, int32(1), exec.calls.Load())
	require.Equal(t, 0, m.afterCount())
	require.Contains(t, m.asked, uint64(70))
}

func TestVerifierSchedulesOneTimerUnderSixtySeconds(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{
		now:      now,
		stamp:    now.Add(-30 * time.Second),
		interval: headertime.AnswerWait,
	}
	st := stateWithPendingFull(1, 1)
	st.Inferences[1].StartedAtHeight = 40
	st.Inferences[1].StartedAtBlockHash = []byte{0x0a}
	exec := &countingExecutor{}
	capLog := &capturingLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(loggingDiscard{}) })

	first := make(chan error, 1)
	var accept bool
	go func() {
		var err error
		accept, err = VerifyRefusedTimeout(context.Background(), st, 1, st.Inferences[1], testPayload(), nil, exec, nil, m)
		first <- err
	}()
	require.Eventually(t, func() bool { return m.afterCount() == 1 }, time.Second, 5*time.Millisecond)
	require.Equal(t, int32(0), exec.calls.Load())
	m.mu.Lock()
	require.Equal(t, 30*time.Second, m.afters[0])
	m.mu.Unlock()

	accept2, err := VerifyRefusedTimeout(context.Background(), st, 1, st.Inferences[1], testPayload(), nil, exec, nil, m)
	require.ErrorIs(t, err, ErrDuplicateChallenge)
	require.False(t, accept2)
	require.Equal(t, 1, m.afterCount(), "a second evaluation does not arm another timer")
	_, found := capLog.find("timeout challenge already held")
	require.True(t, found)
	require.Equal(t, int32(0), exec.calls.Load())

	m.mu.Lock()
	m.now = now.Add(30*time.Second + time.Millisecond)
	m.mu.Unlock()
	m.fire(0)
	require.NoError(t, <-first)
	require.True(t, accept)
	require.Equal(t, int32(1), exec.calls.Load())
	require.Equal(t, 1, m.afterCount())
}

func TestVerifierIgnoresSixtySecondsRemaining(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{now: now, stamp: now, interval: headertime.AnswerWait}
	st := stateWithPendingFull(1, 1)
	st.Inferences[1].StartedAtHeight = 40
	st.Inferences[1].StartedAtBlockHash = []byte{0x0a}
	exec := &countingExecutor{}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, st.Inferences[1], testPayload(), nil, exec, nil, m)
	require.NoError(t, err)
	require.False(t, accept)
	require.Equal(t, int32(0), exec.calls.Load())
	require.Equal(t, 0, m.afterCount())
}

func TestVerifierFeedArrivesDuringTheWait(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{now: now, missing: true, interval: headertime.AnswerWait}
	st := stateWithStarted(1, 1)
	st.Inferences[1].ConfirmedAtHeight = 70
	exec := &countingExecutor{}

	done := make(chan error, 1)
	var accept bool
	go func() {
		var err error
		accept, err = VerifyExecutionTimeout(context.Background(), st, 1, st.Inferences[1], nil, exec, nil, m)
		done <- err
	}()
	require.Eventually(t, func() bool { return m.afterCount() == 1 }, time.Second, 5*time.Millisecond)
	m.mu.Lock()
	require.Equal(t, headertime.AnswerWait, m.afters[0])
	m.now = now.Add(10 * time.Second)
	m.missing = false
	m.stamp = now.Add(-m.interval - time.Millisecond)
	m.mu.Unlock()
	require.Equal(t, int32(0), exec.calls.Load())

	m.notify()
	require.NoError(t, <-done)
	require.True(t, accept)
	require.Equal(t, int32(1), exec.calls.Load())
	require.Equal(t, 1, m.afterCount(), "the remaining feed wait is not armed again")
}

// Issue 12.5: journal floor is at F; the verifier map still lacks F (one block
// behind). Within AnswerWait there is no vote; after F arrives and is past the
// window, the verifier votes for the refusal.
func TestIssue12_VerifierOneBlockBehindFloor_NoVoteThenAccept(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	const floorHeight uint64 = 100
	m := &scriptMap{now: now, missing: true, interval: headertime.AnswerWait}
	st := stateWithPendingFull(1, 1)
	st.Inferences[1].StartedAtHeight = floorHeight
	st.Inferences[1].StartedAtBlockHash = []byte{0xaa}
	exec := &countingExecutor{}

	done := make(chan error, 1)
	var accept bool
	go func() {
		var err error
		accept, err = VerifyRefusedTimeout(context.Background(), st, 1, st.Inferences[1], testPayload(), nil, exec, nil, m)
		done <- err
	}()
	require.Eventually(t, func() bool { return m.afterCount() == 1 }, time.Second, 5*time.Millisecond)
	require.Contains(t, m.asked, floorHeight)
	require.Equal(t, int32(0), exec.calls.Load(), "behind on F: no challenge / no vote yet")
	require.False(t, m.voted(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED))

	m.mu.Lock()
	require.Equal(t, headertime.AnswerWait, m.afters[0])
	m.now = now.Add(10 * time.Second)
	m.missing = false
	m.stamp = now.Add(-headertime.AnswerWait - time.Millisecond)
	m.mu.Unlock()

	m.notify()
	require.NoError(t, <-done)
	require.True(t, accept, "after F lands past the window, vote yes for refusal")
	require.Equal(t, int32(1), exec.calls.Load())
	require.True(t, m.voted(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED))
}

func TestExecutorUnreachableDoesNotExtendTheVerifierWait(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{now: now, missing: true, interval: headertime.AnswerWait}
	st := stateWithPendingFull(1, 1)
	st.Inferences[1].StartedAtHeight = 40
	st.Inferences[1].StartedAtBlockHash = []byte{0x0a}
	exec := &pendingExecutor{}

	done := make(chan error, 1)
	var accept bool
	go func() {
		var err error
		accept, err = VerifyRefusedTimeout(context.Background(), st, 1, st.Inferences[1], testPayload(), nil, exec, nil, m)
		done <- err
	}()
	require.Eventually(t, func() bool { return m.afterCount() == 1 }, time.Second, 5*time.Millisecond)
	m.mu.Lock()
	require.Equal(t, headertime.AnswerWait, m.afters[0])
	require.Equal(t, 0, exec.calls)
	m.now = now.Add(10 * time.Second)
	m.missing = false
	m.stamp = now.Add(-headertime.AnswerWait - time.Millisecond)
	m.mu.Unlock()

	m.notify()
	require.NoError(t, <-done)
	require.True(t, accept, "once this verifier's map is due, an unreachable executor is a yes vote")
	require.Equal(t, 1, exec.calls)
	require.Equal(t, 1, m.afterCount(), "the executor reply does not arm another wait")
}

func TestVerifierFeedStillMissingAtSixtySeconds(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{now: now, missing: true, interval: headertime.AnswerWait}
	st := stateWithPendingFull(1, 1)
	st.Inferences[1].StartedAtHeight = 40
	st.Inferences[1].StartedAtBlockHash = []byte{0x0a}
	exec := &countingExecutor{}
	capLog := &capturingLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(loggingDiscard{}) })

	done := make(chan error, 1)
	var accept bool
	go func() {
		var err error
		accept, err = VerifyRefusedTimeout(context.Background(), st, 1, st.Inferences[1], testPayload(), nil, exec, nil, m)
		done <- err
	}()
	require.Eventually(t, func() bool { return m.afterCount() == 1 }, time.Second, 5*time.Millisecond)
	m.fire(0)
	require.NoError(t, <-done)
	require.False(t, accept)
	require.Equal(t, int32(0), exec.calls.Load())
	_, found := capLog.find("verifier block feed is behind the chain")
	require.True(t, found)
}

type recordingSink struct {
	txs []*types.DevshardTx
}

func (r *recordingSink) AddTx(tx *types.DevshardTx) {
	r.txs = append(r.txs, tx)
}

func TestDuplicateReasonsStayIndependent(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{
		now:      now,
		stamp:    now.Add(-30 * time.Second),
		interval: headertime.AnswerWait,
	}
	pending := stateWithPendingFull(1, 1)
	pending.Inferences[1].StartedAtHeight = 40
	pending.Inferences[1].StartedAtBlockHash = []byte{0x0a}
	started := stateWithStarted(1, 1)
	started.Inferences[1].ConfirmedAtHeight = 70
	refusedExec := &countingExecutor{}
	executionExec := &countingExecutor{}

	refusedDone := make(chan error, 1)
	var refusedAccept bool
	go func() {
		var err error
		refusedAccept, err = VerifyRefusedTimeout(context.Background(), pending, 1, pending.Inferences[1], testPayload(), nil, refusedExec, nil, m)
		refusedDone <- err
	}()
	executionDone := make(chan error, 1)
	var executionAccept bool
	go func() {
		var err error
		executionAccept, err = VerifyExecutionTimeout(context.Background(), started, 1, started.Inferences[1], nil, executionExec, nil, m)
		executionDone <- err
	}()
	require.Eventually(t, func() bool { return m.afterCount() == 2 }, time.Second, 5*time.Millisecond)

	accept, err := VerifyRefusedTimeout(context.Background(), pending, 1, pending.Inferences[1], testPayload(), nil, refusedExec, nil, m)
	require.ErrorIs(t, err, ErrDuplicateChallenge)
	require.False(t, accept)
	accept, err = VerifyExecutionTimeout(context.Background(), started, 1, started.Inferences[1], nil, executionExec, nil, m)
	require.ErrorIs(t, err, ErrDuplicateChallenge)
	require.False(t, accept)
	require.Equal(t, 2, m.afterCount())
	require.Equal(t, int32(0), refusedExec.calls.Load())
	require.Equal(t, int32(0), executionExec.calls.Load())

	m.mu.Lock()
	m.now = now.Add(30*time.Second + time.Millisecond)
	m.mu.Unlock()
	m.fire(0)
	m.fire(1)
	require.NoError(t, <-refusedDone)
	require.NoError(t, <-executionDone)
	require.True(t, refusedAccept)
	require.True(t, executionAccept)
	require.Equal(t, int32(1), refusedExec.calls.Load())
	require.Equal(t, int32(1), executionExec.calls.Load())
}

func TestRememberedTimeoutVoteReplaysSignature(t *testing.T) {
	h := &Host{}
	sig := []byte{0xab, 0xcd}
	h.RememberTimeoutVote(7, types.TimeoutReason_TIMEOUT_REASON_REFUSED, sig, 3)
	got, slot, ok := h.LookupTimeoutVote(7, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.True(t, ok)
	require.Equal(t, sig, got)
	require.Equal(t, uint32(3), slot)

	_, err := h.TryHold(7, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.ErrorIs(t, err, ErrDuplicateChallenge, "a remembered vote still occupies the hold")

	h.ForgetTimeoutVote(7, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	_, _, ok = h.LookupTimeoutVote(7, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.False(t, ok)
}

func TestVotedChallengeIsRememberedUntilTheRecordMoves(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{
		now:      now,
		stamp:    now.Add(-headertime.AnswerWait - time.Millisecond),
		interval: headertime.AnswerWait,
	}
	st := stateWithPendingFull(1, 1)
	rec := st.Inferences[1]
	rec.StartedAtHeight = 40
	rec.StartedAtBlockHash = []byte{0x0a}
	exec := &countingExecutor{}
	sink := &recordingSink{}
	capLog := &capturingLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(loggingDiscard{}) })

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, rec, testPayload(), nil, exec, sink, m)
	require.NoError(t, err)
	require.True(t, accept)
	require.True(t, m.voted(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED))
	require.Equal(t, int32(1), exec.calls.Load())

	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte{1}}}}
	accept, err = VerifyRefusedTimeout(context.Background(), st, 1, rec, testPayload(), []*types.DevshardTx{confirm}, exec, sink, m)
	require.ErrorIs(t, err, ErrDuplicateChallenge)
	require.False(t, accept)
	require.Empty(t, sink.txs)
	require.Equal(t, int32(1), exec.calls.Load())
	require.Equal(t, types.StatusPending, rec.Status)
	_, found := capLog.find("timeout challenge already answered")
	require.True(t, found)

	rec.Status = types.StatusTimedOut
	_, err = VerifyRefusedTimeout(context.Background(), st, 1, rec, testPayload(), nil, exec, sink, m)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected pending")
	require.False(t, m.voted(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED))
	require.Equal(t, int32(1), exec.calls.Load())

	started := stateWithStarted(2, 1)
	started.Inferences[2].ConfirmedAtHeight = 70
	accept, err = VerifyExecutionTimeout(context.Background(), started, 2, started.Inferences[2], nil, exec, sink, m)
	require.NoError(t, err)
	require.True(t, accept)
	require.True(t, m.voted(2, types.TimeoutReason_TIMEOUT_REASON_EXECUTION))
	started.Inferences[2].Status = types.StatusFinished
	_, err = VerifyExecutionTimeout(context.Background(), started, 2, started.Inferences[2], nil, exec, sink, m)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected started")
	require.False(t, m.voted(2, types.TimeoutReason_TIMEOUT_REASON_EXECUTION))
}

func TestIgnoreIsNotRemembered(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	m := &scriptMap{now: now, stamp: now, interval: headertime.AnswerWait}
	st := stateWithPendingFull(1, 1)
	st.Inferences[1].StartedAtHeight = 40
	st.Inferences[1].StartedAtBlockHash = []byte{0x0a}
	exec := &countingExecutor{}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, st.Inferences[1], testPayload(), nil, exec, nil, m)
	require.NoError(t, err)
	require.False(t, accept)
	require.False(t, m.voted(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED))
	require.Equal(t, int32(0), exec.calls.Load())

	m.mu.Lock()
	m.stamp = now.Add(-headertime.AnswerWait - time.Millisecond)
	m.mu.Unlock()
	accept, err = VerifyRefusedTimeout(context.Background(), st, 1, st.Inferences[1], testPayload(), nil, exec, nil, m)
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, int32(1), exec.calls.Load())
}

type fillCounter struct{ n *atomic.Int32 }

func (c fillCounter) Fill(context.Context, int64) (headertime.Fill, error) {
	c.n.Add(1)
	return headertime.Fill{}, blocks.ErrHeaderNotFound
}

func TestVerifierMissingFeedLeavesAnotherConfirmRunning(t *testing.T) {
	var fills atomic.Int32
	src := headertime.New(headertime.WithFiller(fillCounter{n: &fills}))
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(group))
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000),
		state.WithHeaderTimes(src))
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil)
	require.NoError(t, err)

	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{
		testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)}),
	}})
	require.NoError(t, err)

	entered := make(chan struct{})
	var once sync.Once
	h.answerAfter = func(time.Duration) <-chan time.Time {
		once.Do(func() { close(entered) })
		return make(chan time.Time)
	}
	st, rec, ok := sm.SnapshotWithInference(1)
	require.True(t, ok)
	rec.StartedAtHeight = 999
	rec.StartedAtBlockHash = []byte{0xab}
	exec := &countingExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = VerifyRefusedTimeout(ctx, st, 1, rec, testPayload(), nil, exec, nil, h)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("verifier did not wait for the missing header")
	}
	require.Equal(t, int32(0), exec.calls.Load())
	require.Zero(t, fills.Load())

	capLog := &capturingLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(loggingDiscard{}) })
	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, rec, testPayload(), nil, exec, nil, h)
	require.ErrorIs(t, err, ErrDuplicateChallenge)
	require.False(t, accept)
	_, found := capLog.find("timeout challenge already held")
	require.True(t, found)
	require.Equal(t, int32(0), exec.calls.Load())

	sig := testutil.SignExecutorReceipt(t, hosts[2], "escrow-1", 2, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 2, ExecutorSig: sig,
	}}}
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{
		testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{testutil.StartTx(2)}),
		testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{confirm}),
	}})
	require.NoError(t, err)
	started, ok := sm.GetInference(2)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, started.Status)
	require.Equal(t, int32(0), exec.calls.Load())
	require.Zero(t, fills.Load(), "the feed wait does not call GetBlockHeader")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the wait did not return")
	}
}

func TestChallengeStampExecutionCarriesConfirmHash(t *testing.T) {
	rec := &types.InferenceRecord{
		Status:               types.StatusStarted,
		ConfirmedAtHeight:    70,
		ConfirmedAtBlockHash: []byte{0xb1},
		StartedAtHeight:      40,
		StartedAtBlockHash:   []byte{0xa1},
	}
	h, hash := challengeStamp(rec, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.Equal(t, uint64(70), h)
	require.Equal(t, []byte{0xb1}, hash)

	h, hash = challengeStamp(rec, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.Equal(t, uint64(40), h)
	require.Equal(t, []byte{0xa1}, hash)
}

func TestStoredStampTimeRejectsConfirmHashMismatch(t *testing.T) {
	src := headertime.New()
	src.Observe(&blocks.Header{Height: 70, BlockHash: []byte{0xb1}, Time: time.UnixMilli(1_000)}, headertime.OriginDAPI, true)

	ms, ok := storedStampTime(src, 70, []byte{0xb1})
	require.True(t, ok)
	require.Equal(t, int64(1_000), ms)

	_, ok = storedStampTime(src, 70, []byte{0xff})
	require.False(t, ok, "a fork hash at the same height is not the confirm stamp")

	ms, ok = storedStampTime(src, 70, nil)
	require.True(t, ok, "empty hash keeps the legacy height-only read")
	require.Equal(t, int64(1_000), ms)
}

func TestVerifierExecutionRejectsForkHashAtConfirmHeight(t *testing.T) {
	now := time.UnixMilli(time.Now().UnixMilli())
	wantHash := []byte{0xb1}
	forkHash := []byte{0xff}
	src := headertime.New()
	src.Observe(&blocks.Header{Height: 70, BlockHash: forkHash, Time: now.Add(-2 * time.Minute)}, headertime.OriginDAPI, true)

	user := testutil.MustGenerateKey(t)
	hostSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	cfg := testutil.DefaultConfig(1)
	sm, err := state.NewStateMachine("escrow-1", cfg, group, 100000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 100000),
		state.WithHeaderTimes(src))
	require.NoError(t, err)
	host, err := NewHost(sm, hostSigner, stub.NewInferenceEngine(), "escrow-1", group, nil)
	require.NoError(t, err)

	rec := &types.InferenceRecord{
		Status:               types.StatusStarted,
		ConfirmedAtHeight:    70,
		ConfirmedAtBlockHash: wantHash,
	}
	ms, ok := host.StampHeaderTime(challengeStamp(rec, types.TimeoutReason_TIMEOUT_REASON_EXECUTION))
	require.False(t, ok, "map row for a fork hash must not satisfy the confirm stamp")
	require.Zero(t, ms)
}

// Issue 11: early cancel of waitAnswer must return promptly (NewTimer is Stop'd).
func TestIssue11_WaitAnswerCancelReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- waitAnswer(ctx, nil, time.Hour, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waitAnswer did not return after cancel; timer likely not Stop'd")
	}
}

// Issue 11: answer memory is dropped when the inference leaves the live map.
func TestIssue11_ForgetAnswersWhenInferenceLeavesLive(t *testing.T) {
	h := &Host{}
	sig := []byte{0x11}
	h.RememberTimeoutVote(9, types.TimeoutReason_TIMEOUT_REASON_REFUSED, sig, 1)
	h.RememberTimeoutVote(9, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, sig, 1)
	_, _, ok := h.LookupTimeoutVote(9, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.True(t, ok)

	h.answerHolds.forgetInference(9)
	_, _, ok = h.LookupTimeoutVote(9, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.False(t, ok)
	_, _, ok = h.LookupTimeoutVote(9, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.False(t, ok)
}

// Issue 11: past maxConcurrentAnswers, beginAnswer returns ErrAnswerBusy
// (accept=false, no vote) rather than starting another wait.
func TestIssue11_AnswerCapacityRejectsExtraCheck(t *testing.T) {
	h := &Host{}
	releases := make([]func(bool), 0, maxConcurrentAnswers)
	for i := 0; i < maxConcurrentAnswers; i++ {
		release, err := h.TryHold(uint64(i+1), types.TimeoutReason_TIMEOUT_REASON_REFUSED)
		require.NoError(t, err)
		releases = append(releases, release)
	}
	_, err := h.TryHold(uint64(maxConcurrentAnswers+1), types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.ErrorIs(t, err, ErrAnswerBusy)

	// Voted entries do not count toward the concurrent cap.
	releases[0](true)
	_, err = h.TryHold(uint64(maxConcurrentAnswers+1), types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NoError(t, err)

	for i := 1; i < len(releases); i++ {
		releases[i](false)
	}
}

func TestIssue11_ForgetAnswersLeftLiveUsesBeforeSet(t *testing.T) {
	user := testutil.MustGenerateKey(t)
	hostSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	cfg := testutil.DefaultConfig(1)
	sm, err := state.NewStateMachine("escrow-1", cfg, group, 100000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 100000))
	require.NoError(t, err)
	h, err := NewHost(sm, hostSigner, stub.NewInferenceEngine(), "escrow-1", group, nil)
	require.NoError(t, err)

	h.RememberTimeoutVote(42, types.TimeoutReason_TIMEOUT_REASON_REFUSED, []byte{0x42}, 0)
	before := map[uint64]struct{}{42: {}}
	h.forgetAnswersLeftLive(before)
	_, _, ok := h.LookupTimeoutVote(42, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.False(t, ok, "id absent from live map must drop answer memory")
}
