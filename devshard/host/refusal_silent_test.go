package host

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// silentPeer answers SessionHead with heads[0] unless blockHead is set, and
// holds the payload call open while blockReceipt is set.
type silentPeer struct {
	progressPeer
	blockHead    bool
	blockReceipt bool
}

func (p *silentPeer) SessionHead(ctx context.Context) (uint64, []byte, error) {
	if p.blockHead {
		<-ctx.Done()
		return 0, nil, ctx.Err()
	}
	return p.progressPeer.SessionHead(ctx)
}

func (p *silentPeer) ChallengeReceipt(ctx context.Context, id uint64, payload *InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	if p.blockReceipt {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	return p.progressPeer.ChallengeReceipt(ctx, id, payload, diffs)
}

func (p *silentPeer) ChallengeReceiptWires(ctx context.Context, id uint64, payload *InferencePayload, diffs []types.DiffWire, reload WireReload) ([]byte, []*types.DevshardTx, error) {
	if p.blockReceipt {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	return p.progressPeer.ChallengeReceiptWires(ctx, id, payload, diffs, reload)
}

func newSilentPeer(root []byte, nonce uint64) *silentPeer {
	return &silentPeer{progressPeer: progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{{nonce: nonce, root: root}},
		steps: answeredStep(),
	}}
}

func runSilentCheck(t *testing.T, ctx context.Context, peer *silentPeer, journal RefusalJournal) (bool, error) {
	t.Helper()
	st := progressState(2)
	return VerifyRefusedProgress(
		ctx, st, 1, st.Inferences[1], testPayload(), nil,
		peer, nil, due(newEvidenceGroup(t).sm), peer, journal,
		50*time.Millisecond, 200*time.Millisecond, progressUnreachable,
	)
}

// An executor that holds SessionHead open fails the first silentExecutorChecks
// checks on this verifier's deadline; the next one accepts the timeout.
func TestRefusalSilentHeadAcceptsAfterCap(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	journal := &progressJournal{diffs: progressDiffs(2, root)}
	peer := newSilentPeer(root, 2)
	peer.blockHead = true
	ctx := WithColdWalk(context.Background(), NewColdWalks(), "executor")

	for i := 0; i < silentExecutorChecks; i++ {
		accept, err := runSilentCheck(t, ctx, peer, journal)
		require.ErrorIs(t, err, context.DeadlineExceeded, "check %d", i)
		require.False(t, accept)
	}
	accept, err := runSilentCheck(t, ctx, peer, journal)
	require.NoError(t, err)
	require.True(t, accept)
	require.Empty(t, peer.pages)
}

// An executor that answers SessionHead and holds the payload call open is
// counted the same way.
func TestRefusalSilentPayloadAcceptsAfterCap(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	journal := &progressJournal{diffs: progressDiffs(2, root)}
	peer := newSilentPeer(root, 2)
	peer.blockReceipt = true
	ctx := WithColdWalk(context.Background(), NewColdWalks(), "executor")

	for i := 0; i < silentExecutorChecks; i++ {
		accept, err := runSilentCheck(t, ctx, peer, journal)
		require.ErrorIs(t, err, context.DeadlineExceeded, "check %d", i)
		require.False(t, accept)
	}
	accept, err := runSilentCheck(t, ctx, peer, journal)
	require.NoError(t, err)
	require.True(t, accept)
}

// A check the executor answers clears the count.
func TestRefusalAnsweredCheckClearsSilentCount(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	journal := &progressJournal{diffs: progressDiffs(2, root)}
	peer := newSilentPeer(root, 2)
	ctx := WithColdWalk(context.Background(), NewColdWalks(), "executor")

	peer.blockHead = true
	for i := 0; i < silentExecutorChecks; i++ {
		_, err := runSilentCheck(t, ctx, peer, journal)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	peer.blockHead = false
	_, err := runSilentCheck(t, ctx, peer, journal)
	require.NoError(t, err)

	peer.blockHead = true
	accept, err := runSilentCheck(t, ctx, peer, journal)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, accept)
}

// A cancelled request does not count: the caller, not the executor, ended
// the check.
func TestRefusalCancelledRequestNotCountedSilent(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	journal := &progressJournal{diffs: progressDiffs(2, root)}
	peer := newSilentPeer(root, 2)
	peer.blockHead = true
	gate := NewColdWalks()
	st := progressState(2)

	for i := 0; i <= silentExecutorChecks+1; i++ {
		ctx, cancel := context.WithCancel(WithColdWalk(context.Background(), gate, "executor"))
		timer := time.AfterFunc(20*time.Millisecond, cancel)
		accept, err := VerifyRefusedProgress(
			ctx, st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, due(newEvidenceGroup(t).sm), peer, journal,
			time.Second, time.Second, progressUnreachable,
		)
		timer.Stop()
		cancel()
		require.ErrorIs(t, err, context.Canceled, "check %d", i)
		require.False(t, accept)
	}
}

// pageThenHoldPeer answers the first catch-up page of each check and holds the next.
type pageThenHoldPeer struct {
	silentPeer
	catchUps int
}

func (p *pageThenHoldPeer) ChallengeCatchUp(ctx context.Context, id uint64, diffs []types.DiffWire, v string) error {
	p.catchUps++
	if p.catchUps == 1 {
		return p.progressPeer.ChallengeCatchUp(ctx, id, diffs, v)
	}
	<-ctx.Done()
	return ctx.Err()
}

// Answering one page and holding the next counts as silent too, for a short
// walk from a matched tip, a cold walk, and a long gated walk.
func TestRefusalPageThenHoldAcceptsAfterCap(t *testing.T) {
	cases := []struct {
		name   string
		latest uint64
		cold   bool
	}{
		{"short", 6, false},
		{"cold", 6, true},
		{"long", coldWalkLongNonces + 20, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := bytes.Repeat([]byte{1}, 32)
			journal := &nonceJournal{diffs: progressDiffs(int(tc.latest), root)}
			sp := newSilentPeer(root, 1)
			if tc.cold {
				sp.heads[0].nonce, sp.heads[0].root, sp.heads[0].err = 0, nil, errors.New("session not found")
			}
			sp.blockReceipt = true
			peer := &pageThenHoldPeer{silentPeer: *sp}
			ctx := WithColdWalk(context.Background(), NewColdWalks(), "executor")
			st := progressState(tc.latest)
			for i := 0; i <= silentExecutorChecks; i++ {
				peer.catchUps = 0
				accept, err := VerifyRefusedProgress(ctx, st, 1, st.Inferences[1], testPayload(), nil,
					peer, nil, due(newEvidenceGroup(t).sm), peer, journal,
					50*time.Millisecond, 200*time.Millisecond, progressUnreachable)
				if i < silentExecutorChecks {
					require.ErrorIs(t, err, context.DeadlineExceeded, "check %d", i)
					require.False(t, accept)
					continue
				}
				require.NoError(t, err)
				require.True(t, accept)
			}
		})
	}
}

// honestColdPeer applies each page after a delay and reports its real tip.
type honestColdPeer struct {
	progressPeer
	mu      sync.Mutex
	applied uint64
	root    []byte
	delay   time.Duration
}

func (p *honestColdPeer) SessionHead(context.Context) (uint64, []byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.applied == 0 {
		return 0, nil, errors.New("session not found")
	}
	return p.applied, p.root, nil
}

func (p *honestColdPeer) ChallengeCatchUp(ctx context.Context, _ uint64, diffs []types.DiffWire, _ string) error {
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	p.mu.Lock()
	if n := diffs[len(diffs)-1].Nonce; n > p.applied {
		p.applied = n
	}
	p.mu.Unlock()
	return nil
}

// A cold executor that keeps applying pages past the deadline is not counted
// silent; the walk resumes until it is caught up.
func TestRefusalProgressingWalkNotCountedSilent(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	const latest = 12
	journal := &nonceJournal{diffs: progressDiffs(latest, root)}
	peer := &honestColdPeer{root: root, delay: 40 * time.Millisecond}
	peer.progressPeer = progressPeer{heads: []struct {
		nonce uint64
		root  []byte
		err   error
	}{{}}, steps: answeredStep()}
	ctx := WithColdWalk(context.Background(), NewColdWalks(), "executor")
	st := progressState(latest)
	for i := 0; i < 8; i++ {
		accept, err := VerifyRefusedProgress(ctx, st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, due(newEvidenceGroup(t).sm), peer, journal,
			50*time.Millisecond, 200*time.Millisecond, progressUnreachable)
		if err == nil {
			// The last page rides with the payload, which the executor answered.
			require.True(t, accept)
			require.Equal(t, uint64(latest-1), peer.applied)
			require.NotEmpty(t, peer.sawPayload)
			require.True(t, peer.sawPayload[len(peer.sawPayload)-1])
			return
		}
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	t.Fatalf("walk never finished, applied %d", peer.applied)
}

// fakeProgressPeer answers one page per check, reports the matched tip at its
// end, and holds every later page and the payload.
type fakeProgressPeer struct {
	progressPeer
	mu       sync.Mutex
	reported uint64
	root     []byte
	answered int
}

func (p *fakeProgressPeer) SessionHead(context.Context) (uint64, []byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reported, p.root, nil
}

func (p *fakeProgressPeer) ChallengeCatchUp(ctx context.Context, _ uint64, diffs []types.DiffWire, _ string) error {
	p.mu.Lock()
	if p.answered == 0 {
		p.answered++
		p.reported = diffs[len(diffs)-1].Nonce
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (p *fakeProgressPeer) ChallengeReceipt(ctx context.Context, _ uint64, _ *InferencePayload, _ []types.Diff) ([]byte, []*types.DevshardTx, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

func (p *fakeProgressPeer) ChallengeReceiptWires(ctx context.Context, _ uint64, _ *InferencePayload, _ []types.DiffWire, _ WireReload) ([]byte, []*types.DevshardTx, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

// Faked progress runs out with the journal. Checks whose request deadline is
// the verify window (guardCompletion retries) count a held payload, so the
// timeout is accepted within pages+cap checks.
func TestRefusalFakeProgressBoundedByJournal(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	const latest = 12
	journal := &nonceJournal{diffs: progressDiffs(latest, root)}
	peer := &fakeProgressPeer{root: root, reported: 1}
	peer.progressPeer = progressPeer{steps: answeredStep()}
	gate := NewColdWalks()
	st := progressState(latest)
	const sweeps = 3
	for i := 0; i < latest+silentExecutorChecks+1; i++ {
		peer.answered = 0
		ctx, cancel := context.WithCancel(WithColdWalk(context.Background(), gate, "executor"))
		if i >= sweeps {
			cancel()
			ctx, cancel = context.WithTimeout(WithColdWalk(context.Background(), gate, "executor"), 200*time.Millisecond)
		}
		accept, err := VerifyRefusedProgress(ctx, st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, due(newEvidenceGroup(t).sm), peer, journal,
			50*time.Millisecond, 200*time.Millisecond, progressUnreachable)
		cancel()
		if err == nil {
			require.True(t, accept)
			return
		}
		require.ErrorIs(t, err, context.DeadlineExceeded, "check %d", i)
	}
	t.Fatalf("not accepted, executor reported tip %d", peer.reported)
}

// A check shorter than half the query timeout is not counted.
func TestRefusalShortWindowNotCountedSilent(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	journal := &progressJournal{diffs: progressDiffs(2, root)}
	peer := newSilentPeer(root, 2)
	peer.blockHead = true
	gate := NewColdWalks()
	st := progressState(2)
	for i := 0; i <= silentExecutorChecks+1; i++ {
		ctx, cancel := context.WithTimeout(WithColdWalk(context.Background(), gate, "executor"), 40*time.Millisecond)
		accept, err := VerifyRefusedProgress(ctx, st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, due(newEvidenceGroup(t).sm), peer, journal,
			time.Second, time.Second, progressUnreachable)
		cancel()
		require.ErrorIs(t, err, context.DeadlineExceeded, "check %d", i)
		require.False(t, accept)
	}
}

// A SessionHead cut to less than a quarter of queryTimeout by a short request
// deadline is not counted, even when the window passes the floor.
func TestRefusalShortHeadBudgetNotCountedSilent(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	journal := &progressJournal{diffs: progressDiffs(2, root)}
	peer := newSilentPeer(root, 2)
	peer.blockHead = true
	gate := NewColdWalks()
	st := progressState(2)
	for i := 0; i <= silentExecutorChecks+1; i++ {
		ctx, cancel := context.WithTimeout(WithColdWalk(context.Background(), gate, "executor"), 60*time.Millisecond)
		accept, err := VerifyRefusedProgress(ctx, st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, due(newEvidenceGroup(t).sm), peer, journal,
			100*time.Millisecond, time.Second, progressUnreachable)
		cancel()
		require.ErrorIs(t, err, context.DeadlineExceeded, "check %d", i)
		require.False(t, accept)
	}
}

// A held SessionHead counts in a retry given nearly the full queryTimeout,
// whatever its own share; a shorter request is no verdict.
func TestRefusalHeldHeadInFullRetryCounts(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	journal := &progressJournal{diffs: progressDiffs(2, root)}
	peer := newSilentPeer(root, 2)
	peer.blockHead = true
	gate := NewColdWalks()
	st := progressState(2)
	check := func(request time.Duration) (bool, error) {
		ctx, cancel := context.WithTimeout(WithColdWalk(context.Background(), gate, "executor"), request)
		defer cancel()
		return VerifyRefusedProgress(ctx, st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, due(newEvidenceGroup(t).sm), peer, journal,
			100*time.Millisecond, 400*time.Millisecond, progressUnreachable)
	}
	for i := 0; i < silentExecutorChecks; i++ {
		_, err := check(98 * time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded, "retry %d", i)
		_, err = check(80 * time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded, "short request %d", i)
	}
	accept, err := check(98 * time.Millisecond)
	require.NoError(t, err)
	require.True(t, accept)
}
