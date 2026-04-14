package types

import "fmt"

// DefaultFeeParams returns the default fee parameters.
// At MinGasPriceNgonka=10 and ~80k gas per tx, fee ≈ 800,000 ngonka ≈ $0.00046 per tx
// (at GNK=$0.57). Governance-adjustable via MsgUpdateParams.
func DefaultFeeParams() *FeeParams {
	return &FeeParams{
		MinGasPriceNgonka: 10, // per gas unit; ~$0.00046 per typical tx
		BaseValidationGas: 500_000,
		GasPerPocCount:    100,
	}
}

// Validate checks that the fee parameters are well-formed.
func (fp *FeeParams) Validate() error {
	if fp == nil {
		return nil
	}
	if fp.MinGasPriceNgonka > 1_000_000 {
		return fmt.Errorf("min_gas_price_ngonka %d exceeds safety limit of 1,000,000", fp.MinGasPriceNgonka)
	}
	return nil
}
