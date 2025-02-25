package types

import (
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
		},
		ValidationParams: &ValidationParams{
			FalsePositiveRate:           0.05,
			MinRampUpMeasurements:       10,
			PassValue:                   0.99,
			MinValidationAverage:        0.01,
			MaxValidationAverage:        1.0,
			ExpirationBlocks:            20,
			EpochsToMax:                 30,
			FullValidationTrafficCutoff: 10000,
			MinValidationHalfway:        0.05,
			MinValidationTrafficCutoff:  100,
		},
		PocParams: &PocParams{
			DefaultDifficulty: 5,
		},
		TokenomicsParams: &TokenomicsParams{
			SubsidyReductionInterval: 0.05,
			SubsidyReductionAmount:   0.20,
			CurrentSubsidyPercentage: 0.90,
			TopRewardAllowedFailure:  0.10,
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
	return nil
}

// ReduceSubsidyPercentage This produces the exact table we expect, as outlined in the whitepaper
// We round to 4 decimal places, and we use decimal to avoid floating point errors
func (p *TokenomicsParams) ReduceSubsidyPercentage() *TokenomicsParams {
	csp := decimal.NewFromFloat32(p.CurrentSubsidyPercentage)
	sra := decimal.NewFromFloat32(p.SubsidyReductionAmount)
	newCSP := csp.Mul(decimal.NewFromFloat(1).Sub(sra))
	f, _ := newCSP.Round(4).Float64()
	p.CurrentSubsidyPercentage = float32(f)
	return p
}
