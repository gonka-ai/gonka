package scenarios

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/transport"
)

func sessionConfirmationView(t *testing.T, st *fourHostStack) heightsync.ConfirmationView {
	t.Helper()
	for _, cl := range st.Session.Clients() {
		hc, ok := cl.(*transport.HTTPClient)
		if !ok {
			continue
		}
		if v := hc.ConfirmationView(); v != nil {
			return v
		}
	}
	require.Fail(t, "session has no ConfirmationView")
	return nil
}

func defaultConfirmHostOracles(t *testing.T, height int64, hash []byte) []*staticOracle {
	t.Helper()
	return []*staticOracle{
		staticOracleWith(height, hash),
		staticOracleWith(height, hash),
		staticOracleWith(height, hash),
		staticOracleWith(height, hash),
	}
}

// TestHeightSyncAnchor_E2E_IsStrictlyConfirmed_Quorum covers spec §17 (C-quorum).
func TestHeightSyncAnchor_E2E_IsStrictlyConfirmed_Quorum(t *testing.T) {
	ensureHeightSyncPromMetrics(t)
	ctx := context.Background()
	const h int64 = 11
	hash := []byte{0x11, 0x22, 0x33, 0x44}
	st := setupFourHostHTTPHeightSyncWithOracles(t, defaultConfirmHostOracles(t, h, hash), staticOracleWith(h, hash))
	params := defaultInferenceParams()

	for n := uint64(1); n <= 4; n++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "sync turn nonce=%d", n)
	}
	syncHostsFromSession(t, st)

	cv := sessionConfirmationView(t, st)
	require.Equal(t, heightsync.ConfirmConfirmed, cv.IsStrictlyConfirmed(uint64(h)))

	require.NotNil(t, st.Confirm)
	st.Confirm.SetQuorum(5)
	require.Equal(t, heightsync.ConfirmConfirmed, cv.IsStrictlyConfirmed(uint64(h)),
		"monotonic: impossible quota must not un-confirm")

	st, shared := setupFourHostHTTPHeightSyncStoppingOracle(t)
	_ = st
	shared.SetStopped(true)
	cv2 := sessionConfirmationView(t, st)
	require.Equal(t, heightsync.ConfirmStale, cv2.IsStrictlyConfirmed(uint64(h)))
}

// TestHeightSyncAnchor_E2E_MixedHeights_Confirmed covers spec §17 mixed-height quorum.
func TestHeightSyncAnchor_E2E_MixedHeights_Confirmed(t *testing.T) {
	ensureHeightSyncPromMetrics(t)
	ctx := context.Background()
	const h int64 = 11
	goodHash := []byte{0xaa, 0xaa}
	badHash := []byte{0xbb, 0xbb}

	hostOracles := defaultConfirmHostOracles(t, h, goodHash)
	hostOracles[0] = staticOracleWith(h, badHash)

	st := setupFourHostHTTPHeightSyncWithOracles(t, hostOracles, staticOracleWith(h, goodHash))
	params := defaultInferenceParams()
	cv := sessionConfirmationView(t, st)

	require.Equal(t, heightsync.ConfirmPending, cv.IsStrictlyConfirmed(uint64(h)))

	for n := uint64(1); n <= 4; n++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "nonce=%d", n)
	}
	syncHostsFromSession(t, st)
	require.Equal(t, heightsync.ConfirmConfirmed, cv.IsStrictlyConfirmed(uint64(h)))

	_, err := st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)
	require.Equal(t, heightsync.ConfirmConfirmed, cv.IsStrictlyConfirmed(uint64(h)),
		"one dishonest host must not un-confirm quorum height")
}

// TestHeightSyncAnchor_E2E_StaleOracle_Inconclusive covers spec §17 when the oracle is stale.
func TestHeightSyncAnchor_E2E_StaleOracle_Inconclusive(t *testing.T) {
	ensureHeightSyncPromMetrics(t)
	ctx := context.Background()
	st, shared := setupFourHostHTTPHeightSyncStoppingOracle(t)
	params := defaultInferenceParams()

	_, err := st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	shared.SetStopped(true)
	cv := sessionConfirmationView(t, st)
	require.Equal(t, heightsync.ConfirmStale, cv.IsStrictlyConfirmed(100))
	require.Equal(t, verdictInconclusiveFromConfirm(cv.IsStrictlyConfirmed(100)), true)
}

// verdictInconclusiveFromConfirm mirrors cPoC C6 gating for PoC tests.
func verdictInconclusiveFromConfirm(s heightsync.ConfirmState) bool {
	return s == heightsync.ConfirmPending || s == heightsync.ConfirmStale
}

// TestHeightSyncAnchor_E2E_LateOracleHost_ConfirmedViaCourier covers E11 (§3.6 worked example).
//
// Production-faithful design notes:
//   - HeightSyncPeerTips.ShouldPropagateTo(recipient, h) is height-keyed
//     (h > last_propagated[recipient]) — see proposal §"Per-peer last-propagated
//     tracking" — so each (H, hash) pair is lazy-carried to a peer at most once.
//   - MaxFresh() returns a single highest-height section, so each lazy request
//     attaches exactly one originator.
//   - Reaching Q ≥ 3 distinct originators on host A therefore requires three
//     carries with strictly increasing heights (the **height ladder**). This
//     matches what happens in production when peer followers are at slightly
//     different mainnet heights and their tips arrive at the courier over time.
func TestHeightSyncAnchor_E2E_LateOracleHost_ConfirmedViaCourier(t *testing.T) {
	ensureHeightSyncPromMetrics(t)
	ctx := context.Background()
	const hOld, hNew int64 = 10, 11
	goodHash := []byte{0xcc, 0xdd}

	t.Run("UserCacheConfirmed", func(t *testing.T) {
		// Phase 1 of §3.6 worked example: B/C/D respond at H_new and the
		// user-side confirmation index reaches Confirmed via response-leg
		// attestations alone (no carry-forward to host A yet).
		hostOracles := []*staticOracle{
			staticOracleWith(hOld, goodHash),
			staticOracleWith(hNew, goodHash),
			staticOracleWith(hNew, goodHash),
			staticOracleWith(hNew, goodHash),
		}
		st, _ := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
		params := defaultInferenceParams()
		for n := uint64(1); n <= 3; n++ {
			_, err := st.Session.SendInference(ctx, params)
			require.NoError(t, err, "warm B/C/D nonce=%d", n)
		}
		syncHostsFromSession(t, st)
		cv := sessionConfirmationView(t, st)
		require.Equal(t, heightsync.ConfirmConfirmed, cv.IsStrictlyConfirmed(uint64(hNew)))
	})

	t.Run("HostAPendingBeforePropagate", func(t *testing.T) {
		// Phase 2 of §3.6 worked example: host A's index is empty for B/C/D
		// before any inference targets A.
		hostOracles := []*staticOracle{
			staticOracleWith(hOld, goodHash),
			staticOracleWith(hNew, goodHash),
			staticOracleWith(hNew, goodHash),
			staticOracleWith(hNew, goodHash),
		}
		st, _ := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
		const hostAIdx = 0
		cvA := st.Servers[hostAIdx].ConfirmationView()
		require.NotNil(t, cvA)
		require.Equal(t, heightsync.ConfirmPending, cvA.IsStrictlyConfirmed(uint64(hNew)))
		require.Equal(t, hOld, hostOracles[hostAIdx].hdr.Height)
	})

	t.Run("HostAConfirmedAfterLazy", func(t *testing.T) {
		// Phase 3 of §3.6 worked example, production-faithful: a height
		// ladder drives three lazy carries to host A with strictly
		// increasing mainnet heights so each pass satisfies
		// ShouldPropagateTo(hostAURL, h). No test-only propagation reset,
		// no Decide-time mutate hook on the request leg.
		//
		// Hosts B/C/D have oracles at hOld so their response Anchors
		// (B@10, C@10, D@10) stay below the ladder; the user-cache state
		// at each lazy emit is determined by the ladder seeds, not by
		// races between SSE response ingest and round-robin order.
		hostOracles := []*staticOracle{
			staticOracleWith(hOld, goodHash), // A @ hOld
			staticOracleWith(hOld, goodHash), // B @ hOld (real responses do not push the cache)
			staticOracleWith(hOld, goodHash), // C @ hOld
			staticOracleWith(hOld, goodHash), // D @ hOld
		}
		st, peerTips := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
		params := defaultInferenceParams()
		const hostAIdx = 0
		hashHex := hex.EncodeToString(goodHash)
		nowMs := time.Now().UnixMilli()

		// Each ladder step models a single host's fresh tip landing in
		// the courier cache (ingest stores a verified blob; tests seed
		// RecordOriginWithBlob so MaxFresh accepts the entry). Heights
		// are strictly increasing so MaxFresh selects this step's
		// originator and ShouldPropagateTo holds.
		ladder := []struct {
			addr string
			h    int64
		}{
			{st.HostAddrs[1], hNew},     // B @ 11
			{st.HostAddrs[2], hNew + 1}, // C @ 12
			{st.HostAddrs[3], hNew + 2}, // D @ 13
		}
		for _, step := range ladder {
			recordCourierPeerTip(peerTips, &heightsync.HeightSyncSection{
				ChainID:               "gonka-testenv-1",
				ProofType:             heightsync.AnchorProofType,
				MainnetHeight:         step.h,
				MainnetBlockHashHex:   hashHex,
				OriginatorSenderID:    step.addr,
				OriginatorTimestampMs: nowMs,
			})
			// Round-robin requests until the next nonce lands on host A.
			// Non-A hosts respond at hOld; those responses cannot regress
			// the ladder (RecordOrigin only replaces on h ≥ existing).
			for {
				p, err := st.Session.PrepareInference(params)
				require.NoError(t, err)
				resp, err := st.Session.SendOnly(ctx, p, nil, nil)
				require.NoError(t, err)
				require.NoError(t, st.Session.ProcessResponse(p.HostIdx(), resp, p.Nonce()))
				if p.HostIdx() == hostAIdx {
					break
				}
			}
		}
		syncHostsFromSession(t, st)

		cvA := st.Servers[hostAIdx].ConfirmationView()
		require.Equal(t, heightsync.ConfirmConfirmed, cvA.IsStrictlyConfirmed(uint64(hNew)))
		require.Equal(t, hOld, hostOracles[hostAIdx].hdr.Height,
			"confirmation must not advance local follower tip")

		seen := make(map[string]struct{})
		for _, a := range st.Servers[hostAIdx].HeightSyncAuditRing().List(st.UserAddr) {
			if a.Direction == "request" && a.MainnetHeight >= hNew {
				seen[a.OriginatorSenderID] = struct{}{}
			}
		}
		require.GreaterOrEqual(t, len(seen), 3,
			"courier must deliver ≥3 distinct originators (height ladder) to host A")
	})
}

// TestHeightSyncAnchor_E2E_LateOracleHost_StillPendingWithoutPropagate is E11 negative.
func TestHeightSyncAnchor_E2E_LateOracleHost_StillPendingWithoutPropagate(t *testing.T) {
	ensureHeightSyncPromMetrics(t)
	ctx := context.Background()
	const hOld, hNew int64 = 10, 11
	goodHash := []byte{0xee, 0xff}

	hostOracles := []*staticOracle{
		staticOracleWith(hOld, goodHash),
		staticOracleWith(hNew, goodHash),
		staticOracleWith(hNew, goodHash),
		staticOracleWith(hNew, goodHash),
	}
	st, _ := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
	params := defaultInferenceParams()

	for n := uint64(2); n <= 4; n++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "nonce=%d", n)
	}
	syncHostsFromSession(t, st)

	cvA := st.Servers[0].ConfirmationView()
	require.Equal(t, heightsync.ConfirmPending, cvA.IsStrictlyConfirmed(uint64(hNew)),
		"host A must stay pending until courier propagates lazy anchors")
}
