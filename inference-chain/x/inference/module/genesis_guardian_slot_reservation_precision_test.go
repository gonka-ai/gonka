package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestDecimalPrecisionPanic_Isolated demonstrates the root cause of the consensus panic.
// This is a pure unit test with no keeper dependencies.
func TestDecimalPrecisionPanic_Isolated(t *testing.T) {
	// Reproduce the exact calculation from ApplyBLSGuardianSlotReservation:
	// m = 0.52, f = m/(1+m) = 0.52/1.52
	// remainderFraction = 1 - f
	// For weight=1, nonGuardianWeight=31 (prime): share = 1/31
	// percent = (1/31) * remainderFraction * 100
	// This produces >18 decimal places

	m := decimal.NewFromFloat(0.52)
	onePlusM := m.Add(decimal.NewFromInt(1))
	f := m.Div(onePlusM)                                       // 0.52/1.52
	remainderFraction := decimal.NewFromInt(1).Sub(f)          // 1 - f
	share := decimal.NewFromInt(1).Div(decimal.NewFromInt(31)) // 1/31 (prime)
	percent := share.Mul(remainderFraction).Mul(decimal.NewFromInt(100))

	rawString := percent.String()
	t.Logf("Raw decimal string: %s (len=%d after dot)", rawString, len(rawString)-2)

	// OLD CODE: This panics with "exceeds max precision"
	t.Run("old_code_panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("PANIC: %v", r)
			} else {
				t.Fatal("Expected panic but didn't get one")
			}
		}()
		_ = mathsdk.LegacyMustNewDecFromStr(rawString)
	})

	// NEW CODE: Truncate to 18 decimals, no panic
	t.Run("new_code_succeeds", func(t *testing.T) {
		truncated := percent.StringFixed(18)
		t.Logf("Truncated string: %s", truncated)

		require.NotPanics(t, func() {
			dec, err := mathsdk.LegacyNewDecFromStr(truncated)
			require.NoError(t, err)
			require.False(t, dec.IsZero())
		}, "New code should not panic with StringFixed(18)")
	})
}

// TestApplyBLSGuardianSlotReservation_RepeatingDecimalPrecision tests that
// repeating decimal percentages (e.g. 1/3 = 0.333...) don't cause panics
// when converted to LegacyDec (max 18 decimal precision).
//
// Before the fix: LegacyMustNewDecFromStr(decimal.String()) panics with
// "value exceeds max precision by -N decimal places"
//
// After the fix: StringFixed(18) truncates to 18 decimals, no panic.
func TestApplyBLSGuardianSlotReservation_RepeatingDecimalPrecision(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	// Setup SDK config for bech32
	config := sdk.GetConfig()
	if config.GetBech32AccountAddrPrefix() != "gonka" {
		config.SetBech32PrefixForAccount("gonka", "gonkapub")
		config.SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")
	}

	// Generate valid addresses
	guardian1AccAddr := sdk.AccAddress([]byte("guardian1___________"))
	guardian1OpAddr := sdk.ValAddress(guardian1AccAddr).String()
	participant2AccAddr := sdk.AccAddress([]byte("participant2________"))
	participant3AccAddr := sdk.AccAddress([]byte("participant3________"))

	// Governance params with guardian
	p, err := k.GetParams(ctx)
	require.NoError(t, err)
	p.GenesisGuardianParams = &types.GenesisGuardianParams{
		NetworkMaturityThreshold: 10_000_000,
		NetworkMaturityMinHeight: 0,
		GuardianAddresses:        []string{guardian1OpAddr},
	}
	require.NoError(t, k.SetParams(ctx, p))

	// Genesis-only params enabling feature
	genesisParams := types.GenesisOnlyParams{
		TotalSupply:               1_000_000_000,
		OriginatorSupply:          160_000_000,
		PreProgrammedSaleAmount:   120_000_000,
		SupplyDenom:               "gonka",
		GenesisGuardianMultiplier: types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:    true,
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Weights that produce repeating decimals when divided:
	// nonGuardianWeight = 1 + 1 + 1 = 3
	// Each non-guardian share = 1/3 = 0.333... (infinite repeating)
	// This triggers the >18 decimal precision issue with decimal.String()
	activeParticipants := []*types.ActiveParticipant{
		{Index: guardian1AccAddr.String(), Weight: 100, CapWeight: 100},
		{Index: participant2AccAddr.String(), Weight: 1, CapWeight: 1},
		{Index: participant3AccAddr.String(), Weight: 1, CapWeight: 1},
	}

	// This should NOT panic after the fix
	result := applyBLSGuardianSlotReservation(ctx, k, activeParticipants, activeParticipants)

	// Result should be non-nil (reservation applied successfully)
	require.NotNil(t, result, "Should return adjusted percentages without panic")

	// Verify guardian has an entry
	_, hasGuardian := result[guardian1AccAddr.String()]
	require.True(t, hasGuardian, "Guardian should have adjusted percentage")

	// Verify non-guardians have entries
	_, hasP2 := result[participant2AccAddr.String()]
	_, hasP3 := result[participant3AccAddr.String()]
	require.True(t, hasP2, "Participant2 should have adjusted percentage")
	require.True(t, hasP3, "Participant3 should have adjusted percentage")
}

// TestApplyBLSGuardianSlotReservation_PrimeWeights tests with prime number weights
// that create long repeating decimal expansions.
func TestApplyBLSGuardianSlotReservation_PrimeWeights(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	config := sdk.GetConfig()
	if config.GetBech32AccountAddrPrefix() != "gonka" {
		config.SetBech32PrefixForAccount("gonka", "gonkapub")
		config.SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")
	}

	guardian1AccAddr := sdk.AccAddress([]byte("guardian1___________"))
	guardian1OpAddr := sdk.ValAddress(guardian1AccAddr).String()
	participant2AccAddr := sdk.AccAddress([]byte("participant2________"))
	participant3AccAddr := sdk.AccAddress([]byte("participant3________"))
	participant4AccAddr := sdk.AccAddress([]byte("participant4________"))

	p, err := k.GetParams(ctx)
	require.NoError(t, err)
	p.GenesisGuardianParams = &types.GenesisGuardianParams{
		NetworkMaturityThreshold: 10_000_000,
		NetworkMaturityMinHeight: 0,
		GuardianAddresses:        []string{guardian1OpAddr},
	}
	require.NoError(t, k.SetParams(ctx, p))

	genesisParams := types.GenesisOnlyParams{
		TotalSupply:               1_000_000_000,
		OriginatorSupply:          160_000_000,
		PreProgrammedSaleAmount:   120_000_000,
		SupplyDenom:               "gonka",
		GenesisGuardianMultiplier: types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:    true,
	}
	k.SetGenesisOnlyParams(ctx, &genesisParams)

	// Prime weights that produce irrational-like decimal expansions
	// 7, 11, 13 are primes; sum = 31 (also prime)
	// Divisions like 7/31, 11/31, 13/31 have long repeating periods
	activeParticipants := []*types.ActiveParticipant{
		{Index: guardian1AccAddr.String(), Weight: 100, CapWeight: 100},
		{Index: participant2AccAddr.String(), Weight: 7, CapWeight: 7},
		{Index: participant3AccAddr.String(), Weight: 11, CapWeight: 11},
		{Index: participant4AccAddr.String(), Weight: 13, CapWeight: 13},
	}

	// Should not panic
	result := applyBLSGuardianSlotReservation(ctx, k, activeParticipants, activeParticipants)
	require.NotNil(t, result, "Should handle prime-weight divisions without panic")
}

func TestApplyBLSGuardianSlotReservation_UsesCapWeight(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	config := sdk.GetConfig()
	if config.GetBech32AccountAddrPrefix() != "gonka" {
		config.SetBech32PrefixForAccount("gonka", "gonkapub")
		config.SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")
	}

	guardianAcc := sdk.AccAddress([]byte("guardian1___________"))
	regularAcc := sdk.AccAddress([]byte("participant2________"))
	newAcc := sdk.AccAddress([]byte("participant3________"))

	p, err := k.GetParams(ctx)
	require.NoError(t, err)
	p.GenesisGuardianParams = &types.GenesisGuardianParams{
		NetworkMaturityThreshold: 10_000_000,
		NetworkMaturityMinHeight: 0,
		GuardianAddresses:        []string{sdk.ValAddress(guardianAcc).String()},
	}
	require.NoError(t, k.SetParams(ctx, p))
	require.NoError(t, k.SetGenesisOnlyParams(ctx, &types.GenesisOnlyParams{
		TotalSupply:               1_000_000_000,
		OriginatorSupply:          160_000_000,
		PreProgrammedSaleAmount:   120_000_000,
		SupplyDenom:               "gonka",
		GenesisGuardianMultiplier: types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:    true,
	}))

	activeParticipants := []*types.ActiveParticipant{
		{Index: guardianAcc.String(), Weight: 100, CapWeight: 0},
		{Index: regularAcc.String(), Weight: 500, CapWeight: 50},
		{Index: newAcc.String(), Weight: 300, CapWeight: 0},
	}
	result := applyBLSGuardianSlotReservation(ctx, k, activeParticipants, activeParticipants)
	require.NotNil(t, result)

	guardianShare, hasGuardian := result[guardianAcc.String()]
	require.True(t, hasGuardian)
	require.False(t, guardianShare.IsZero(), "zero-cap guardian receives only temporary protected power")
	require.True(t, result[newAcc.String()].IsZero(), "zero-cap participant must get no reserved share")
	require.False(t, result[regularAcc.String()].IsZero(), "capped returning participant keeps a remainder share")

	aboveTargetParticipants := []*types.ActiveParticipant{
		{Index: guardianAcc.String(), Weight: 100, CapWeight: 100},
		{Index: regularAcc.String(), Weight: 50, CapWeight: 50},
	}
	aboveTarget := applyBLSGuardianSlotReservation(ctx, k, aboveTargetParticipants, aboveTargetParticipants)
	require.True(t,
		aboveTarget[guardianAcc.String()].Equal(mathsdk.LegacyMustNewDecFromStr("34.210526315789470000")),
		"BLS must reduce an above-target guardian to the exact reserved share",
	)
}
