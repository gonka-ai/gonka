package heightsync_test

import (
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/heightsync"

	"github.com/stretchr/testify/require"
)

func TestNonceInSyncTurn_K8Slots4(t *testing.T) {
	for _, n := range []uint64{1, 2, 3, 4, 8, 9, 10, 11} {
		require.True(t, heightsync.NonceInSyncTurn(n, 8, 4, nil), "nonce=%d", n)
	}
	for _, n := range []uint64{5, 6, 7, 12, 15} {
		require.False(t, heightsync.NonceInSyncTurn(n, 8, 4, nil), "nonce=%d", n)
	}
}

func TestClassifyInbound_StaleOriginRejected(t *testing.T) {
	now := time.Now()
	hs := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         11,
		MainnetBlockHashHex:   "aabb",
		OriginatorSenderID:    "gonka1host",
		OriginatorTimestampMs: now.Add(-5 * time.Minute).UnixMilli(),
	}
	v := heightsync.ClassifyInboundRequestAnchor(hs, heightsync.InboundValidateParams{
		Nonce: 5,
		K:     8,
		Slots: 4,
		Now:   now,
	})
	require.Equal(t, heightsync.ResultInvalidStaleOrigin, v.Result)
	require.Equal(t, "stale_origin", v.Reason)
	require.Equal(t, heightsync.TrustDisputeCarrier, v.Trust)
}

func TestClassifyInbound_LazyOutsideSyncTurn(t *testing.T) {
	now := time.Now()
	hs := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         11,
		MainnetBlockHashHex:   "aabb",
		OriginatorSenderID:    "gonka1host",
		OriginatorTimestampMs: now.UnixMilli(),
	}
	v := heightsync.ClassifyInboundRequestAnchor(hs, heightsync.InboundValidateParams{
		Nonce:     5,
		K:         8,
		Slots:     4,
		Now:       now,
		OracleHdr: &blocks.Header{Height: 10},
	})
	require.Equal(t, heightsync.ResultValidLazyAnchor, v.Result)
	require.Equal(t, heightsync.TagLazy, v.Tag)
	require.Equal(t, heightsync.TrustUntrustedPeer, v.Trust)
}

func TestClassifyInbound_CarryForwardInsideSyncTurnIsCadence(t *testing.T) {
	now := time.Now()
	hs := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         11,
		MainnetBlockHashHex:   "aabb",
		OriginatorSenderID:    "gonka1host",
		OriginatorTimestampMs: now.UnixMilli(),
	}
	v := heightsync.ClassifyInboundRequestAnchor(hs, heightsync.InboundValidateParams{
		Nonce:     2,
		K:         8,
		Slots:     4,
		Now:       now,
		OracleHdr: &blocks.Header{Height: 10},
	})
	require.Equal(t, heightsync.ResultValidAnchor, v.Result)
	require.Equal(t, heightsync.TagCadence, v.Tag)
}

func TestClassifyInbound_SyncTurnOmitInvalid(t *testing.T) {
	v := heightsync.ClassifyInboundRequestAnchor(nil, heightsync.InboundValidateParams{
		Nonce: 3,
		K:     8,
		Slots: 4,
	})
	require.Equal(t, heightsync.ResultInvalidSyncTurnOmit, v.Result)
}
