package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrainingParamsValidate_EpochLimitsBindOnlyWhenEnabled(t *testing.T) {
	epoch := &EpochParams{EpochLength: 2000}

	params := DefaultTrainingParams()
	require.NoError(t, params.Validate(epoch))

	params.TrainingEnabled = true
	require.ErrorContains(t, params.Validate(epoch), "settled_shard_retention_blocks")

	params.SettledShardRetentionBlocks = 2*epoch.EpochLength + params.ReleaseBufferBlocks
	require.ErrorContains(t, params.Validate(epoch), "opt_in_ttl_blocks")

	params.OptInTtlBlocks = epoch.EpochLength
	require.NoError(t, params.Validate(epoch))
}

func TestParamsValidate_DefaultsHoldForRealEpochLengths(t *testing.T) {
	for _, epochLength := range []int64{50, 250, 360, 2000, 17280} {
		params := DefaultParams()
		params.EpochParams.EpochLength = epochLength
		require.NoError(t, params.Validate(), "epoch length %d", epochLength)
	}
}
