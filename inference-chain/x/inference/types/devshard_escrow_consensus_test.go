package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevshardValidationRateForCreate(t *testing.T) {
	require.Equal(t, DefaultDevshardValidationRate, DevshardValidationRateForCreate(nil))
	require.Equal(t, DefaultDevshardValidationRate, DevshardValidationRateForCreate(&DevshardEscrowParams{}))
	require.Equal(t, uint32(3000), DevshardValidationRateForCreate(&DevshardEscrowParams{ValidationRate: 3000}))
	require.Equal(t, uint32(10000), DevshardValidationRateForCreate(&DevshardEscrowParams{ValidationRate: 10000}))
}

func TestDevshardLogprobsModeForCreate(t *testing.T) {
	require.Equal(t, DefaultLogprobsMode, DevshardLogprobsModeForCreate(nil))
	require.Equal(t, DefaultLogprobsMode, DevshardLogprobsModeForCreate(&ValidationParams{}))
	require.Equal(t, DefaultLogprobsMode, DevshardLogprobsModeForCreate(&ValidationParams{LogprobsMode: "processed"}))
	require.Equal(t, LogprobsModeRaw, DevshardLogprobsModeForCreate(&ValidationParams{LogprobsMode: LogprobsModeRaw}))
	require.Equal(t, LogprobsModeProcessed, DevshardLogprobsModeForCreate(&ValidationParams{LogprobsMode: LogprobsModeProcessed}))
}

func TestDevshardEscrowLogprobsModeWireRoundTrip(t *testing.T) {
	in := &DevshardEscrow{
		Id:                  7,
		Creator:             "gonka1abc",
		ModelId:             "Qwen",
		ValidationRate:      5000,
		VoteThresholdFactor: 67,
		LogprobsMode:        LogprobsModeRaw,
	}

	b, err := in.Marshal()
	require.NoError(t, err)
	require.Equal(t, in.Size(), len(b))

	var out DevshardEscrow
	require.NoError(t, out.Unmarshal(b))
	require.Equal(t, *in, out)
}

func TestDevshardEscrowLegacyWireDecodesEmptyLogprobsMode(t *testing.T) {
	legacy := &DevshardEscrow{Id: 1, ModelId: "m", ValidationRate: 5000, VoteThresholdFactor: 50}
	b, err := legacy.Marshal()
	require.NoError(t, err)

	var out DevshardEscrow
	require.NoError(t, out.Unmarshal(b))
	require.Equal(t, "", out.LogprobsMode)
}
