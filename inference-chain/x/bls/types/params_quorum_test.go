package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDKGQuorumSlots(t *testing.T) {
	defaultParams := DefaultParams()
	slots, err := defaultParams.DKGQuorumSlots(100)
	require.NoError(t, err)
	require.Equal(t, uint64(51), slots)

	supermajority := defaultParams
	supermajority.DkgQuorumNumerator = 2
	supermajority.DkgQuorumDenominator = 3
	require.NoError(t, supermajority.Validate())
	slots, err = supermajority.DKGQuorumSlots(100)
	require.NoError(t, err)
	require.Equal(t, uint64(67), slots)

	unset := defaultParams
	unset.DkgQuorumNumerator = 0
	unset.DkgQuorumDenominator = 0
	_, err = unset.DKGQuorumSlots(100)
	require.Error(t, err)
}

func TestDKGQuorumFractionValidation(t *testing.T) {
	params := DefaultParams()

	params.DkgQuorumNumerator = 2
	params.DkgQuorumDenominator = 2
	require.Error(t, params.Validate())

	params.DkgQuorumNumerator = 3
	params.DkgQuorumDenominator = 2
	require.Error(t, params.Validate())

	params.DkgQuorumNumerator = 0
	params.DkgQuorumDenominator = 3
	require.Error(t, params.Validate())

	params.DkgQuorumNumerator = 1
	params.DkgQuorumDenominator = 1
	require.Error(t, params.Validate())
}

func TestMaxDealerAttemptsPerEpochValidation(t *testing.T) {
	params := DefaultParams()
	require.Equal(t, uint32(3), params.MaxDealerAttemptsPerEpoch)
	require.NoError(t, params.Validate())

	params.MaxDealerAttemptsPerEpoch = 0
	require.Error(t, params.Validate())

	params.MaxDealerAttemptsPerEpoch = 1
	require.NoError(t, params.Validate())
}
