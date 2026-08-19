package scenarios

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

const courierBootstrapHeight = int64(11)

// TestHeightSyncAnchor_E2E_CourierBootstrap covers plan §5/E1 after E9: the
// session-open seed warms the courier cache from the roster, so nonce 1
// already carries an Anchor. Host A (higher tip) remains MaxFresh originator
// and later nonces carry that originator.
func TestHeightSyncAnchor_E2E_CourierBootstrap(t *testing.T) {
	ctx := context.Background()
	logs := installCaptureLogger(t)

	hashA := []byte{0x11, 0x22, 0x33, 0x44}
	hashBase := []byte{0xab, 0xcd, 0xef, 0x42}
	// Host A must own MaxFresh after nonce 1 (higher than peers at 10).
	hostOracles := []*staticOracle{
		staticOracleWith(10, hashBase),
		staticOracleWith(courierBootstrapHeight, hashA),
		staticOracleWith(10, hashBase),
		staticOracleWith(10, hashBase),
	}
	st, peerTips := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
	params := defaultInferenceParams()

	hostAIdx := hostIdxForNonce(1)
	hostA := st.HostAddrs[hostAIdx]
	require.Nil(t, peerTips.MaxFresh(time.Now(), peerTips.Freshness), "courier cache must start cold")

	_, err := st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	entries := logs.snapshot()
	require.Equal(t, "anchor", requestEmitModeAtNonce(entries, 1),
		"nonce 1 outbound Anchors from the E9 seed, not from the nonce-1 response")

	tip := peerTips.MaxFresh(time.Now(), peerTips.Freshness)
	require.NotNil(t, tip)
	require.Equal(t, courierBootstrapHeight, tip.MainnetHeight)
	require.Equal(t, hostA, tip.OriginatorSenderID)

	ar := st.Servers[hostAIdx].HeightSyncAuditRing()
	require.NotNil(t, ar)
	var sawHostAResponse bool
	for _, a := range ar.List(st.HostAddrs[hostAIdx]) {
		if a.Direction == "response" && a.MainnetHeight == courierBootstrapHeight &&
			bytes.Equal(a.MainnetBlockHash, hashA) {
			sawHostAResponse = true
			break
		}
	}
	require.True(t, sawHostAResponse, "host A must Anchor its oracle tip on the nonce=1 response")

	for _, n := range []uint64{2, 3, 4} {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "sync turn nonce=%d", n)
		syncHostsFromSession(t, st)
	}

	entries = logs.snapshot()
	require.Equal(t, "anchor", requestEmitModeAtNonce(entries, 2),
		"nonce 2 must carry Anchor from warmed cache")
	require.NotEqual(t, st.UserAddr, tip.OriginatorSenderID)

	for _, n := range []uint64{2, 3, 4} {
		hostIdx := hostIdxForNonce(n)
		requireInboundUserAnchorOriginator(t, st.Servers[hostIdx], st.UserAddr, hostA,
			courierBootstrapHeight, hashA, heightsync.TagCadence)
	}
}

// TestHeightSyncAnchor_E2E_PipelinedCourier covers plan §5/E7 after E9: all four
// sync-turn nonces in flight before responses. The session-open seed warms the
// cache, so the wave Anchors; the next sync turn still carries host originators.
func TestHeightSyncAnchor_E2E_PipelinedCourier(t *testing.T) {
	ctx := context.Background()
	logs := installCaptureLogger(t)

	hashBase := []byte{0xde, 0xad, 0xbe, 0xef}
	hostOracles := []*staticOracle{
		staticOracleWith(100, hashBase),
		staticOracleWith(100, hashBase),
		staticOracleWith(100, hashBase),
		staticOracleWith(100, hashBase),
	}
	st, peerTips := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
	params := defaultInferenceParams()

	require.Nil(t, peerTips.MaxFresh(time.Now(), peerTips.Freshness))

	courierPipelinedSyncTurn(t, ctx, st, params, 1, 4)

	entries := logs.snapshot()
	for n := 1; n <= 4; n++ {
		require.Equal(t, "anchor", requestEmitModeAtNonce(entries, n),
			"pipelined sync-turn nonce=%d Anchors from the E9 seed", n)
	}
	require.NotNil(t, peerTips.MaxFresh(time.Now(), peerTips.Freshness),
		"responses must warm peer-tip cache after the pipelined wave")

	// Omit-window nonces 5–7 before periodic sync turn 8–11.
	for n := 5; n <= 7; n++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "omit window nonce=%d", n)
		syncHostsFromSession(t, st)
	}
	for n := 8; n <= 11; n++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "second sync turn nonce=%d", n)
		syncHostsFromSession(t, st)
	}

	entries = logs.snapshot()
	for n := 8; n <= 11; n++ {
		require.Equal(t, "anchor", requestEmitModeAtNonce(entries, n),
			"second sync turn nonce=%d must carry full Anchor", n)
	}

	// At least one receiving host must record a cadence inbound with a host originator.
	var sawCarriedOrigin bool
	for hostIdx, srv := range st.Servers {
		ar := srv.HeightSyncAuditRing()
		if ar == nil {
			continue
		}
		for _, a := range ar.List(st.UserAddr) {
			if a.Direction != "request" || a.Tag != heightsync.TagCadence {
				continue
			}
			if a.OriginatorSenderID == "" || a.OriginatorSenderID == st.UserAddr {
				continue
			}
			if a.OriginatorSenderID == st.HostAddrs[hostIdx] {
				continue // host-oracle path, not carry-forward
			}
			sawCarriedOrigin = true
			break
		}
		if sawCarriedOrigin {
			break
		}
	}
	require.True(t, sawCarriedOrigin,
		"second sync turn must deliver carry-forward with host originator metadata")
}
