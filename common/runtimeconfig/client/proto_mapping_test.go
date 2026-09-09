package client

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/nodemanager/gen"
)

func TestProtoMapping_RoundTrip(t *testing.T) {
	orig := Snapshot{
		ParamsBlockHeight:       42,
		CurrentEpochID:          7,
		LogprobsMode:            "raw",
		DevshardRequestsEnabled: true,
		MaxNonce:                99,
		ApprovedVersions: []ApprovedVersion{
			{Name: "v1", Binary: "/bin/v1", SHA256: "deadbeef"},
		},
		ServedAt:            time.Unix(1_700_000_123, 0),
		RefusalTimeout:      60,
		ExecutionTimeout:    1200,
		ValidationRate:      5000,
		VoteThresholdFactor: 50,
	}
	round := SnapshotFromProto(ProtoFromSnapshot(orig))
	require.Equal(t, orig, round)
}

func TestToProto_ZeroServedAtEmitsZero(t *testing.T) {
	proto := Snapshot{}.ToProto()
	require.Equal(t, int64(0), proto.GetServedAtUnix())

	round := SnapshotFromProto(proto)
	require.True(t, round.ServedAt.IsZero())
}

func TestSnapshotFromProto_NegativeServedAtUnset(t *testing.T) {
	// Legacy ToProto emitted time.Time{}.Unix() ≈ year-1 sentinel.
	snap := SnapshotFromProto(&gen.RuntimeConfig{ServedAtUnix: time.Time{}.Unix()})
	require.True(t, snap.ServedAt.IsZero())
	require.True(t, time.Time{}.Unix() < 0)
}

func TestProtoFromSnapshot_ZeroServedAtEmitsZero(t *testing.T) {
	proto := ProtoFromSnapshot(Snapshot{})
	require.Equal(t, int64(0), proto.GetServedAtUnix())
}
