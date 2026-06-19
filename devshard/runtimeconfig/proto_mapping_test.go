package runtimeconfig

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
