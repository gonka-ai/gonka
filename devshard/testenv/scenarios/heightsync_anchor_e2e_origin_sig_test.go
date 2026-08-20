package scenarios

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/signing"
	"devshard/transport"
)

// TestHeightSyncAnchor_E2E_ResponseOriginSignatureVerified covers E9 (Step 8).
func TestHeightSyncAnchor_E2E_ResponseOriginSignatureVerified(t *testing.T) {
	ensureHeightSyncPromMetrics(t)
	ctx := context.Background()
	const h int64 = 11
	hash := []byte{0xca, 0xfe, 0xba, 0xbe}

	hostOracles := []*staticOracle{
		staticOracleWith(h, hash),
		staticOracleWith(h, hash),
		staticOracleWith(h, hash),
		staticOracleWith(h, hash),
	}
	st, peerTips := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
	params := defaultInferenceParams()
	verifier := signing.NewSecp256k1Verifier()

	beforeInvalid := heightsync.OriginSigInvalidTotal()

	for n := uint64(1); n <= 4; n++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "sync turn nonce=%d", n)
	}
	syncHostsFromSession(t, st)

	require.Equal(t, beforeInvalid, heightsync.OriginSigInvalidTotal(),
		"honest signed responses must not increment origin_sig_invalid")

	for i, origin := range st.HostAddrs {
		sec, blob, sig, ok := peerTips.VerifiedAnchorFor(origin, h)
		require.True(t, ok, "host %d must have verified blob at H=%d", i, h)
		require.NotEmpty(t, blob)
		require.NotEmpty(t, sig)
		require.NoError(t, heightsync.VerifyOriginDetached(verifier, sec, blob, sig))
	}

	// User cache MaxFresh must come from verified entries only.
	tip := peerTips.MaxFresh(time.Now(), peerTips.Freshness)
	require.NotNil(t, tip)
	require.GreaterOrEqual(t, tip.MainnetHeight, h)

	// Session HTTP client exposes evidence API (courier).
	var evidenceClient *transport.HTTPClient
	for _, cl := range st.Session.Clients() {
		hc, ok := cl.(*transport.HTTPClient)
		if !ok {
			continue
		}
		if _, _, ok := hc.HeightSyncEvidenceFor(st.HostAddrs[0], h); ok {
			evidenceClient = hc
			break
		}
	}
	require.NotNil(t, evidenceClient)
	_, _, ok := evidenceClient.HeightSyncEvidenceFor(st.HostAddrs[0], h)
	require.True(t, ok)
}

// TestHeightSyncAnchor_E2E_CarrierExculpation covers E10 (Step 8) at the evidence layer.
func TestHeightSyncAnchor_E2E_CarrierExculpation(t *testing.T) {
	ensureHeightSyncPromMetrics(t)
	ctx := context.Background()
	const h int64 = 11
	hashA := []byte{0xaa, 0xbb}

	hostOracles := []*staticOracle{
		staticOracleWith(h, hashA),
		staticOracleWith(h, []byte{0xcc, 0xdd}),
		staticOracleWith(h, []byte{0xcc, 0xdd}),
		staticOracleWith(h, []byte{0xcc, 0xdd}),
	}
	st, peerTips := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
	params := defaultInferenceParams()
	hostA := st.HostAddrs[0]

	for n := uint64(1); n <= 4; n++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "sync turn nonce=%d", n)
	}
	syncHostsFromSession(t, st)

	blob, sig, ok := peerTips.OriginSignedBlobFor(hostA, h)
	require.True(t, ok, "host A signed response must be cached")

	sec, _, _, ok := peerTips.VerifiedAnchorFor(hostA, h)
	require.True(t, ok)
	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, heightsync.VerifyOriginDetached(verifier, sec, blob, sig),
		"DISPUTE_ORIGINATOR: carrier can exculpate with stored signed_blob")

	var hc *transport.HTTPClient
	for _, cl := range st.Session.Clients() {
		c, ok := cl.(*transport.HTTPClient)
		if ok {
			hc = c
			break
		}
	}
	require.NotNil(t, hc)
	gotBlob, gotSig, got := hc.HeightSyncEvidenceFor(hostA, h)
	require.True(t, got)
	require.Equal(t, blob, gotBlob)
	require.Equal(t, sig, gotSig)

	// Cache loss ⇒ DISPUTE_CARRIER (carrier cannot produce blob).
	emptyTips := transport.NewHeightSyncPeerTips()
	_, _, lost := emptyTips.OriginSignedBlobFor(hostA, h)
	require.False(t, lost)
}

// TestHeightSyncAnchor_E2E_ResponseOriginSignatureInvalidDropped covers E9 variant B (Step 8).
func TestHeightSyncAnchor_E2E_ResponseOriginSignatureInvalidDropped(t *testing.T) {
	ensureHeightSyncPromMetrics(t)
	ctx := context.Background()
	const h int64 = 11
	hash := []byte{0xde, 0xad}

	hostOracles := []*staticOracle{
		staticOracleWith(h, hash),
		staticOracleWith(h, hash),
		staticOracleWith(h, hash),
		staticOracleWith(h, hash),
	}
	st, peerTips := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
	params := defaultInferenceParams()

	const badNonce uint64 = 1
	badHost := hostIdxForNonce(badNonce)
	origin := st.HostAddrs[badHost]
	st.Servers[badHost].SetHeightSyncResponseAfterSignHook(func(sec *heightsync.HeightSyncSection, nonce uint64) {
		if sec == nil || nonce != badNonce || len(sec.SenderSignature) == 0 {
			return
		}
		sec.SenderSignature[0] ^= 0xff
	})

	beforeInvalid := heightsync.OriginSigInvalidTotal()
	_, err := st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	require.Equal(t, beforeInvalid+1, heightsync.OriginSigInvalidTotal())
	_, _, _, ok := peerTips.VerifiedAnchorFor(origin, h)
	require.True(t, ok, "E9 seed still stores the host's verified tip; only the inference response is dropped")
	require.NotNil(t, peerTips.MaxFresh(time.Now(), peerTips.Freshness),
		"seed warms the cache even when the nonce-1 response signature is invalid")
}
