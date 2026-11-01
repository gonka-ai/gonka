package host

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

func TestChallengeClaimsDecodeEachNonceOnce(t *testing.T) {
	f := newWriterFixture(t, 10)
	f.store.gate = make(chan struct{})
	f.store.entered = make(chan struct{}, 1)
	wires, err := diffWiresFromDiffs(f.diffs)
	require.NoError(t, err)

	beforeClaimed := promtest.ToFloat64(observability.ChallengeDiffCounter("claimed"))
	beforeWaited := promtest.ToFloat64(observability.ChallengeDiffCounter("waited"))
	beforeApplied := promtest.ToFloat64(observability.ChallengeDiffCounter("applied"))

	first := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), wires, nil)
		f.host.mu.Unlock()
		first <- err
	}()
	<-f.store.entered

	const waiters = 29
	var waiting atomic.Int32
	testBeforeClaimWait = func() { waiting.Add(1) }
	t.Cleanup(func() { testBeforeClaimWait = nil })

	var wg sync.WaitGroup
	errs := make(chan error, waiters)
	for range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.host.mu.Lock()
			_, err := f.host.applyClaimedDiffsLocked(context.Background(), wires, nil)
			f.host.mu.Unlock()
			errs <- err
		}()
	}
	// The first claim is already in the list once the store gate is entered, so
	// that length does not mean the others have seen it. Releasing the gate
	// before they wait lets them observe the nonces as already applied.
	require.Eventually(t, func() bool {
		return waiting.Load() == waiters
	}, 2*time.Second, time.Millisecond)

	close(f.store.gate)
	require.NoError(t, <-first)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, uint64(10), f.host.LatestNonce())
	claimed := promtest.ToFloat64(observability.ChallengeDiffCounter("claimed")) - beforeClaimed
	waited := promtest.ToFloat64(observability.ChallengeDiffCounter("waited")) - beforeWaited
	applied := promtest.ToFloat64(observability.ChallengeDiffCounter("applied")) - beforeApplied
	require.Equal(t, float64(10), claimed)
	require.Greater(t, waited, float64(0))
	require.Equal(t, float64(300), claimed+waited+applied)
}

func TestChallengeClaimFarRangeDoesNotBlockTheTip(t *testing.T) {
	f := newWriterFixture(t, 20)
	f.host.mu.Lock()
	_, err := f.host.applyDiffsLocked(context.Background(), f.diffs[:10])
	require.NoError(t, err)
	far := &nonceClaim{from: 900, to: 1000, done: make(chan struct{})}
	f.host.writer.claims = []*nonceClaim{far}
	wires, err := diffWiresFromDiffs(f.diffs[10:20])
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = f.host.applyClaimedDiffsLocked(ctx, wires, nil)
	f.host.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, uint64(20), f.host.LatestNonce())
	select {
	case <-far.done:
		t.Fatal("the tip request ended the far claim")
	default:
	}
}

func TestChallengeClaimRetryAfterFailedClaim(t *testing.T) {
	f := newWriterFixture(t, 5)
	f.store.gate = make(chan struct{})
	f.store.entered = make(chan struct{}, 1)
	var calls atomic.Int32
	f.store.before = func(context.Context) error {
		if calls.Add(1) <= storage.DefaultPersistMaxAttempts {
			return errors.New("disk")
		}
		return nil
	}
	wires, err := diffWiresFromDiffs(f.diffs)
	require.NoError(t, err)
	before := promtest.ToFloat64(observability.ChallengeClaimRetryCounter("claim_failed"))

	first := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), wires, nil)
		f.host.mu.Unlock()
		first <- err
	}()
	<-f.store.entered
	second := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), wires, nil)
		f.host.mu.Unlock()
		second <- err
	}()
	require.Eventually(t, func() bool {
		f.host.mu.Lock()
		n := len(f.host.writer.claims)
		f.host.mu.Unlock()
		return n == 1
	}, 2*time.Second, time.Millisecond)
	close(f.store.gate)

	require.Error(t, <-first)
	require.NoError(t, <-second)
	require.Equal(t, uint64(5), f.host.LatestNonce())
	require.Equal(t, float64(1), promtest.ToFloat64(observability.ChallengeClaimRetryCounter("claim_failed"))-before)
	f.host.mu.Lock()
	require.Empty(t, f.host.writer.claims)
	f.host.mu.Unlock()
}

func TestChallengeStackedGapRetriesOnce(t *testing.T) {
	f := newWriterFixture(t, 10)
	f.store.gate = make(chan struct{})
	f.store.entered = make(chan struct{}, 1)
	f.store.before = func(context.Context) error { return errors.New("disk") }
	low, err := diffWiresFromDiffs(f.diffs[:5])
	require.NoError(t, err)
	high, err := diffWiresFromDiffs(f.diffs[5:])
	require.NoError(t, err)
	before := promtest.ToFloat64(observability.ChallengeClaimRetryCounter("gap"))

	first := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), low, nil)
		f.host.mu.Unlock()
		first <- err
	}()
	<-f.store.entered
	second := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), high, nil)
		f.host.mu.Unlock()
		second <- err
	}()
	require.Eventually(t, func() bool { return f.queued() == 1 }, 2*time.Second, time.Millisecond)
	close(f.store.gate)

	require.Error(t, <-first)
	err = <-second
	require.Error(t, err)
	require.ErrorIs(t, err, ErrReconcileGap)
	require.Equal(t, float64(1), promtest.ToFloat64(observability.ChallengeClaimRetryCounter("gap"))-before)
}

func TestChallengeClaimAttemptCap(t *testing.T) {
	f := newWriterFixture(t, 1)
	done := make(chan struct{})
	close(done)
	f.host.mu.Lock()
	f.host.writer.claims = []*nonceClaim{{from: 1, to: 1, done: done, err: errors.New("stuck")}}
	wires, err := diffWiresFromDiffs(f.diffs[:1])
	require.NoError(t, err)
	before := promtest.ToFloat64(observability.ChallengeClaimRetryCounter("claim_failed"))
	_, err = f.host.applyClaimedDiffsLocked(context.Background(), wires, nil)
	f.host.mu.Unlock()
	require.Error(t, err)
	require.Equal(t, float64(challengeClaimAttempts), promtest.ToFloat64(observability.ChallengeClaimRetryCounter("claim_failed"))-before)
}

func TestChallengeFailedClaimWaitDoesNotBurnTheCap(t *testing.T) {
	f := newWriterFixture(t, 1)
	honest, err := diffWiresFromDiffs(f.diffs[:1])
	require.NoError(t, err)
	bad := append([]types.DiffWire(nil), honest...)
	bad[0].UserSig = bytes.Clone(bad[0].UserSig)
	bad[0].UserSig[0] ^= 0xff

	releaseBad := make(chan struct{})
	var blockBad atomic.Bool
	blockBad.Store(true)
	var releaseBadOnce sync.Once
	t.Cleanup(func() { releaseBadOnce.Do(func() { close(releaseBad) }) })
	testBeforeClaimSig = func() {
		if blockBad.Load() {
			<-releaseBad
		}
	}
	t.Cleanup(func() { testBeforeClaimSig = nil })

	waitHold := make(chan struct{})
	var blockWait atomic.Bool
	blockWait.Store(true)
	var releaseWaitOnce sync.Once
	t.Cleanup(func() { releaseWaitOnce.Do(func() { close(waitHold) }) })
	enteredWait := make(chan struct{}, 1)
	testBeforeClaimWait = func() {
		if !blockWait.Load() {
			return
		}
		select {
		case enteredWait <- struct{}{}:
		default:
		}
		<-waitHold
	}
	t.Cleanup(func() { testBeforeClaimWait = nil })

	startBad := func() <-chan error {
		out := make(chan error, 1)
		go func() {
			f.host.mu.Lock()
			_, err := f.host.applyClaimedDiffsLocked(context.Background(), bad, nil)
			f.host.mu.Unlock()
			out <- err
		}()
		return out
	}
	await := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal(what)
		}
	}
	claimListed := func() bool {
		t.Helper()
		f.host.mu.Lock()
		n := len(f.host.writer.claims)
		f.host.mu.Unlock()
		return n == 1
	}

	badErr := startBad()
	require.Eventually(t, claimListed, 2*time.Second, time.Millisecond)
	honestErr := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), honest, nil)
		f.host.mu.Unlock()
		honestErr <- err
	}()
	await(enteredWait, "honest request did not wait on the bad claim")

	const failures = challengeClaimAttempts + 1
	before := promtest.ToFloat64(observability.ChallengeClaimRetryCounter("claim_failed"))
	for i := 1; i < failures; i++ {
		releaseBad <- struct{}{}
		require.Error(t, <-badErr)
		badErr = startBad()
		require.Eventually(t, claimListed, 2*time.Second, time.Millisecond)
		waitHold <- struct{}{}
		await(enteredWait, "honest request did not wait on the next bad claim")
	}
	releaseBad <- struct{}{}
	require.Error(t, <-badErr)
	blockBad.Store(false)
	blockWait.Store(false)
	waitHold <- struct{}{}

	require.NoError(t, <-honestErr)
	require.Equal(t, uint64(1), f.host.LatestNonce())
	require.Equal(t, float64(failures), promtest.ToFloat64(observability.ChallengeClaimRetryCounter("claim_failed"))-before)
}

func TestChallengeBadSignatureStopsTheDecode(t *testing.T) {
	f := newWriterFixture(t, 4)
	wires, err := diffWiresFromDiffs(f.diffs)
	require.NoError(t, err)

	t.Run("first diff", func(t *testing.T) {
		bad := append([]types.DiffWire(nil), wires...)
		bad[0].UserSig = []byte{1, 2, 3}
		require.Equal(t, int32(1), applyBadClaim(t, f, bad))
		require.Equal(t, uint64(0), f.host.LatestNonce())
	})

	t.Run("third diff", func(t *testing.T) {
		bad := append([]types.DiffWire(nil), wires...)
		bad[2].UserSig = []byte{1, 2, 3}
		require.Equal(t, int32(3), applyBadClaim(t, f, bad))
		require.Equal(t, uint64(2), f.host.LatestNonce())
	})
}

func applyBadClaim(t *testing.T, f *writerFixture, wires []types.DiffWire) int32 {
	t.Helper()
	var decodes atomic.Int32
	testBeforeClaimDecode = func() { decodes.Add(1) }
	t.Cleanup(func() { testBeforeClaimDecode = nil })
	f.host.mu.Lock()
	_, err := f.host.applyClaimedDiffsLocked(context.Background(), wires, nil)
	f.host.mu.Unlock()
	require.ErrorIs(t, err, types.ErrInvalidUserSig)
	return decodes.Load()
}

func TestChallengeHoleAfterAPrefixReportsTheGap(t *testing.T) {
	f := newWriterFixture(t, 4)
	wires, err := diffWiresFromDiffs(f.diffs)
	require.NoError(t, err)
	wires = []types.DiffWire{wires[0], wires[1], wires[3]}

	f.host.mu.Lock()
	_, err = f.host.applyClaimedDiffsLocked(context.Background(), wires, nil)
	f.host.mu.Unlock()
	require.ErrorIs(t, err, ErrReconcileGap)
	require.Equal(t, uint64(2), f.host.LatestNonce())
}

func TestChallengeBelowLatestDecodesNothing(t *testing.T) {
	f := newWriterFixture(t, 2)
	f.host.mu.Lock()
	_, err := f.host.applyDiffsLocked(context.Background(), f.diffs)
	require.NoError(t, err)
	wires, err := diffWiresFromDiffs(f.diffs)
	require.NoError(t, err)
	wires[0].Txs = []byte{0xff}
	wires[1].Txs = []byte{0xff}
	before := promtest.ToFloat64(observability.ChallengeDiffCounter("applied"))
	_, err = f.host.applyClaimedDiffsLocked(context.Background(), wires, nil)
	f.host.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, float64(2), promtest.ToFloat64(observability.ChallengeDiffCounter("applied"))-before)
}

func TestChallengeWaiterCancelDoesNotEndTheClaim(t *testing.T) {
	f := newWriterFixture(t, 5)
	f.store.gate = make(chan struct{})
	f.store.entered = make(chan struct{}, 1)
	wires, err := diffWiresFromDiffs(f.diffs)
	require.NoError(t, err)

	first := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), wires, nil)
		f.host.mu.Unlock()
		first <- err
	}()
	<-f.store.entered

	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(ctx, wires, nil)
		f.host.mu.Unlock()
		second <- err
	}()
	require.Eventually(t, func() bool {
		f.host.mu.Lock()
		n := len(f.host.writer.claims)
		f.host.mu.Unlock()
		return n == 1
	}, 2*time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-second, context.Canceled)
	f.host.mu.Lock()
	require.Len(t, f.host.writer.claims, 1)
	f.host.mu.Unlock()
	close(f.store.gate)
	require.NoError(t, <-first)
	require.Equal(t, uint64(5), f.host.LatestNonce())
}

func TestApplyRunReleasesLockBetweenChunks(t *testing.T) {
	f := newWriterFixture(t, applyChunkNonces+1)
	fired := make(chan struct{}, 1)
	testAfterApplyChunk = func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { testAfterApplyChunk = nil })
	f.host.mu.Lock()
	_, err := f.host.applyDiffsLocked(context.Background(), f.diffs)
	f.host.mu.Unlock()
	require.NoError(t, err)
	select {
	case <-fired:
	default:
		t.Fatal("a long run held the host lock across every diff")
	}
}

func TestChallengeNonceLimitEndsTheClaim(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	const maxNonce uint32 = 8
	h := newTestHostWithMaxNonce(t, 0, hosts, user, maxNonce)
	activeCap := types.MaxActiveNonce(maxNonce, len(hosts))
	for nonce := uint64(1); nonce <= activeCap; nonce++ {
		diff := testutil.SignDiff(t, user, "escrow-1", nonce, nil)
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
		require.NoError(t, err, "nonce %d", nonce)
	}
	over := activeCap + 1
	diff := testutil.SignDiff(t, user, "escrow-1", over, []*types.DevshardTx{testutil.StartTx(over)})
	wires, err := diffWiresFromDiffs([]types.Diff{diff})
	require.NoError(t, err)
	h.mu.Lock()
	_, err = h.applyClaimedDiffsLocked(context.Background(), wires, nil)
	require.Empty(t, h.writer.claims)
	h.mu.Unlock()
	require.ErrorIs(t, err, types.ErrNonceLimitExceeded)
}

// countingVerifier counts RecoverAddress. When entered is set, the first call
// waits until release is closed, so a test can pile waiters onto the claim.
type countingVerifier struct {
	inner   signing.Verifier
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (v *countingVerifier) RecoverAddress(message, signature []byte) (string, error) {
	if v.calls.Add(1) == 1 && v.entered != nil {
		close(v.entered)
		<-v.release
	}
	return v.inner.RecoverAddress(message, signature)
}

func newExecutorHost(t *testing.T, n int, v signing.Verifier) (*Host, []types.Diff, *gatedBatchStore) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	if v == nil {
		v = signing.NewSecp256k1Verifier()
	}
	inner := storage.NewMemory()
	require.NoError(t, inner.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: 10000,
	}))
	probe, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)
	diffs := make([]types.Diff, 0, n)
	for nonce := uint64(1); nonce <= uint64(n); nonce++ {
		var txs []*types.DevshardTx
		if nonce == 1 {
			txs = []*types.DevshardTx{testutil.StartTx(1)}
		}
		root, err := probe.ApplyLocal(nonce, txs)
		require.NoError(t, err)
		diffs = append(diffs, testutil.SignDiffWithRoot(t, user, "escrow-1", nonce, txs, root))
	}
	store := &gatedBatchStore{Memory: inner}
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), v, inner)
	require.NoError(t, err)
	engine := newBlockingInferenceEngine()
	t.Cleanup(func() { close(engine.release) })
	h, err := NewHost(sm, hosts[1], engine, "escrow-1", group, nil,
		WithGrace(uint64(n)+10), WithStorage(store), WithVerifier(v))
	require.NoError(t, err)
	return h, diffs, store
}

func waitForClaimWaiter(t *testing.T) <-chan struct{} {
	t.Helper()
	arrived := make(chan struct{}, 1)
	testBeforeClaimWait = func() {
		select {
		case arrived <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { testBeforeClaimWait = nil })
	return arrived
}

func TestChallengeThirtyPagesTwoSigChecks(t *testing.T) {
	const page = 4
	v := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	h, diffs, _ := newExecutorHost(t, page, v)
	wires, err := diffWiresFromDiffs(diffs)
	require.NoError(t, err)
	before := promtest.ToFloat64(observability.ChallengeDiffCounter("claimed"))

	var wg sync.WaitGroup
	receipts := make(chan []byte, 30)
	errs := make(chan error, 30)
	for range 3 {
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				receipt, _, err := h.ChallengeReceiptWire(context.Background(), 1, defaultPayload(), wires, nil)
				errs <- err
				receipts <- receipt
			}()
		}
	}
	wg.Wait()
	close(errs)
	close(receipts)

	var receipt []byte
	for err := range errs {
		require.NoError(t, err)
	}
	n := 0
	for got := range receipts {
		require.NotEmpty(t, got)
		if receipt == nil {
			receipt = got
		}
		require.Equal(t, receipt, got)
		n++
	}
	require.Equal(t, 30, n)
	require.Equal(t, int32(2*page), v.calls.Load())
	require.Equal(t, float64(page), promtest.ToFloat64(observability.ChallengeDiffCounter("claimed"))-before)
	root, err := h.StateRoot()
	require.NoError(t, err)
	require.Equal(t, diffs[page-1].PostStateRoot, root)
}

func TestChallengeBadSignatureWaitersUseTheirOwnBytes(t *testing.T) {
	v := &countingVerifier{
		inner:   signing.NewSecp256k1Verifier(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	h, diffs, store := newExecutorHost(t, 4, v)
	wires, err := diffWiresFromDiffs(diffs)
	require.NoError(t, err)
	bad := append([]types.DiffWire(nil), wires...)
	for i := range bad {
		bad[i].UserSig = []byte{1, 2, 3}
	}
	arrived := waitForClaimWaiter(t)

	first := make(chan error, 1)
	go func() {
		_, _, err := h.ChallengeReceiptWire(context.Background(), 1, defaultPayload(), bad, nil)
		first <- err
	}()
	<-v.entered

	type result struct {
		receipt []byte
		err     error
	}
	waiters := make(chan result, 2)
	for range 2 {
		go func() {
			receipt, _, err := h.ChallengeReceiptWire(context.Background(), 1, defaultPayload(), wires, nil)
			waiters <- result{receipt, err}
		}()
	}
	select {
	case <-arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not wait on the claim")
	}
	close(v.release)

	require.Error(t, <-first)
	for range 2 {
		got := <-waiters
		require.NoError(t, got.err)
		require.NotEmpty(t, got.receipt)
	}
	require.Equal(t, uint64(4), h.LatestNonce())
	require.Len(t, store.calls(), 1)
	root, err := h.StateRoot()
	require.NoError(t, err)
	require.True(t, bytes.Equal(diffs[3].PostStateRoot, root))
}

func TestChallengeClaimantCancelBeforeWriterWaitersApply(t *testing.T) {
	v := &countingVerifier{
		inner:   signing.NewSecp256k1Verifier(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	h, diffs, store := newExecutorHost(t, 4, v)
	wires, err := diffWiresFromDiffs(diffs)
	require.NoError(t, err)
	arrived := waitForClaimWaiter(t)
	ctx, cancel := context.WithCancel(context.Background())

	first := make(chan error, 1)
	go func() {
		_, _, err := h.ChallengeReceiptWire(ctx, 1, defaultPayload(), wires, nil)
		first <- err
	}()
	<-v.entered

	waiter := make(chan error, 1)
	var receipt []byte
	go func() {
		var err error
		receipt, _, err = h.ChallengeReceiptWire(context.Background(), 1, defaultPayload(), wires, nil)
		waiter <- err
	}()
	select {
	case <-arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not wait on the claim")
	}
	cancel()
	close(v.release)

	require.ErrorIs(t, <-first, context.Canceled)
	require.NoError(t, <-waiter)
	require.NotEmpty(t, receipt)
	require.Equal(t, uint64(4), h.LatestNonce())
	require.Len(t, store.calls(), 1)
	h.mu.Lock()
	require.Empty(t, h.writer.claims)
	h.mu.Unlock()
}

func TestChallengeOwnBytesFillTheHoleAfterTheLowerClaimFails(t *testing.T) {
	f := newWriterFixture(t, 10)
	f.store.gate = make(chan struct{})
	f.store.entered = make(chan struct{}, 1)
	var calls atomic.Int32
	f.store.before = func(context.Context) error {
		if calls.Add(1) <= storage.DefaultPersistMaxAttempts {
			return errors.New("disk")
		}
		return nil
	}
	low, err := diffWiresFromDiffs(f.diffs[:5])
	require.NoError(t, err)
	full, err := diffWiresFromDiffs(f.diffs)
	require.NoError(t, err)
	arrived := waitForClaimWaiter(t)
	before := promtest.ToFloat64(observability.ChallengeClaimRetryCounter("claim_failed"))

	first := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), low, nil)
		f.host.mu.Unlock()
		first <- err
	}()
	<-f.store.entered
	second := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), full, nil)
		f.host.mu.Unlock()
		second <- err
	}()
	select {
	case <-arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("the full request did not wait on the lower claim")
	}
	close(f.store.gate)

	require.Error(t, <-first)
	require.NoError(t, <-second)
	require.Equal(t, uint64(10), f.host.LatestNonce())
	require.Equal(t, float64(1), promtest.ToFloat64(observability.ChallengeClaimRetryCounter("claim_failed"))-before)
	f.host.mu.Lock()
	require.Empty(t, f.host.writer.claims)
	f.host.mu.Unlock()
}

func TestChallengeStackedSlowLowerClaimStillApplies(t *testing.T) {
	f := newWriterFixture(t, 10)
	releaseCh := make(chan struct{})
	entered := make(chan struct{})
	var firstSig atomic.Bool
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(release)
	testBeforeClaimSig = func() {
		if firstSig.CompareAndSwap(false, true) {
			close(entered)
			<-releaseCh
		}
	}
	t.Cleanup(func() { testBeforeClaimSig = nil })
	waiting := make(chan struct{})
	testBeforeStackedWait = func() { close(waiting) }
	t.Cleanup(func() { testBeforeStackedWait = nil })
	low, err := diffWiresFromDiffs(f.diffs[:5])
	require.NoError(t, err)
	high, err := diffWiresFromDiffs(f.diffs[5:])
	require.NoError(t, err)
	before := promtest.ToFloat64(observability.ChallengeClaimRetryCounter("gap"))

	first := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), low, nil)
		f.host.mu.Unlock()
		first <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("lower claim did not reach the signature check")
	}
	second := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), high, nil)
		f.host.mu.Unlock()
		second <- err
	}()
	select {
	case <-waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("stacked claim did not wait for the lower claim to queue")
	}
	release()

	require.NoError(t, <-first)
	require.NoError(t, <-second)
	require.Equal(t, uint64(10), f.host.LatestNonce())
	require.Equal(t, before, promtest.ToFloat64(observability.ChallengeClaimRetryCounter("gap")))
}

func TestChallengeStackedWaitCapReturnsGap(t *testing.T) {
	f := newWriterFixture(t, 10)
	hold := make(chan struct{})
	started := make(chan struct{})
	var decodes atomic.Int32
	testBeforeClaimDecode = func() {
		if decodes.Add(1) == 1 {
			close(started)
			<-hold
		}
	}
	t.Cleanup(func() { testBeforeClaimDecode = nil })
	testStackedWaitLimit = func(time.Duration) time.Duration { return 20 * time.Millisecond }
	t.Cleanup(func() { testStackedWaitLimit = nil })
	low, err := diffWiresFromDiffs(f.diffs[:5])
	require.NoError(t, err)
	high, err := diffWiresFromDiffs(f.diffs[5:])
	require.NoError(t, err)
	before := promtest.ToFloat64(observability.ChallengeClaimRetryCounter("gap"))

	lower := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), low, nil)
		f.host.mu.Unlock()
		lower <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("lower claim did not start decoding")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	f.host.mu.Lock()
	_, err = f.host.applyClaimedDiffsLocked(ctx, high, nil)
	f.host.mu.Unlock()
	elapsed := time.Since(start)

	require.ErrorIs(t, err, ErrReconcileGap)
	require.NoError(t, ctx.Err())
	require.Less(t, elapsed, 500*time.Millisecond)
	require.Equal(t, float64(1), promtest.ToFloat64(observability.ChallengeClaimRetryCounter("gap"))-before)
	require.Equal(t, int32(6), decodes.Load(), "the gap retry must reuse the decoded run")
	require.Equal(t, uint64(0), f.host.LatestNonce())

	close(hold)
	select {
	case <-lower:
	case <-time.After(2 * time.Second):
		t.Fatal("lower claim did not finish")
	}
}

func TestChallengeStackedSharedBatchFailureRetriesOnce(t *testing.T) {
	f := newWriterFixture(t, 10)
	f.store.before = func(context.Context) error { return errors.New("disk") }
	release := make(chan struct{})
	testBeforeTakeQueue = func() { <-release }
	t.Cleanup(func() { testBeforeTakeQueue = nil })
	low, err := diffWiresFromDiffs(f.diffs[:5])
	require.NoError(t, err)
	high, err := diffWiresFromDiffs(f.diffs[5:])
	require.NoError(t, err)
	before := promtest.ToFloat64(observability.ChallengeClaimRetryCounter("gap"))

	first := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), low, nil)
		f.host.mu.Unlock()
		first <- err
	}()
	require.Eventually(t, func() bool { return f.queued() == 1 }, 2*time.Second, time.Millisecond)

	second := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), high, nil)
		f.host.mu.Unlock()
		second <- err
	}()
	require.Eventually(t, func() bool { return f.queued() == 2 }, 2*time.Second, time.Millisecond)
	close(release)

	require.Error(t, <-first)
	err = <-second
	require.ErrorIs(t, err, ErrReconcileGap)
	require.Equal(t, float64(1), promtest.ToFloat64(observability.ChallengeClaimRetryCounter("gap"))-before)
	require.Equal(t, uint64(0), f.host.LatestNonce())
}

func TestChallengeStackedShrunkLowerClaimDoesNotApplyPastTheHole(t *testing.T) {
	f := newWriterFixture(t, 10)
	releaseCh := make(chan struct{})
	entered := make(chan struct{})
	var firstSig atomic.Bool
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(release)
	testBeforeClaimSig = func() {
		if firstSig.CompareAndSwap(false, true) {
			close(entered)
			<-releaseCh
		}
	}
	t.Cleanup(func() { testBeforeClaimSig = nil })
	waiting := make(chan struct{})
	testBeforeStackedWait = func() {
		select {
		case <-waiting:
		default:
			close(waiting)
		}
	}
	t.Cleanup(func() { testBeforeStackedWait = nil })
	low, err := diffWiresFromDiffs(f.diffs[:5])
	require.NoError(t, err)
	low[2].UserSig = []byte{1, 2, 3}
	high, err := diffWiresFromDiffs(f.diffs[5:])
	require.NoError(t, err)
	before := promtest.ToFloat64(observability.ChallengeClaimRetryCounter("gap"))

	first := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), low, nil)
		f.host.mu.Unlock()
		first <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("lower claim did not reach the signature check")
	}
	second := make(chan error, 1)
	go func() {
		f.host.mu.Lock()
		_, err := f.host.applyClaimedDiffsLocked(context.Background(), high, nil)
		f.host.mu.Unlock()
		second <- err
	}()
	select {
	case <-waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("stacked claim did not wait for the lower claim to queue")
	}
	release()

	require.Error(t, <-first)
	err = <-second
	require.ErrorIs(t, err, ErrReconcileGap)
	require.Equal(t, float64(1), promtest.ToFloat64(observability.ChallengeClaimRetryCounter("gap"))-before)
	require.Equal(t, uint64(2), f.host.LatestNonce())
	for _, call := range f.store.calls() {
		require.LessOrEqual(t, call[1], uint64(2))
	}
}

func TestChallengePredecodedSkipsASecondDecode(t *testing.T) {
	f := newWriterFixture(t, 2)
	wires, err := diffWiresFromDiffs(f.diffs)
	require.NoError(t, err)
	wires[0].Txs = []byte{0xff}
	wires[1].Txs = []byte{0xff}
	f.host.mu.Lock()
	_, err = f.host.applyClaimedDiffsLocked(context.Background(), wires, f.diffs)
	f.host.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, uint64(2), f.host.LatestNonce())
}
