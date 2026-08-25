package transport

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

func testVerifiedBlob() (blob, sig []byte) {
	return []byte("blob"), []byte{1}
}

func TestHeightSyncPeerTips_SnapshotVerifiedReady(t *testing.T) {
	tips := NewHeightSyncPeerTips()
	now := time.UnixMilli(2_000_000)

	require.False(t, tips.Snapshot(now).CacheReady)

	blob, sig := testVerifiedBlob()
	tips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         42,
		MainnetBlockHashHex:   "abc123def456",
		OriginatorSenderID:    "gonka1host",
		OriginatorTimestampMs: now.UnixMilli(),
	}, blob, sig)

	st := tips.Snapshot(now)
	require.True(t, st.CacheReady)
	require.Equal(t, 1, st.VerifiedOriginators)
	require.Equal(t, int64(42), st.MaxFreshHeight)
	require.Equal(t, "abc123de", st.BlockHashPrefix)
}

func TestHeightSyncPeerTips_FreshnessFilter(t *testing.T) {
	tips := NewHeightSyncPeerTips()
	tips.Freshness = 60 * time.Second
	now := time.UnixMilli(1_000_000)

	staleHigh := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         200,
		MainnetBlockHashHex:   "stale",
		OriginatorSenderID:    "gonka1stale",
		OriginatorTimestampMs: now.Add(-2 * time.Minute).UnixMilli(),
	}
	freshLow := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         100,
		MainnetBlockHashHex:   "fresh",
		OriginatorSenderID:    "gonka1fresh",
		OriginatorTimestampMs: now.Add(-10 * time.Second).UnixMilli(),
	}

	blob, sig := testVerifiedBlob()
	tips.RecordOriginWithBlob(staleHigh, blob, sig)
	tips.RecordOriginWithBlob(freshLow, blob, sig)

	got := tips.MaxFresh(now, tips.Freshness)
	require.NotNil(t, got)
	require.Equal(t, int64(100), got.MainnetHeight)
	require.Equal(t, "gonka1fresh", got.OriginatorSenderID)
	require.Equal(t, "fresh", got.MainnetBlockHashHex)
}

func TestHeightSyncPeerTips_PerPeerPropagation(t *testing.T) {
	tips := NewHeightSyncPeerTips()

	require.False(t, tips.ShouldPropagateTo("host-b", 0))
	require.True(t, tips.ShouldPropagateTo("host-b", 50))
	require.False(t, tips.ShouldPropagateTo("", 50))

	tips.MarkPropagated("host-b", 50)
	require.False(t, tips.ShouldPropagateTo("host-b", 50))
	require.True(t, tips.ShouldPropagateTo("host-b", 51))
	require.True(t, tips.ShouldPropagateTo("host-c", 50))

	tips.MarkPropagated("host-b", 40)
	require.Equal(t, uint64(50), tips.lastPropagated["host-b"], "MarkPropagated must not lower recorded height")
}

func TestHeightSyncPeerTips_CarryPreservesOriginator(t *testing.T) {
	tips := NewHeightSyncPeerTips()
	now := time.Now()
	blob, sig := testVerifiedBlob()
	tips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         42,
		MainnetBlockHashHex:   "aabb",
		ChainID:               "gonka-test",
		OriginatorSenderID:    "gonka1hostA",
		OriginatorTimestampMs: now.UnixMilli(),
		TimestampUnixMs:       now.UnixMilli(),
	}, blob, sig)

	outbound := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       10,
		MainnetBlockHashHex: "local",
		TimestampUnixMs:     now.Add(time.Second).UnixMilli(),
		OriginatorSenderID:  "gonka1user-should-not-stick",
	}
	tips.Carry(outbound)

	require.Equal(t, int64(42), outbound.MainnetHeight)
	require.Equal(t, "aabb", outbound.MainnetBlockHashHex)
	require.Equal(t, "gonka1hostA", outbound.OriginatorSenderID)
	require.Equal(t, now.UnixMilli(), outbound.OriginatorTimestampMs)
	require.NotEqual(t, "gonka1user-should-not-stick", outbound.OriginatorSenderID)
}

func TestHeightSyncPeerTips_CarryOverwritesOriginatorAtSameHeight(t *testing.T) {
	tips := NewHeightSyncPeerTips()
	now := time.Now()
	blob, sig := testVerifiedBlob()
	tips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         11,
		MainnetBlockHashHex:   "aabb",
		OriginatorSenderID:    "gonka1hostA",
		OriginatorTimestampMs: now.UnixMilli(),
	}, blob, sig)
	outbound := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         11,
		MainnetBlockHashHex:   "aabb",
		OriginatorSenderID:    "gonka1user-should-not-stick",
		OriginatorTimestampMs: now.Add(time.Second).UnixMilli(),
	}
	tips.Carry(outbound)
	require.Equal(t, "gonka1hostA", outbound.OriginatorSenderID)
	require.Equal(t, now.UnixMilli(), outbound.OriginatorTimestampMs)
}

func TestHeightSyncPeerTips_UpdateBackwardCompatWithoutOriginator(t *testing.T) {
	tips := NewHeightSyncPeerTips()
	now := time.Now()
	blob, sig := testVerifiedBlob()

	tips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       11,
		MainnetBlockHashHex: "aa",
		TimestampUnixMs:     now.UnixMilli(),
	}, blob, sig)

	got := tips.MaxFresh(now, time.Minute)
	require.NotNil(t, got)
	require.Equal(t, int64(11), got.MainnetHeight)
}

func TestRecordOriginWithBlob_StoresVerbatimBlob(t *testing.T) {
	tips := NewHeightSyncPeerTips()
	sec := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         77,
		MainnetBlockHashHex:   "cafe",
		OriginatorSenderID:    "gonka1host",
		OriginatorTimestampMs: time.Now().UnixMilli(),
	}
	blob := []byte("blob-bytes")
	sig := []byte{1, 2, 3}
	tips.RecordOriginWithBlob(sec, blob, sig)

	gotBlob, gotSig, ok := tips.OriginSignedBlobFor("gonka1host", 77)
	require.True(t, ok)
	require.Equal(t, blob, gotBlob)
	require.Equal(t, sig, gotSig)
}

func TestMaxFresh_SkipsEntriesWithoutBlob(t *testing.T) {
	tips := NewHeightSyncPeerTips()
	require.True(t, tips.RequireVerifiedBlob, "production constructor must require verified blobs")
	now := time.Now()
	tips.RecordOrigin(&heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         50,
		MainnetBlockHashHex:   "aa",
		OriginatorSenderID:    "gonka1no-blob",
		OriginatorTimestampMs: now.UnixMilli(),
	})
	require.Nil(t, tips.MaxFresh(now, time.Minute))

	unverifiedCarry := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       10,
		MainnetBlockHashHex: "local",
	}
	tips.Carry(unverifiedCarry)
	require.Equal(t, int64(10), unverifiedCarry.MainnetHeight, "Carry must ignore RecordOrigin without blob")

	tips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         51,
		MainnetBlockHashHex:   "bb",
		OriginatorSenderID:    "gonka1with-blob",
		OriginatorTimestampMs: now.UnixMilli(),
	}, []byte("b"), []byte{9})
	got := tips.MaxFresh(now, time.Minute)
	require.NotNil(t, got)
	require.Equal(t, int64(51), got.MainnetHeight)
}

func TestMaxFresh_ZeroTimestampIsNotFresh(t *testing.T) {
	tips := NewHeightSyncPeerTips()
	now := time.Now()
	blob, sig := testVerifiedBlob()
	tips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       11,
		MainnetBlockHashHex: "aabb",
		OriginatorSenderID:  "gonka1host",
	}, blob, sig)
	require.Nil(t, tips.MaxFresh(now, time.Minute), "zero originator timestamp is arbitrarily old")

	outbound := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       1,
		MainnetBlockHashHex: "local",
	}
	tips.Carry(outbound)
	require.Equal(t, int64(1), outbound.MainnetHeight, "Carry must not promote a zero-ts cache entry")
}

func TestOriginSignedBlobFor_Lookup(t *testing.T) {
	tips := NewHeightSyncPeerTips()
	_, _, ok := tips.OriginSignedBlobFor("missing", 1)
	require.False(t, ok)
}
