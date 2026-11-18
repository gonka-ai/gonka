package shadow_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/engine"
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
)

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
	round := loadRound{log: &verifyLog{}, clock: newVirtualClock()}
	clients := make([]user.HostClient, loadSlots)
	for index := range signers {
		machine := statetest.MustStateMachine(t, loadEscrow, config, group, loadBalance, creator.Address(), verifier)
		executor, err := host.NewHost(machine, signers[index], stub.NewInferenceEngine(), loadEscrow, group, nil, host.WithGrace(loadGrace))
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
	session, err := user.NewSession(machine, creator, loadEscrow, group, clients, verifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	round.session = session
	for len(round.nonces) < timeouts {
		prepared, err := session.PrepareInference(context.Background(), user.InferenceParams{
			Model: loadModel, Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: loadStartedAt,
		})
		require.NoError(t, err)
		if int(prepared.Nonce()%loadSlots) == loadShadowSlot {
			round.nonces = append(round.nonces, prepared.Nonce())
		}
	}
	return round
}

type voteTally struct {
	votedNonces     int
	applied         int
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
	voter := newTreeVoter(posters, r.clock)
	params := user.InferenceParams{Model: loadModel, Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: loadStartedAt}
	var tally voteTally
	startedAt := r.clock.now()
	for _, nonce := range r.nonces {
		posted, voted := voter.Settle(context.Background(), loadEscrow, loadModel, params, nonce, false)
		if voted {
			tally.votedNonces++
		}
		if posted.Action == engine.TimeoutActionCompleted {
			tally.applied++
		}
	}
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
//  3. Assert the nonce was voted and report the VerifyTimeout calls the round made.
func TestShadowTimeoutVerifyCallsPerRound(t *testing.T) {
	for _, shape := range []struct {
		name        string
		withReceipt bool
	}{{"no_receipt", false}, {"with_receipt", true}} {
		round := newLoadRound(t, loadShape{withReceipt: shape.withReceipt}, 1)
		tally := round.settleAll(t)
		require.Equal(t, 1, tally.votedNonces)
		require.Equal(t, 1, distinctNonces(round.log.snapshot()), "the nonce's round asked at least one verifier")
		t.Logf("MEASURE %s round=%s calls=%d verifiers=%d", treeName, shape.name, tally.totalCalls, loadSlots-1)
	}
}

// Test flow:
//  1. Build an eight-host session with K probe timeouts on one shadow executor, every verifier rejecting, for K of 50 and 500.
//  2. Settle the K nonces one after another through the probe's voter on a virtual clock the voter's pacing advances.
//  3. Assert every nonce was voted and asked at least one verifier, and the peak calls per verifier in any one second stays within the tree's bound.
//  4. Report the peak and the calls per timeout.
func TestShadowBurstPeakVerifyRate(t *testing.T) {
	for _, bursts := range []int{loadSmallBurst, loadLargeBurst} {
		round := newLoadRound(t, loadShape{}, bursts)
		tally := round.settleAll(t)
		require.Equal(t, bursts, tally.votedNonces, "every probe timeout ends in a vote round")
		require.Equal(t, bursts, distinctNonces(round.log.snapshot()), "every nonce's round reached a verifier")
		require.LessOrEqual(t, tally.peakPerHost, peakBound(bursts))
		t.Logf("MEASURE %s burst=%d calls=%d calls_per_timeout=%.2f peak_per_verifier_per_second=%d drain_seconds=%.0f", treeName, bursts, tally.totalCalls, tally.callsPerTimeout, tally.peakPerHost, tally.drainTime.Seconds())
	}
}

// Test flow:
//  1. Build an eight-host session with fifty probe timeouts on one shadow executor, every verifier accepting.
//  2. Settle the nonces through the probe's voter.
//  3. Assert every vote reached the threshold and was applied, so the miss landed on the shadow host for each nonce.
func TestShadowBurstVotesApply(t *testing.T) {
	round := newLoadRound(t, loadShape{accepts: true}, loadSmallBurst)
	tally := round.settleAll(t)
	require.Equal(t, loadSmallBurst, tally.votedNonces)
	require.Equal(t, loadSmallBurst, tally.applied, fmt.Sprintf("applied votes = %d of %d", tally.applied, loadSmallBurst))
	t.Logf("MEASURE %s burst=%d applied=%d", treeName, loadSmallBurst, tally.applied)
}
