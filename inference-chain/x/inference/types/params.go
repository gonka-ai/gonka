package types

import (
	"fmt"

	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
	"github.com/shopspring/decimal"
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
		EpochParams: &EpochParams{
			EpochLength:               40,
			EpochMultiplier:           1,
			EpochShift:                0,
			DefaultUnitOfComputePrice: 100,
			PocStageDuration:          10,
			PocExchangeDuration:       2,
			PocValidationDelay:        2,
			PocValidationDuration:     6,
		},
		ValidationParams: &ValidationParams{
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
			TimestampExpiration:         60,
			TimestampAdvance:            30,
		},
		PocParams: &PocParams{
			DefaultDifficulty:    5,
			ValidationSampleSize: 200,
		},
		TokenomicsParams: &TokenomicsParams{
			SubsidyReductionInterval: DecimalFromFloat(0.05),
			SubsidyReductionAmount:   DecimalFromFloat(0.20),
			CurrentSubsidyPercentage: DecimalFromFloat(0.90),
			TopRewardAllowedFailure:  DecimalFromFloat(0.10),
			TopMinerPocQualification: 10,
		},
	}
}

// ParamSetPairs get the params.ParamSet: Pretty sure this is deprecated
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{}
}
func validateEpochParams(i interface{}) error {
	return nil
}

// Validate validates the set of params
func (p Params) Validate() error {
	if p.EpochParams == nil {
		return fmt.Errorf("epoch params cannot be nil")
	}
	if p.ValidationParams == nil {
		return fmt.Errorf("validation params cannot be nil")
	}
	if p.PocParams == nil {
		return fmt.Errorf("poc params cannot be nil")
	}
	if p.TokenomicsParams == nil {
		return fmt.Errorf("tokenomics params cannot be nil")
	}

	if p.ValidationParams.FalsePositiveRate == nil {
		return fmt.Errorf("false positive rate cannot be nil")
	}
	if p.ValidationParams.PassValue == nil {
		return fmt.Errorf("pass value cannot be nil")
	}
	if p.ValidationParams.MinValidationAverage == nil {
		return fmt.Errorf("min validation average cannot be nil")
	}
	if p.ValidationParams.MaxValidationAverage == nil {
		return fmt.Errorf("max validation average cannot be nil")
	}
	if p.ValidationParams.MinValidationHalfway == nil {
		return fmt.Errorf("min validation halfway cannot be nil")
	}
	if p.ValidationParams.MissPercentageCutoff == nil {
		return fmt.Errorf("miss percentage cutoff cannot be nil")
	}
	if p.ValidationParams.MissRequestsPenalty == nil {
		return fmt.Errorf("miss requests penalty cannot be nil")
	}

	// Validate timestamp parameters
	if p.ValidationParams.TimestampExpiration <= 0 {
		return fmt.Errorf("timestamp expiration must be positive")
	}
	if p.ValidationParams.TimestampAdvance <= 0 {
		return fmt.Errorf("timestamp advance must be positive")
	}

	if p.TokenomicsParams.SubsidyReductionInterval == nil {
		return fmt.Errorf("subsidy reduction interval cannot be nil")
	}
	if p.TokenomicsParams.SubsidyReductionAmount == nil {
		return fmt.Errorf("subsidy reduction amount cannot be nil")
	}
	if p.TokenomicsParams.CurrentSubsidyPercentage == nil {
		return fmt.Errorf("current subsidy percentage cannot be nil")
	}
	if p.TokenomicsParams.TopRewardAllowedFailure == nil {
		return fmt.Errorf("top reward allowed failure cannot be nil")
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
