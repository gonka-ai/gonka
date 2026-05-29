package simulation

import (
	"math/rand"

	"github.com/productscience/inference/x/inference/types"
)

// MutateSimParams randomizes the governance-mutable Params for each sim seed,
// pushing values to the real Params.Validate() boundaries and across all seven
// in-scope sub-structs. Per issue #982 Phase 3: "explore parameter boundaries
// and parameter combinations more aggressively."
//
// Three design rules, derived from the code (not from caution):
//
//  1. Most economic params (prices, vesting, fees, weight ratios) are pushed to
//     their Validate() boundaries. They are liveness-safe — an extreme price or
//     vesting period just makes a tx fail gracefully (escrow > balance, etc.),
//     it does not halt the chain — and they stress the accounting/invariant
//     paths where findings #1265/#1269/#1273 live.
//  2. Timing params (EpochParams) are widened but kept internally consistent:
//     Validate() does NOT enforce that the PoC + validation stages fit inside
//     EpochLength, but the simulation requires it. So EpochLength is fuzzed
//     within a survivable band (floor 30, matching the proven baseline) and the
//     stage durations are derived as fractions of it.
//  3. Collateral SLASHING-activation levers (SlashFractionInvalid/Downtime,
//     DowntimeMissedPercentageThreshold, GracePeriodEndEpoch) are NOT fuzzed.
//     The sim's upstream-validator substrate runs at Tokens=1 (an InitChain
//     shrink hack, see app/sim_test.go shrinkUpstreamStakingValidators), and
//     ANY active slashing zeroes Tokens+DelegatorShares → x/slashing Unjail
//     divides 0/0 → validators jail and never recover → the cometbft set
//     drains and the run SKIPs on "empty validator set". This is a sim-substrate
//     limitation, NOT a production constraint; those params are covered by unit
//     tests, not by genesis fuzzing.
//
// GenesisOnlyParams (collateral_amount, model_init_params, ...) are NOT touched
// — only runtime-mutable Params. See docs/simulation.md §Parameter fuzzing.
//
// Parameter COMBINATIONS are exercised via corner profiles: ~1/4 of seeds drive
// every knob to its low boundary, ~1/4 to its high boundary, the rest spread
// independently — so corner-combination bugs surface instead of averaging out.
func MutateSimParams(rng *rand.Rand, params *types.Params) {
	if params == nil {
		return
	}
	p := pickProfile(rng)

	if params.EpochParams != nil {
		epochLen := fuzzInt64(rng, p, 30, 120)
		params.EpochParams.EpochLength = epochLen
		params.EpochParams.PocStageDuration = clampMinI64(epochLen/5, 2)      // ~20%, >=2
		params.EpochParams.PocValidationDuration = clampMinI64(epochLen/8, 1) // ~12%, >=1
		params.EpochParams.InferencePruningEpochThreshold = fuzzUint64(rng, p, 1, 10)
	}
	if params.ValidationParams != nil {
		params.ValidationParams.MinRampUpMeasurements = fuzzInt32(rng, p, 1, 50)
		params.ValidationParams.ExpirationBlocks = fuzzInt64(rng, p, 5, 80)
		params.ValidationParams.MinValidationTrafficCutoff = fuzzInt64(rng, p, 0, 1000)
	}
	if params.PocParams != nil {
		params.PocParams.DefaultDifficulty = fuzzInt32(rng, p, 1, 20)
		params.PocParams.PocDataPruningEpochThreshold = fuzzUint64(rng, p, 1, 10)
	}
	if params.FeeParams != nil {
		params.FeeParams.MinGasPriceNgonka = fuzzUint64(rng, p, 0, 5000)
		params.FeeParams.BaseValidationGas = fuzzUint64(rng, p, 500, 50000)
	}
	// CollateralParams — only the non-slashing economic levers. The slashing
	// levers (SlashFraction*, DowntimeMissedPercentageThreshold,
	// GracePeriodEndEpoch) are deliberately left at defaults: active slashing
	// drains the Tokens=1 sim validator substrate (see rule 3 above).
	if params.CollateralParams != nil {
		params.CollateralParams.BaseWeightRatio = fuzzDecimal(rng, p, 0, 1)          // [0,1]
		params.CollateralParams.CollateralPerWeightUnit = fuzzDecimal(rng, p, 0, 100) // >=0
	}
	if params.TokenomicsParams != nil {
		params.TokenomicsParams.WorkVestingPeriod = fuzzUint64(rng, p, 0, 360)
		params.TokenomicsParams.RewardVestingPeriod = fuzzUint64(rng, p, 0, 360)
	}
	// DynamicPricingParams — lower<upper enforced by construction; prices >0.
	if params.DynamicPricingParams != nil {
		params.DynamicPricingParams.StabilityZoneLowerBound = fuzzDecimal(rng, p, 0, 0.49)
		params.DynamicPricingParams.StabilityZoneUpperBound = fuzzDecimal(rng, p, 0.51, 1.0)
		params.DynamicPricingParams.PriceElasticity = fuzzDecimal(rng, p, 0.01, 1.0) // (0,1]
		params.DynamicPricingParams.UtilizationWindowDuration = fuzzUint64(rng, p, 1, 600)
		params.DynamicPricingParams.MinPerTokenPrice = fuzzUint64(rng, p, 1, 1000)
		params.DynamicPricingParams.BasePerTokenPrice = fuzzUint64(rng, p, 1, 2000)
		params.DynamicPricingParams.GracePeriodPerTokenPrice = fuzzUint64(rng, p, 0, 1000)
		params.DynamicPricingParams.GracePeriodEndEpoch = fuzzUint64(rng, p, 0, 50)
	}
}

// fuzzProfile biases a whole seed toward a corner of the parameter space.
type fuzzProfile int

const (
	profileSpread fuzzProfile = iota // independent uniform draws
	profileLow                       // every field at its low boundary
	profileHigh                      // every field at its high boundary
)

func pickProfile(rng *rand.Rand) fuzzProfile {
	switch rng.Intn(4) {
	case 0:
		return profileLow
	case 1:
		return profileHigh
	default:
		return profileSpread
	}
}

func fuzzInt64(rng *rand.Rand, p fuzzProfile, min, max int64) int64 {
	switch p {
	case profileLow:
		return min
	case profileHigh:
		return max
	default:
		return randInt64(rng, min, max)
	}
}

func fuzzUint64(rng *rand.Rand, p fuzzProfile, min, max uint64) uint64 {
	switch p {
	case profileLow:
		return min
	case profileHigh:
		return max
	default:
		return randUint64(rng, min, max)
	}
}

func fuzzInt32(rng *rand.Rand, p fuzzProfile, min, max int32) int32 {
	switch p {
	case profileLow:
		return min
	case profileHigh:
		return max
	default:
		return randInt32(rng, min, max)
	}
}

// fuzzDecimal returns a *types.Decimal in [lo,hi], boundary-pulled per profile.
func fuzzDecimal(rng *rand.Rand, p fuzzProfile, lo, hi float64) *types.Decimal {
	var f float64
	switch p {
	case profileLow:
		f = lo
	case profileHigh:
		f = hi
	default:
		f = lo + rng.Float64()*(hi-lo)
	}
	return types.DecimalFromFloat(f)
}

func clampMinI64(v, min int64) int64 {
	if v < min {
		return min
	}
	return v
}

// randInt64 returns a random int64 in [min, max] inclusive.
func randInt64(rng *rand.Rand, min, max int64) int64 {
	if max <= min {
		return min
	}
	return min + rng.Int63n(max-min+1)
}

// randUint64 returns a random uint64 in [min, max] inclusive.
func randUint64(rng *rand.Rand, min, max uint64) uint64 {
	if max <= min {
		return min
	}
	return min + uint64(rng.Int63n(int64(max-min+1)))
}

// randInt32 returns a random int32 in [min, max] inclusive.
func randInt32(rng *rand.Rand, min, max int32) int32 {
	if max <= min {
		return min
	}
	return min + rng.Int31n(max-min+1)
}
