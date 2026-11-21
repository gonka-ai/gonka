package mlnode

import (
	"decentralized-api/mlnodeclient"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDecodeArtifactVector(t *testing.T) {
	for _, tc := range []struct {
		name   string
		points []int
		valid  bool
	}{
		{"valid", []int{0, 15, 1}, true},
		{"short", []int{0, 1}, false},
		{"long", []int{0, 1, 2, 3}, false},
		{"negative", []int{0, -1, 2}, false},
		{"overflow", []int{0, 16, 2}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vector, err := decodeArtifactVector(mlnodeclient.ArtifactV2{KPointsSteps: tc.points}, 2)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, []byte{0, 15, 1}, vector)
			} else {
				require.Error(t, err)
			}
		})
	}
}
