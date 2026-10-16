package host

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/state"
	"devshard/types"
)

type coldPeer struct {
	mu       sync.Mutex
	head     func() (uint64, []byte, error)
	sent     []uint64
	payloads int
}

func (p *coldPeer) SessionHead(context.Context) (uint64, []byte, error) {
	return p.head()
}

func (p *coldPeer) GetMempool(context.Context) ([]*types.DevshardTx, error) {
	return nil, nil
}

func (p *coldPeer) sentNonces() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uint64(nil), p.sent...)
}

func (p *coldPeer) payloadCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.payloads
}

func (p *coldPeer) ChallengeCatchUp(_ context.Context, _ uint64, diffs []types.DiffWire, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range diffs {
		p.sent = append(p.sent, d.Nonce)
	}
	return nil
}

func (p *coldPeer) ChallengeReceipt(_ context.Context, _ uint64, _ *InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.payloads++
	for _, d := range diffs {
		p.sent = append(p.sent, d.Nonce)
	}
	return nil, nil, nil
}

// nonceJournal serves one nonce per page and records every nonce it loads.
// arm makes the next load of one nonce wait for release or the walk deadline.
type nonceJournal struct {
	mu      sync.Mutex
	diffs   []types.Diff
	loaded  []uint64
	blockAt uint64
	blocked chan struct{}
	release chan struct{}
}

func (j *nonceJournal) arm(nonce uint64) (blocked, release chan struct{}) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.blockAt = nonce
	j.blocked = make(chan struct{})
	j.release = make(chan struct{})
	return j.blocked, j.release
}

func (j *nonceJournal) disarm() {
	j.mu.Lock()
	j.blockAt = 0
	j.mu.Unlock()
}

func (j *nonceJournal) Anchor(nonce uint64, root []byte) (bool, error) {
	for _, d := range j.diffs {
		if d.Nonce == nonce {
			return bytes.Equal(d.PostStateRoot, root), nil
		}
	}
	return false, nil
}

func (j *nonceJournal) loadedNonces() []uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]uint64(nil), j.loaded...)
}

func (j *nonceJournal) clearLoaded() {
	j.mu.Lock()
	j.loaded = nil
	j.mu.Unlock()
}

func (j *nonceJournal) Pages(ctx context.Context, from, to uint64, next func() uint64, fn func([]types.DiffWire, bool) (func(context.Context) error, error)) error {
	if from > to {
		return nil
	}
	for n := from; n <= to; {
		j.mu.Lock()
		var blocked, release chan struct{}
		if j.blockAt == n {
			j.blockAt = 0
			blocked, release = j.blocked, j.release
		}
		j.mu.Unlock()
		if blocked != nil {
			close(blocked)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		j.mu.Lock()
		j.loaded = append(j.loaded, n)
		j.mu.Unlock()
		wires, err := journalWires([]types.Diff{j.diffs[n-1]})
		if err != nil {
			return err
		}
		more := n < to
		send, err := fn(wires, more)
		if err != nil {
			return err
		}
		if send != nil {
			if err := send(ctx); err != nil {
				return err
			}
		}
		if !more {
			return nil
		}
		n++
		if next != nil {
			if cur := next(); cur > n {
				n = cur
			}
		}
	}
	return nil
}

func coldNotFound() (uint64, []byte, error) {
	return 0, nil, errors.New("session not found")
}

func runColdCheck(t *testing.T, ctx context.Context, st types.EscrowState, peer *coldPeer, journal RefusalJournal, ev *state.StateMachine, budget time.Duration) (bool, error) {
	t.Helper()
	return VerifyRefusedProgress(
		ctx, st, 1, st.Inferences[1], testPayload(), nil,
		peer, nil, ev, peer, journal,
		50*time.Millisecond, budget, deadlinePassedRefused(st, 1), progressUnreachable,
	)
}

// coldChecker runs checks against one gate and one executor key. Each check
// gets its own context and evidence group.
type coldChecker struct {
	t       *testing.T
	gate    *ColdWalks
	key     string
	st      types.EscrowState
	peer    *coldPeer
	journal RefusalJournal
}

func (c coldChecker) run(ctx context.Context, budget time.Duration) (bool, error) {
	return runColdCheck(c.t, WithColdWalk(ctx, c.gate, c.key), c.st, c.peer, c.journal, newEvidenceGroup(c.t).sm, budget)
}

func (c coldChecker) check() (bool, error) {
	return c.run(context.Background(), 2*time.Second)
}

func (c coldChecker) goCheck(ev *state.StateMachine) chan error {
	out := make(chan error, 1)
	ctx := WithColdWalk(context.Background(), c.gate, c.key)
	go func() {
		_, err := runColdCheck(c.t, ctx, c.st, c.peer, c.journal, ev, 2*time.Second)
		out <- err
	}()
	return out
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(what)
	}
}

func signalColdWait(t *testing.T) chan struct{} {
	arrived := make(chan struct{}, 1)
	testBeforeColdWait = func() {
		select {
		case arrived <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { testBeforeColdWait = nil })
	return arrived
}

func nonces(from, to uint64) []uint64 {
	var out []uint64
	for n := from; n <= to; n++ {
		out = append(out, n)
	}
	return out
}

func TestColdWalkOneCheckSendsTheJournal(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	blocked, release := journal.arm(1)
	peer := &coldPeer{head: coldNotFound}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}
	arrived := signalColdWait(t)

	leader := c.goCheck(newEvidenceGroup(t).sm)
	waitFor(t, blocked, "cold walk did not start")
	waiter := c.goCheck(newEvidenceGroup(t).sm)
	waitFor(t, arrived, "second check did not wait")
	require.Empty(t, journal.loadedNonces())

	close(release)
	require.NoError(t, <-leader)
	require.NoError(t, <-waiter)
	require.Equal(t, nonces(1, 4), journal.loadedNonces())
	require.Equal(t, nonces(1, 4), peer.sentNonces())
	require.Equal(t, 2, peer.payloadCalls())
}

func TestColdWalkResumesAfterTheBudget(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	journal.arm(2)
	peer := &coldPeer{head: coldNotFound}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}

	_, err := c.run(context.Background(), 500*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []uint64{1}, journal.loadedNonces())

	accept, err := c.check()
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, nonces(1, 4), journal.loadedNonces())
	require.Equal(t, nonces(1, 4), peer.sentNonces())
}

func TestColdWalkDeliveredJournalSendsPayloadOnly(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	peer := &coldPeer{head: func() (uint64, []byte, error) { return 0, nil, nil }}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}

	for range 4 {
		accept, err := c.check()
		require.NoError(t, err)
		require.True(t, accept)
	}
	require.Equal(t, nonces(1, 4), journal.loadedNonces())
	require.Equal(t, nonces(1, 4), peer.sentNonces())
	require.Equal(t, 4, peer.payloadCalls())
}

func TestColdWalkAttemptLimitSendsPayloadOnly(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	peer := &coldPeer{head: coldNotFound}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}

	for _, at := range []uint64{2, 3} {
		journal.arm(at)
		_, err := c.run(context.Background(), 500*time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	require.Equal(t, []uint64{1, 2}, journal.loadedNonces())

	accept, err := c.check()
	require.NoError(t, err)
	require.True(t, accept, "a cold executor past the limit is judged on the payload alone")
	require.Equal(t, []uint64{1, 2}, journal.loadedNonces())
	require.Equal(t, 1, peer.payloadCalls())
}

func TestColdWalkRealTipClearsTheMark(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	var mu sync.Mutex
	phase := 0
	peer := &coldPeer{head: func() (uint64, []byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if phase == 1 {
			return 2, root, nil
		}
		return coldNotFound()
	}}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}
	setPhase := func(p int) {
		mu.Lock()
		phase = p
		mu.Unlock()
	}

	accept, err := c.check()
	require.NoError(t, err)
	require.True(t, accept)

	setPhase(1)
	accept, err = c.check()
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, []uint64{1, 2, 3, 4, 3, 4}, journal.loadedNonces())

	setPhase(2)
	journal.clearLoaded()
	accept, err = c.check()
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, nonces(1, 4), journal.loadedNonces())
}

func TestColdWalkWrongRootDoesNotClearTheMark(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	wrong := bytes.Repeat([]byte{2}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	var mu sync.Mutex
	phase := 0
	peer := &coldPeer{head: func() (uint64, []byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch phase {
		case 1:
			return 2, wrong, nil
		case 2:
			return st.LatestNonce + 5, root, nil
		default:
			return coldNotFound()
		}
	}}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}
	for _, p := range []int{0, 1, 2} {
		mu.Lock()
		phase = p
		mu.Unlock()
		accept, err := c.check()
		require.NoError(t, err)
		require.True(t, accept)
	}

	mu.Lock()
	phase = 0
	mu.Unlock()
	journal.clearLoaded()
	accept, err := c.check()
	require.NoError(t, err)
	require.True(t, accept)
	require.Empty(t, journal.loadedNonces(), "the journal was already sent and no tip matched since")
}

func TestColdWalkRealTipDoesNotWait(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	blocked, release := journal.arm(1)
	var mu sync.Mutex
	phase := 0
	peer := &coldPeer{head: func() (uint64, []byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if phase == 1 {
			return 2, root, nil
		}
		return coldNotFound()
	}}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}

	leader := c.goCheck(newEvidenceGroup(t).sm)
	waitFor(t, blocked, "cold walk did not start")

	mu.Lock()
	phase = 1
	mu.Unlock()
	accept, err := c.check()
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, []uint64{3, 4}, journal.loadedNonces())
	select {
	case err := <-leader:
		t.Fatalf("cold walk finished while the real tip was checked: %v", err)
	default:
	}

	close(release)
	require.NoError(t, <-leader)

	mu.Lock()
	phase = 0
	mu.Unlock()
	journal.clearLoaded()
	accept, err = c.check()
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, nonces(1, 4), journal.loadedNonces())
}

func TestColdWalkLongWrongRootTakesTheGate(t *testing.T) {
	const latest = coldWalkLongNonces + 8
	st := progressState(latest)
	root := bytes.Repeat([]byte{1}, 32)
	wrong := bytes.Repeat([]byte{2}, 32)
	journal := &nonceJournal{diffs: progressDiffs(latest, root)}
	blocked, release := journal.arm(2)
	peer := &coldPeer{head: func() (uint64, []byte, error) { return 1, wrong, nil }}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}
	arrived := signalColdWait(t)

	leader := c.goCheck(newEvidenceGroup(t).sm)
	waitFor(t, blocked, "walk did not start")
	waiter := c.goCheck(newEvidenceGroup(t).sm)
	waitFor(t, arrived, "second check did not wait")
	require.Empty(t, journal.loadedNonces())

	close(release)
	require.NoError(t, <-leader)
	require.NoError(t, <-waiter)
	require.Equal(t, nonces(2, latest), journal.loadedNonces())
}

func TestColdWalkStuckMatchedTipStops(t *testing.T) {
	const latest = coldWalkLongNonces + 8
	st := progressState(latest)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(latest, root)}
	peer := &coldPeer{head: func() (uint64, []byte, error) { return 1, root, nil }}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}

	for range 3 {
		accept, err := c.check()
		require.NoError(t, err)
		require.True(t, accept)
	}
	require.Equal(t, 2, countNonce(journal.loadedNonces(), 2),
		"the first walk is progress, the second is not, the third sends the payload alone")
}

func TestColdWalkCancelDoesNotSpendAnAttempt(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	peer := &coldPeer{head: coldNotFound}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}

	for _, at := range []uint64{2, 3} {
		blocked, _ := journal.arm(at)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := c.run(ctx, 2*time.Second)
			done <- err
		}()
		waitFor(t, blocked, "walk did not reach the armed nonce")
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	}
	require.Equal(t, []uint64{1, 2}, journal.loadedNonces())

	accept, err := c.check()
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, nonces(1, 4), journal.loadedNonces())
	require.Equal(t, nonces(1, 4), peer.sentNonces())
}

func TestColdWalkWaitBudgetDoesNotAccept(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	blocked, release := journal.arm(1)
	peer := &coldPeer{head: coldNotFound}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}
	arrived := signalColdWait(t)

	leader := c.goCheck(newEvidenceGroup(t).sm)
	waitFor(t, blocked, "cold walk did not start")

	type result struct {
		accept bool
		err    error
	}
	waiter := make(chan result, 1)
	ev := newEvidenceGroup(t).sm
	ctx := WithColdWalk(context.Background(), c.gate, c.key)
	go func() {
		accept, err := runColdCheck(t, ctx, st, peer, journal, ev, 200*time.Millisecond)
		waiter <- result{accept, err}
	}()
	waitFor(t, arrived, "waiting check did not join")
	got := <-waiter
	require.ErrorIs(t, got.err, context.DeadlineExceeded)
	require.False(t, got.accept)
	require.Empty(t, journal.loadedNonces())

	close(release)
	require.NoError(t, <-leader)
	accept, err := c.check()
	require.NoError(t, err)
	require.True(t, accept)
}

func TestColdWalkBusyDoesNotClearTheMark(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	busy := errors.New("storage busy")
	var mu sync.Mutex
	phase := 0
	peer := &coldPeer{head: func() (uint64, []byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch phase {
		case 1:
			return 0, nil, busy
		case 2:
			return 0, nil, errors.New("dial")
		default:
			return 0, nil, nil
		}
	}}
	gate := NewColdWalks()
	unreachable := func(err error) bool {
		return err != nil && !errors.Is(err, busy) && !sessionNotFound(err)
	}
	check := func() (bool, error) {
		return VerifyRefusedProgress(
			WithColdWalk(context.Background(), gate, "exec"),
			st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, newEvidenceGroup(t).sm, peer, journal,
			50*time.Millisecond, 2*time.Second, deadlinePassedRefused(st, 1), unreachable,
		)
	}
	for _, p := range []int{0, 1, 2, 0} {
		mu.Lock()
		phase = p
		mu.Unlock()
		accept, err := check()
		require.NoError(t, err)
		require.True(t, accept)
	}
	require.Equal(t, nonces(1, 4), journal.loadedNonces())
}

func TestColdWalkExecutorsStaySeparate(t *testing.T) {
	st := progressState(4)
	root := bytes.Repeat([]byte{1}, 32)
	journal := &nonceJournal{diffs: progressDiffs(4, root)}
	blocked, release := journal.arm(1)
	peer := &coldPeer{head: coldNotFound}
	gate := NewColdWalks()
	a := coldChecker{t: t, gate: gate, key: "a", st: st, peer: peer, journal: journal}
	b := coldChecker{t: t, gate: gate, key: "b", st: st, peer: peer, journal: journal}

	first := a.goCheck(newEvidenceGroup(t).sm)
	waitFor(t, blocked, "first executor did not start")
	second := b.goCheck(newEvidenceGroup(t).sm)
	require.NoError(t, <-second)
	require.Equal(t, nonces(1, 4), journal.loadedNonces())

	close(release)
	require.NoError(t, <-first)
	require.Equal(t, 2, countNonce(journal.loadedNonces(), 1))
}

func countNonce(nonces []uint64, want uint64) int {
	n := 0
	for _, nonce := range nonces {
		if nonce == want {
			n++
		}
	}
	return n
}
