package shadow_test

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/warmup"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

const (
	loadEscrow       = "escrow-1"
	loadModel        = "llama"
	loadStartedAt    = 1000
	loadSlots        = 8
	loadShadowSlot   = 3
	loadReceiptSlot  = 5
	loadBalance      = 1_000_000_000
	loadGrace        = 10
	loadWindow       = time.Second
	loadSmallBurst   = 50
	loadLargeBurst   = 500
	loadVoteSettleIn = 10 * time.Minute

	loadObservedHeight  = 100
	loadDueHeight       = 2000
	loadHeightSyncEvery = 10
	loadFloorRaiseTries = 4
	loadHeartbeatStep   = time.Hour
)

var loadChainHash = []byte{0xaa}

type virtualClock struct {
	mu      sync.Mutex
	current time.Time
}

func newVirtualClock() *virtualClock {
	return &virtualClock{current: time.Unix(loadStartedAt, 0).Add(loadVoteSettleIn)}
}

func (c *virtualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *virtualClock) sleep(_ context.Context, delay time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(delay)
	return nil
}

type heightOracle struct {
	height atomic.Int64
}

func (o *heightOracle) Latest(context.Context) (*blocks.Header, error) {
	return blocks.HashOnlyHeader(o.height.Load(), time.Unix(1, 0).UTC(), "load-chain", append([]byte(nil), loadChainHash...)), nil
}

func (o *heightOracle) At(ctx context.Context, _ int64) (*blocks.Header, error) {
	return o.Latest(ctx)
}

func (o *heightOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *heightOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	closed := make(chan *blocks.Header)
	close(closed)
	return closed, nil
}

type verifyCall struct {
	slot  int
	nonce uint64
	at    time.Time
}

type verifyLog struct {
	mu    sync.Mutex
	calls []verifyCall
}

func (l *verifyLog) record(call verifyCall) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *verifyLog) snapshot() []verifyCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]verifyCall(nil), l.calls...)
}

type verifierPeer struct {
	*user.InProcessClient
	slot         int
	group        []types.SlotAssignment
	signer       *signing.Secp256k1Signer
	accepts      bool
	holdsReceipt bool
	log          *verifyLog
	clock        *virtualClock
}

func (p *verifierPeer) VerifyTimeout(_ context.Context, inferenceID uint64, reason types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff, _ host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	p.log.record(verifyCall{slot: p.slot, nonce: inferenceID, at: p.clock.now()})
	if !p.accepts {
		var mempool []*types.DevshardTx
		if p.holdsReceipt {
			mempool = []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: inferenceID}}}}
		}
		return false, nil, 0, mempool, "", nil
	}
	content := &types.TimeoutVoteContent{EscrowId: loadEscrow, InferenceId: inferenceID, Reason: reason, Accept: true}
	data, err := types.CanonicalSignedBytes(content)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	signature, err := p.signer.Sign(data)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	return true, signature, p.group[p.slot].SlotID, nil, "", nil
}

func (p *verifierPeer) VerifyErrorMiss(context.Context, uint64, []types.Diff, host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "", nil
}

type loadRound struct {
	session *user.Session
	log     *verifyLog
	clock   *virtualClock
	nonces  []uint64
	chain   *chainHeight
}

type chainHeight struct {
	height  atomic.Uint64
	oracles []*heightOracle
	beat    atomic.Int64
}

func (c *chainHeight) set(height uint64) {
	c.height.Store(height)
	for _, oracle := range c.oracles {
		oracle.height.Store(int64(height))
	}
}

func (c *chainHeight) raiseFloor(t *testing.T, session *user.Session, height uint64) {
	t.Helper()
	c.set(height)
	for range loadFloorRaiseTries {
		c.beat.Add(int64(loadHeartbeatStep))
		require.NoError(t, session.MaybeHeartbeat(context.Background()))
		require.NoError(t, session.SendPendingDiff(context.Background()))
		if floor, _, _ := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1); floor >= height {
			return
		}
	}
	floor, _, _ := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.GreaterOrEqual(t, floor, height, "the host acks must carry the floor to the due height")
}

type loadShape struct {
	accepts     bool
	withReceipt bool
}

func newLoadRound(t *testing.T, shape loadShape, timeouts int) loadRound {
	t.Helper()
	signers := make([]*signing.Secp256k1Signer, loadSlots)
	for index := range signers {
		signers[index] = testutil.MustGenerateKey(t)
	}
	creator := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(signers)
	config := testutil.DefaultConfig(loadSlots)
	verifier := signing.NewSecp256k1Verifier()
	chain := &chainHeight{oracles: make([]*heightOracle, loadSlots)}
	chain.height.Store(loadObservedHeight)
	chain.beat.Store(time.Unix(loadStartedAt, 0).UnixNano())
	round := loadRound{log: &verifyLog{}, clock: newVirtualClock(), chain: chain}
	clients := make([]user.HostClient, loadSlots)
	for index := range signers {
		machine := statetest.MustStateMachine(t, loadEscrow, config, group, loadBalance, creator.Address(), verifier)
		chain.oracles[index] = &heightOracle{}
		chain.oracles[index].height.Store(loadObservedHeight)
		executor, err := host.NewHost(machine, signers[index], stub.NewInferenceEngine(), loadEscrow, group, nil, host.WithGrace(loadGrace), host.WithChainOracle(chain.oracles[index]))
		require.NoError(t, err)
		clients[index] = &verifierPeer{
			InProcessClient: &user.InProcessClient{Host: executor},
			slot:            index,
			group:           group,
			signer:          signers[index],
			accepts:         shape.accepts,
			holdsReceipt:    shape.withReceipt && index == loadReceiptSlot,
			log:             round.log,
			clock:           round.clock,
		}
	}
	machine := statetest.MustStateMachine(t, loadEscrow, config, group, loadBalance, creator.Address(), verifier)
	session, err := user.NewSession(machine, creator, loadEscrow, group, clients, verifier,
		user.WithHeightSyncCadence(loadHeightSyncEvery, loadSlots),
		user.WithHeartbeatClock(func() time.Time { return time.Unix(0, chain.beat.Load()) }),
		user.WithObservedHeight(func() (uint64, []byte, bool) { return chain.height.Load(), loadChainHash, true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	round.session = session
	_, err = session.SendInference(context.Background(), user.InferenceParams{
		Model: loadModel, Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: loadStartedAt,
	})
	require.NoError(t, err)
	require.NoError(t, session.SendPendingDiff(context.Background()))
	for len(round.nonces) < timeouts {
		prepared, err := session.PrepareInference(context.Background(), user.InferenceParams{
			Model: loadModel, Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: loadStartedAt,
		})
		require.NoError(t, err)
		if int(prepared.Nonce()%loadSlots) == loadShadowSlot {
			round.nonces = append(round.nonces, prepared.Nonce())
		}
	}
	chain.raiseFloor(t, session, loadDueHeight)
	return round
}

type voteTally struct {
	missed          uint32
	totalCalls      int
	callsPerTimeout float64
	peakPerHost     int
	drainTime       time.Duration
}

func (r loadRound) settleAll(t *testing.T) voteTally {
	t.Helper()
	payload := &host.InferencePayload{Prompt: testutil.TestPrompt, Model: loadModel, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: loadStartedAt}
	posters := func(string, any) (engine.TimeoutPoster, bool) {
		return engine.NewSessionTimeouts(r.session, payload), true
	}
	voter := warmup.Voter{Posters: posters, Pace: warmup.NewVotePacer(r.clock.now, r.clock.sleep)}
	params := user.InferenceParams{Model: loadModel, Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: loadStartedAt}
	var tally voteTally
	startedAt := r.clock.now()
	for _, nonce := range r.nonces {
		voter.Settle(context.Background(), loadEscrow, loadModel, params, nonce, false)
	}
	tally.missed = r.session.StateMachine().SnapshotState().HostStats[loadShadowSlot].Missed
	calls := r.log.snapshot()
	tally.totalCalls = len(calls)
	tally.callsPerTimeout = float64(len(calls)) / float64(len(r.nonces))
	tally.peakPerHost = peakPerHost(calls)
	tally.drainTime = r.clock.now().Sub(startedAt)
	return tally
}

func peakPerHost(calls []verifyCall) int {
	bySlot := map[int][]time.Time{}
	for _, call := range calls {
		bySlot[call.slot] = append(bySlot[call.slot], call.at)
	}
	peak := 0
	for _, times := range bySlot {
		sort.Slice(times, func(a, b int) bool { return times[a].Before(times[b]) })
		start := 0
		for end := range times {
			for times[end].Sub(times[start]) >= loadWindow {
				start++
			}
			peak = max(peak, end-start+1)
		}
	}
	return peak
}

func distinctNonces(calls []verifyCall) int {
	seen := map[uint64]bool{}
	for _, call := range calls {
		seen[call.nonce] = true
	}
	return len(seen)
}

// Test flow:
//  1. Build an eight-host session and one probe timeout on a shadow executor, once with every verifier rejecting and no receipt anywhere, once with one verifier also returning the host's receipt.
//  2. Settle the nonce through the probe's voter.
//  3. Assert the nonce's round asked a verifier and report the VerifyTimeout calls the round made.
func TestShadowTimeoutVerifyCallsPerRound(t *testing.T) {
	for _, shape := range []struct {
		name        string
		withReceipt bool
	}{{"no_receipt", false}, {"with_receipt", true}} {
		round := newLoadRound(t, loadShape{withReceipt: shape.withReceipt}, 1)
		tally := round.settleAll(t)
		require.Equal(t, 1, distinctNonces(round.log.snapshot()), "the nonce's round asked at least one verifier")
		t.Logf("MEASURE round=%s calls=%d verifiers=%d", shape.name, tally.totalCalls, loadSlots-1)
	}
}

// Test flow:
//  1. Build an eight-host session with K probe timeouts on one shadow executor, every verifier rejecting, for K of 50 and 500.
//  2. Settle the K nonces one after another through the probe's voter on a virtual clock the voter's pacing advances.
//  3. Assert every nonce asked at least one verifier, and the peak calls per verifier in any one second stays within the pacer's burst plus one second of its rate.
//  4. Report the peak and the calls per timeout.
func TestShadowBurstPeakVerifyRate(t *testing.T) {
	for _, bursts := range []int{loadSmallBurst, loadLargeBurst} {
		round := newLoadRound(t, loadShape{}, bursts)
		tally := round.settleAll(t)
		require.Equal(t, bursts, distinctNonces(round.log.snapshot()), "every nonce's round reached a verifier")
		require.LessOrEqual(t, tally.peakPerHost, warmup.VoteRoundBurst+int(warmup.VoteRoundsPerSecond))
		t.Logf("MEASURE burst=%d calls=%d calls_per_timeout=%.2f peak_per_verifier_per_second=%d drain_seconds=%.0f", bursts, tally.totalCalls, tally.callsPerTimeout, tally.peakPerHost, tally.drainTime.Seconds())
	}
}

// Test flow:
//  1. Build an eight-host session with fifty probe timeouts on one shadow executor, every verifier accepting.
//  2. Settle the nonces through the probe's voter.
//  3. Assert the session's state counts one miss on the shadow slot per nonce, so every vote reached the threshold and was applied.
func TestShadowBurstVotesApply(t *testing.T) {
	round := newLoadRound(t, loadShape{accepts: true}, loadSmallBurst)
	tally := round.settleAll(t)
	require.Equal(t, uint32(loadSmallBurst), tally.missed, "every probe timeout is applied as a miss on the shadow host")
	t.Logf("MEASURE burst=%d missed=%d", loadSmallBurst, tally.missed)
}
