package types_test

import (
	"fmt"
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
	require.Equal(t, int64(32*60), p.ExecutionTimeout)
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

func TestDevshardEscrowParams_ValidateVersionNameContract(t *testing.T) {
	setVersion := func(p *types.DevshardEscrowParams, name string) {
		p.ApprovedVersions = []*types.DevshardApprovedVersion{{
			Name:   name,
			Binary: "https://example.invalid/devshard.zip",
			Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}}
	}
	valid := []string{"v5", "v4+hotfix", "v4}x", "v9;hotfix"}
	for _, name := range valid {
		t.Run("valid_"+name, func(t *testing.T) {
			p := types.DefaultDevshardEscrowParams()
			setVersion(p, name)
			require.NoError(t, p.Validate())
		})
	}

	invalid := []string{"", ".", "..", " v5", "v 5", "v5/next", `v5\next`, "v5?x", "v5#x", "v5%x", `v5"x`, "v5'x", "v5\nnext"}
	for _, name := range invalid {
		t.Run("invalid_"+name, func(t *testing.T) {
			p := types.DefaultDevshardEscrowParams()
			setVersion(p, name)
			require.ErrorContains(t, p.Validate(), "invalid name")
		})
	}

	p := types.DefaultDevshardEscrowParams()
	p.ApprovedVersions = []*types.DevshardApprovedVersion{nil}
	require.ErrorContains(t, p.Validate(), "cannot be null")
}

func TestDevshardEscrowParams_ValidateApprovedVersionCapacity(t *testing.T) {
	p := types.DefaultDevshardEscrowParams()
	for i := 0; i <= types.MaxDevshardApprovedVersions; i++ {
		p.ApprovedVersions = append(p.ApprovedVersions, &types.DevshardApprovedVersion{
			Name:   fmt.Sprintf("v%d", i),
			Binary: "https://example.invalid/devshard.zip",
			Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		})
	}
	require.ErrorContains(t, p.Validate(), "maximum is 32")
}
