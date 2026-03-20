package keeper

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/require"
)

func TestFindDonorIndex_TiePrefersLargerRemainder(t *testing.T) {
	participants := []types.ParticipantWithWeightAndKey{
		{Address: "victim", PercentageWeight: math.LegacyNewDec(50)},
		{Address: "attacker", PercentageWeight: math.LegacyNewDec(50)},
		{Address: "dust", PercentageWeight: math.LegacyNewDec(1)},
	}
	assigned := []int64{5, 5, 1}
	remainders := []math.LegacyDec{
		math.LegacyMustNewDecFromStr("0.01"),
		math.LegacyMustNewDecFromStr("0.99"),
		math.LegacyMustNewDecFromStr("0.40"),
	}

	donor := findDonorIndex(assigned, remainders, participants)
	require.Equal(t, 1, donor, "donor should be participant with larger remainder when assigned slots tie")
}

func TestFindDonorIndex_EqualRemaindersUsesAddressTieBreak(t *testing.T) {
	participants := []types.ParticipantWithWeightAndKey{
		{Address: "zeta", PercentageWeight: math.LegacyNewDec(50)},
		{Address: "alpha", PercentageWeight: math.LegacyNewDec(50)},
	}
	assigned := []int64{4, 4}
	remainders := []math.LegacyDec{
		math.LegacyMustNewDecFromStr("0.50"),
		math.LegacyMustNewDecFromStr("0.50"),
	}

	donor := findDonorIndex(assigned, remainders, participants)
	require.Equal(t, 1, donor, "address tie-break should remain deterministic")
}
