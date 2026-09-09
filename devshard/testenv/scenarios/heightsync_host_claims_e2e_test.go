package scenarios

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/heightsync"
	"devshard/transport"
	"devshard/types"
)

const hostClaimsReconcileWarn = "heightsync: untrusted peer tip disagrees with oracle at reconciled height"

// advancingOracle is a history-backed tip that tests can AdvanceTo. Latest()
// is the current header; At() still returns earlier heights so L6 / reconcile
// can see the canonical hash once the follower reaches a claimed height.
type advancingOracle struct {
	mu     sync.Mutex
	latest int64
	byH    map[int64]*blocks.Header
}

func newAdvancingOracle(height int64, hash []byte) *advancingOracle {
	o := &advancingOracle{byH: make(map[int64]*blocks.Header)}
	o.setLocked(height, hash)
	return o
}

func (o *advancingOracle) AdvanceTo(height int64, hash []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.setLocked(height, hash)
}

func (o *advancingOracle) setLocked(height int64, hash []byte) {
	hdr := &blocks.Header{
		Height:    height,
		ChainID:   "gonka-testenv-1",
		BlockHash: append([]byte(nil), hash...),
	}
	o.byH[height] = hdr
	o.latest = height
}

func (o *advancingOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	_ = ctx
	o.mu.Lock()
	defer o.mu.Unlock()
	return cloneHeader(o.byH[o.latest]), nil
}

func (o *advancingOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	_ = ctx
	o.mu.Lock()
	defer o.mu.Unlock()
	h := o.byH[height]
	if h == nil {
		return nil, errNoHeaderAtHeight
	}
	return cloneHeader(h), nil
}

var errNoHeaderAtHeight = errors.New("no header at height")

func (o *advancingOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *advancingOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func cloneHeader(h *blocks.Header) *blocks.Header {
	if h == nil {
		return nil
	}
	out := *h
	out.BlockHash = append([]byte(nil), h.BlockHash...)
	return &out
}

func setupFourHostHTTPHeightSyncCourierLogged(t *testing.T, hostOracles []*staticOracle, logOracle blocks.BlockOracle) (*fourHostStack, *transport.HeightSyncPeerTips) {
	t.Helper()
	return setupFourHostHTTPHeightSyncCourier(t, hostOracles, func(cc *transport.ClientConfig) {
		cc.HeightSyncLogOracle = logOracle
	})
}

func sendHostClaimInferences(t *testing.T, st *fourHostStack, n int) {
	t.Helper()
	ctx := context.Background()
	params := defaultInferenceParams()
	for i := 1; i <= n; i++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoErrorf(t, err, "nonce=%d served by slot %d must not fail", i, hostIdxForNonce(uint64(i)))
		syncHostsFromSession(t, st)
	}
}

func logHasNegativeDeltaForHeight(entries []testLogEntry, height int64) bool {
	want := strconv.FormatInt(height, 10)
	for _, e := range entries {
		if e.msg != "heightsync: peer attestation received" {
			continue
		}
		if e.kv["peer_height"] != want && e.kv["height"] != want {
			continue
		}
		d, err := strconv.ParseInt(e.kv["delta"], 10, 64)
		if err == nil && d < 0 {
			return true
		}
	}
	return false
}

func logHasTrustAndDeltaAbove(entries []testLogEntry, trust string, minAbsDelta int64) bool {
	for _, e := range entries {
		if e.msg != "heightsync: peer attestation received" {
			continue
		}
		if e.kv["trust_level"] != trust {
			continue
		}
		d, err := strconv.ParseInt(e.kv["delta"], 10, 64)
		if err == nil && d > minAbsDelta {
			return true
		}
	}
	return false
}

func logHasMsg(entries []testLogEntry, msg string) bool {
	for _, e := range entries {
		if e.msg == msg {
			return true
		}
	}
	return false
}

func requireNoDisputeMarks(t *testing.T, st *fourHostStack) {
	t.Helper()
	for i, srv := range st.Servers {
		ml := srv.HeightSyncMarks()
		if ml == nil {
			continue
		}
		require.Falsef(t, ml.HasKind(heightsync.MarkDisputeOriginator),
			"host %d must not be named originator of a dispute it did not cause", i)
		require.Falsef(t, ml.HasKind(heightsync.MarkDisputeCarrier),
			"host %d must not see a carrier contradiction on this exchange", i)
	}
}

func responseAuditHeights(st *fourHostStack) map[int64]bool {
	seen := make(map[int64]bool)
	for i, srv := range st.Servers {
		ar := srv.HeightSyncAuditRing()
		if ar == nil {
			continue
		}
		for _, a := range ar.List(st.HostAddrs[i]) {
			if a.Direction == "response" {
				seen[a.MainnetHeight] = true
			}
		}
	}
	return seen
}

func inboundTrustSeen(st *fourHostStack, trust heightsync.AttestationTrust, height int64) bool {
	for _, srv := range st.Servers {
		ar := srv.HeightSyncAuditRing()
		if ar == nil {
			continue
		}
		for _, a := range ar.List(st.UserAddr) {
			if a.Direction == "request" && a.Trust == trust && a.MainnetHeight == height {
				return true
			}
		}
	}
	return false
}

// TestHeightSync_E2E_HostLowerHeightAutoAlignsAndLogs is scenario A: one host's
// oracle sits far below the roster. F is the higher host-signed tip; the
// lagging host lifts in the log (CATCHING_UP) and chat does not stall. The
// low tip stays visible on the response-leg Anchor. Do not mutate height after
// origin sign — that invalidates the signature; honest lag is a low Latest().
func TestHeightSync_E2E_HostLowerHeightAutoAlignsAndLogs(t *testing.T) {
	logs := installCaptureLogger(t)
	const high, low int64 = 1000, 5
	hash := func(i int, h int64) []byte { return []byte{0xd0, byte(i), byte(h)} }
	hostOracles := []*staticOracle{
		staticOracleWith(high, hash(0, high)),
		staticOracleWith(high, hash(1, high)),
		staticOracleWith(high, hash(2, high)),
		staticOracleWith(low, hash(3, low)),
	}
	st, _ := setupFourHostHTTPHeightSyncCourierLogged(t, hostOracles, staticOracleWith(high, hash(0, high)))
	seedHTTPSession(t, st.Session)

	const nonces = 8
	sendHostClaimInferences(t, st, nonces)

	sm := st.Session.StateMachine()
	require.Equal(t, uint64(nonces), sm.LatestNonce())
	seen := responseAuditHeights(st)
	require.True(t, seen[high] && seen[low],
		"both ends of the spread must reach the audit ring: high=%v low=%v seen=%v", high, low, seen)
	require.True(t, logHasNegativeDeltaForHeight(logs.snapshot(), low),
		"operators must see a negative delta naming the lagging peer height %d", low)

	require.Equal(t, 3, hostIdxForNonce(3))
	floor, _, known := sm.HeightSyncFloorAsOf(4)
	require.True(t, known)
	require.Equal(t, uint64(high), floor, "a host-signed high tip seeds F; the low host does not rewind it")
	recs := sm.SnapshotState().Inferences
	require.Equal(t, floor, recs[3].ConfirmedAtHeight,
		"the lagging slot carries F in the log rather than understating it")

	requireNoDisputeMarks(t, st)
	require.Equal(t, types.PhaseActive, sm.SnapshotState().Phase)
}

// TestHeightSync_E2E_HostFutureHeightBeyondDDetected is scenario B: one host
// claims H+Δ with Δ > D (default 2) and a hash the honest oracles do not
// know. Envelope trust is untrusted_peer; chat still serves. Strong slash is
// not required in this release. L5a MARK(l5a_admission) is the heartbeat/ack
// admission check (unit + citest); this inference path pins the envelope.
func TestHeightSync_E2E_HostFutureHeightBeyondDDetected(t *testing.T) {
	logs := installCaptureLogger(t)
	const local, future int64 = 100, 110 // |Δ| = 10 > D = 2
	canon := []byte{0xab, 0xcd, 0xef, 0x42}
	fake := append([]byte(nil), canon...)
	fake[0] ^= 0xff
	honest := staticOracleWith(local, canon)
	hostOracles := []*staticOracle{
		staticOracleWith(local, canon),
		staticOracleWith(local, canon),
		staticOracleWith(local, canon),
		staticOracleWith(future, fake),
	}
	st, _ := setupFourHostHTTPHeightSyncCourierLogged(t, hostOracles, honest)
	seedHTTPSession(t, st.Session)
	sendHostClaimInferences(t, st, 8)

	require.Equal(t, uint64(8), st.Session.StateMachine().LatestNonce())
	require.True(t, inboundTrustSeen(st, heightsync.TrustUntrustedPeer, future),
		"an honest host that received the future carry must record untrusted_peer at height %d", future)
	require.True(t, logHasTrustAndDeltaAbove(logs.snapshot(), string(heightsync.TrustUntrustedPeer), int64(heightsync.DefaultSyncDeltaBlocks)),
		"operators must see trust_level=untrusted_peer with delta > D=%d", heightsync.DefaultSyncDeltaBlocks)
	require.Equal(t, types.PhaseActive, st.Session.StateMachine().SnapshotState().Phase)
}

// TestHeightSync_E2E_HostFabricatedHashInsideDReconciles is scenario C: one
// host claims H+1 (inside D) with a fabricated hash. Honest followers hold
// that tip as untrusted_peer. When their oracle later reaches H+1 and sees
// the canonical hash, they warn. Chat never blocks. Mutating height after
// origin sign is not used — the liar's Latest() is the fabricated pair.
func TestHeightSync_E2E_HostFabricatedHashInsideDReconciles(t *testing.T) {
	logs := installCaptureLogger(t)
	const local, claimed int64 = 100, 101
	canon := []byte{0xab, 0xcd, 0xef, 0x42}
	canonNext := []byte{0x11, 0x22, 0x33, 0x44}
	fake := append([]byte(nil), canonNext...)
	fake[0] ^= 0xff
	honest := newAdvancingOracle(local, canon)
	liar := staticOracleWith(claimed, fake)
	hsched := []blocks.BlockOracle{honest, honest, honest, liar}
	dummy := staticOracleWith(1, []byte{0x01})
	peerTips := transport.NewHeightSyncPeerTips()
	src := heightsync.NewPeerTipOracleSource(peerTips, peerTips.Freshness)
	sched := heightsync.MustNewAnchorScheduler(8, 4, src)
	st := setupFourHostHTTPHeightSyncFromChainOracles(t, hsched, hsched, dummy, honest, nil, func(cc *transport.ClientConfig) {
		cc.HeightSync = sched
		cc.HeightSyncPeerTips = peerTips
		cc.HeightSyncLogOracle = honest
	})
	seedHTTPSession(t, st.Session)

	// Nonce 5 (host 1) lazy-carries the liar's 101 tip while honest is still 100.
	sendHostClaimInferences(t, st, 5)
	require.False(t, logHasMsg(logs.snapshot(), hostClaimsReconcileWarn),
		"must not warn before the honest oracle reaches the claimed height")

	honest.AdvanceTo(claimed, canonNext)
	// Nonce 9 → host 1, the slot that held the pending untrusted tip.
	sendHostClaimInferences(t, st, 4)
	require.Equal(t, 1, hostIdxForNonce(9))
	require.True(t, logHasMsg(logs.snapshot(), hostClaimsReconcileWarn),
		"when local Latest() reaches the held height, hash mismatch must warn")
	require.Equal(t, uint64(9), st.Session.StateMachine().LatestNonce())
	require.Equal(t, types.PhaseActive, st.Session.StateMachine().SnapshotState().Phase)
}
