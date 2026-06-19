package types_test

import (
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestDefaultDevshardEscrowParams_FeeDefaults(t *testing.T) {
	p := types.DefaultDevshardEscrowParams()
	require.Equal(t, types.DefaultDevshardCreateDevshardFee, p.CreateDevshardFee)
	require.Equal(t, types.DefaultDevshardFeePerNonce, p.FeePerNonce)
	require.Equal(t, types.DefaultDevshardRefusalTimeout, p.RefusalTimeout)
	require.Equal(t, types.DefaultDevshardExecutionTimeout, p.ExecutionTimeout)
	require.Equal(t, types.DefaultDevshardValidationRate, p.ValidationRate)
	require.Equal(t, types.DefaultDevshardVoteThresholdFactor, p.VoteThresholdFactor)
	require.NoError(t, p.Validate())
}

func TestDevshardEscrowParams_ProtoRoundTrip_Phase4Fields(t *testing.T) {
	orig := types.DefaultDevshardEscrowParams()
	orig.RefusalTimeout = 99
	orig.ExecutionTimeout = 2000
	orig.ValidationRate = 3000
	orig.VoteThresholdFactor = 55
	orig.CreateDevshardFee = 11_111
	orig.FeePerNonce = 2_222

	bz, err := proto.Marshal(orig)
	require.NoError(t, err)

	var decoded types.DevshardEscrowParams
	require.NoError(t, proto.Unmarshal(bz, &decoded))
	require.Equal(t, orig, &decoded)
}

func TestDevshardEscrowParams_Validate_RejectsInvalidPhase4(t *testing.T) {
	base := func() *types.DevshardEscrowParams {
		p := types.DefaultDevshardEscrowParams()
		return p
	}

	t.Run("refusal_timeout", func(t *testing.T) {
		p := base()
		p.RefusalTimeout = 0
		require.ErrorContains(t, p.Validate(), "refusal_timeout")
	})

	t.Run("execution_timeout", func(t *testing.T) {
		p := base()
		p.ExecutionTimeout = -1
		require.ErrorContains(t, p.Validate(), "execution_timeout")
	})

	t.Run("validation_rate", func(t *testing.T) {
		p := base()
		p.ValidationRate = 10001
		require.ErrorContains(t, p.Validate(), "validation_rate")
	})

	t.Run("vote_threshold_factor_zero", func(t *testing.T) {
		p := base()
		p.VoteThresholdFactor = 0
		require.ErrorContains(t, p.Validate(), "vote_threshold_factor")
	})

	t.Run("vote_threshold_factor_over_100", func(t *testing.T) {
		p := base()
		p.VoteThresholdFactor = 101
		require.ErrorContains(t, p.Validate(), "vote_threshold_factor")
	})
}
