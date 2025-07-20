package types

import (
	"fmt"

	"cosmossdk.io/math"
	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
	"github.com/pkg/errors"
	"github.com/shopspring/decimal"
)

var (
	KeySlashFractionInvalid              = []byte("SlashFractionInvalid")
	KeySlashFractionDowntime             = []byte("SlashFractionDowntime")
	KeyDowntimeMissedPercentageThreshold = []byte("DowntimeMissedPercentageThreshold")
	KeyGracePeriodEndEpoch               = []byte("GracePeriodEndEpoch")
	KeyBaseWeightRatio                   = []byte("BaseWeightRatio")
	KeyCollateralPerWeightUnit           = []byte("CollateralPerWeightUnit")
)

var _ paramtypes.ParamSet = (*Params)(nil)

// ParamKeyTable the param key table for launch module
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&Params{})
}

// NewParams creates a new Params instance
func NewParams() Params {
	return Params{}
}

const million = 1_000_000
const year = 365 * 24 * 60 * 60

func DefaultGenesisOnlyParams() GenesisOnlyParams {
	return GenesisOnlyParams{
		TotalSupply:              1_000 * million,
		OriginatorSupply:         160 * million,
		TopRewardAmount:          120 * million,
		PreProgrammedSaleAmount:  120 * million,
		TopRewards:               3,
		SupplyDenom:              NativeCoin,
		StandardRewardAmount:     600 * million,
		TopRewardPeriod:          year,
		TopRewardPayouts:         12,
		TopRewardPayoutsPerMiner: 4,
		TopRewardMaxDuration:     year * 4,
	}
}

// DefaultParams returns a default set of parameters
func DefaultParams() Params {
	return Params{
		EpochParams:      DefaultEpochParams(),
		ValidationParams: DefaultValidationParams(),
		PocParams:        DefaultPocParams(),
		TokenomicsParams: DefaultTokenomicsParams(),
		CollateralParams: DefaultCollateralParams(),
	}
}

func DefaultEpochParams() *EpochParams {
	return &EpochParams{
		EpochLength:               40,
		EpochMultiplier:           1,
		EpochShift:                0,
		DefaultUnitOfComputePrice: 100,
		PocStageDuration:          10,
		PocExchangeDuration:       2,
		PocValidationDelay:        2,
		PocValidationDuration:     6,
	}
}

func DefaultValidationParams() *ValidationParams {
	return &ValidationParams{
		FalsePositiveRate:           DecimalFromFloat(0.05),
		MinRampUpMeasurements:       10,
		PassValue:                   DecimalFromFloat(0.99),
		MinValidationAverage:        DecimalFromFloat(0.01),
		MaxValidationAverage:        DecimalFromFloat(1.0),
		ExpirationBlocks:            20,
		EpochsToMax:                 30,
		FullValidationTrafficCutoff: 10000,
		MinValidationHalfway:        DecimalFromFloat(0.05),
		MinValidationTrafficCutoff:  100,
		MissPercentageCutoff:        DecimalFromFloat(0.01),
		MissRequestsPenalty:         DecimalFromFloat(1.0),
	}
}

func DefaultPocParams() *PocParams {
	return &PocParams{
		DefaultDifficulty: 5,
	}
}

func DefaultTokenomicsParams() *TokenomicsParams {
	return &TokenomicsParams{
		SubsidyReductionInterval: DecimalFromFloat(0.05),
		SubsidyReductionAmount:   DecimalFromFloat(0.20),
		CurrentSubsidyPercentage: DecimalFromFloat(0.90),
		TopRewardAllowedFailure:  DecimalFromFloat(0.10),
		TopMinerPocQualification: 10,
		WorkVestingPeriod:        0, // Default: no vesting (production: 180, E2E tests: 2)
		RewardVestingPeriod:      0, // Default: no vesting (production: 180, E2E tests: 2)
		TopMinerVestingPeriod:    0, // Default: no vesting (production: 180, E2E tests: 2)
	}
}

func DefaultCollateralParams() *CollateralParams {
	return &CollateralParams{
		SlashFractionInvalid:              DecimalFromFloat(0.20),
		SlashFractionDowntime:             DecimalFromFloat(0.10),
		DowntimeMissedPercentageThreshold: DecimalFromFloat(0.05),
		GracePeriodEndEpoch:               180,
		BaseWeightRatio:                   DecimalFromFloat(0.2),
		CollateralPerWeightUnit:           DecimalFromFloat(1),
	}
}

// ParamSetPairs get the params.ParamSet: Pretty sure this is deprecated
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{}
}

// ParamSetPairs gets the params for the slashing section
func (p *CollateralParams) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeySlashFractionInvalid, &p.SlashFractionInvalid, validateSlashFraction),
		paramtypes.NewParamSetPair(KeySlashFractionDowntime, &p.SlashFractionDowntime, validateSlashFraction),
		paramtypes.NewParamSetPair(KeyDowntimeMissedPercentageThreshold, &p.DowntimeMissedPercentageThreshold, validatePercentage),
		paramtypes.NewParamSetPair(KeyGracePeriodEndEpoch, &p.GracePeriodEndEpoch, validateEpoch),
		paramtypes.NewParamSetPair(KeyBaseWeightRatio, &p.BaseWeightRatio, validateBaseWeightRatio),
		paramtypes.NewParamSetPair(KeyCollateralPerWeightUnit, &p.CollateralPerWeightUnit, validateCollateralPerWeightUnit),
	}
}

func validateEpochParams(i interface{}) error {
	return nil
}

// Validate validates the set of params
func (p Params) Validate() error {
	// TODO: Uncomment this when we have a way to validate the params
	// if err := p.EpochParams.Validate(); err != nil {
	// 	return err
	// }
	// if err := p.ValidationParams.Validate(); err != nil {
	// 	return err
	// }
	// if err := p.PocParams.Validate(); err != nil {
	// 	return err
	// }
	// if err := p.TokenomicsParams.Validate(); err != nil {
	// 	return err
	// }
	if err := p.CollateralParams.Validate(); err != nil {
		return err
	}
	return nil
}

func (p *CollateralParams) Validate() error {
	if err := validateSlashFraction(p.SlashFractionInvalid); err != nil {
		return errors.Wrap(err, "invalid slash_fraction_invalid")
	}
	if err := validateSlashFraction(p.SlashFractionDowntime); err != nil {
		return errors.Wrap(err, "invalid slash_fraction_downtime")
	}
	if err := validatePercentage(p.DowntimeMissedPercentageThreshold); err != nil {
		return errors.Wrap(err, "invalid downtime_missed_percentage_threshold")
	}
	if err := validateEpoch(p.GracePeriodEndEpoch); err != nil {
		return errors.Wrap(err, "invalid grace_period_end_epoch")
	}
	if err := validateBaseWeightRatio(p.BaseWeightRatio); err != nil {
		return errors.Wrap(err, "invalid base_weight_ratio")
	}
	if err := validateCollateralPerWeightUnit(p.CollateralPerWeightUnit); err != nil {
		return errors.Wrap(err, "invalid collateral_per_weight_unit")
	}
	return nil
}

func validateSlashFraction(i interface{}) error {
	v, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	legacyDec, err := v.ToLegacyDec()
	if err != nil {
		return err
	}
	if legacyDec.IsNegative() || legacyDec.GT(math.LegacyOneDec()) {
		return fmt.Errorf("slash fraction must be between 0 and 1, but is %s", legacyDec.String())
	}
	return nil
}

func validateBaseWeightRatio(i interface{}) error {
	v, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	legacyDec, err := v.ToLegacyDec()
	if err != nil {
		return err
	}
	if legacyDec.IsNegative() {
		return fmt.Errorf("base weight ratio cannot be negative: %s", legacyDec)
	}

	if legacyDec.GT(math.LegacyOneDec()) {
		return fmt.Errorf("base weight ratio cannot be greater than 1: %s", legacyDec)
	}

	return nil
}

func validateCollateralPerWeightUnit(i interface{}) error {
	v, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	legacyDec, err := v.ToLegacyDec()
	if err != nil {
		return err
	}
	if legacyDec.IsNegative() {
		return fmt.Errorf("collateral per weight unit cannot be negative: %s", legacyDec)
	}

	return nil
}

func validatePercentage(i interface{}) error {
	v, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	legacyDec, err := v.ToLegacyDec()
	if err != nil {
		return err
	}
	if legacyDec.IsNegative() || legacyDec.GT(math.LegacyOneDec()) {
		return fmt.Errorf("percentage must be between 0 and 1, but is %s", legacyDec.String())
	}
	return nil
}

func validateEpoch(i interface{}) error {
	_, ok := i.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	return nil
}

// ReduceSubsidyPercentage This produces the exact table we expect, as outlined in the whitepaper
// We round to 4 decimal places, and we use decimal to avoid floating point errors
func (p *TokenomicsParams) ReduceSubsidyPercentage() *TokenomicsParams {
	csp := p.CurrentSubsidyPercentage.ToDecimal()
	sra := p.SubsidyReductionAmount.ToDecimal()
	newCSP := csp.Mul(decimal.NewFromFloat(1).Sub(sra)).Round(4)
	p.CurrentSubsidyPercentage = &Decimal{Value: newCSP.CoefficientInt64(), Exponent: newCSP.Exponent()}
	return p
}

func (d *Decimal) ToLegacyDec() (math.LegacyDec, error) {
	return math.LegacyNewDecFromStr(d.ToDecimal().String())
}

func (d *Decimal) ToDecimal() decimal.Decimal {
	return decimal.New(d.Value, d.Exponent)
}

func (d *Decimal) ToFloat() float64 {
	return d.ToDecimal().InexactFloat64()
}

func (d *Decimal) ToFloat32() float32 {
	return float32(d.ToDecimal().InexactFloat64())
}

func DecimalFromFloat(f float64) *Decimal {
	d := decimal.NewFromFloat(f)
	return &Decimal{Value: d.CoefficientInt64(), Exponent: d.Exponent()}
}

func DecimalFromFloat32(f float32) *Decimal {
	d := decimal.NewFromFloat32(f)
	return &Decimal{Value: d.CoefficientInt64(), Exponent: d.Exponent()}
}
