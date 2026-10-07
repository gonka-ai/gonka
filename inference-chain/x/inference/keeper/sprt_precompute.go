package keeper

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

// PrecomputeSPRTValues checks the SPRT params and computes the log-likelihood ratios for them.
// The values are not stored: GetPrecomputedSPRTValues derives them from the current params.
func (k Keeper) PrecomputeSPRTValues(ctx context.Context) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	_, err = sprtValuesBytes(params)
	return err
}

func sprtValuesBytes(params types.Params) ([]byte, error) {
	vp := params.ValidationParams
	if vp == nil {
		vp = types.DefaultValidationParams()
	}

	// Validate all required parameters
	if vp.BadParticipantInvalidationRate == nil {
		return nil, fmt.Errorf("BadParticipantInvalidationRate is nil")
	}
	if vp.BadParticipantInvalidationRate.ToDecimal().LessThan(decimal.Zero) || vp.BadParticipantInvalidationRate.ToDecimal().GreaterThan(decimal.NewFromInt(1)) {
		return nil, fmt.Errorf("BadParticipantInvalidationRate must be between 0 and 1, got: %s", vp.BadParticipantInvalidationRate.String())
	}

	if vp.FalsePositiveRate == nil {
		return nil, fmt.Errorf("FalsePositiveRate is nil")
	}
	if vp.FalsePositiveRate.ToDecimal().LessThan(decimal.Zero) || vp.FalsePositiveRate.ToDecimal().GreaterThan(decimal.NewFromInt(1)) {
		return nil, fmt.Errorf("FalsePositiveRate must be between 0 and 1, got: %s", vp.FalsePositiveRate.String())
	}

	if vp.DowntimeBadPercentage == nil {
		return nil, fmt.Errorf("DowntimeBadPercentage is nil")
	}
	if vp.DowntimeBadPercentage.ToDecimal().LessThan(decimal.Zero) || vp.DowntimeBadPercentage.ToDecimal().GreaterThan(decimal.NewFromInt(1)) {
		return nil, fmt.Errorf("DowntimeBadPercentage must be between 0 and 1, got: %s", vp.DowntimeBadPercentage.String())
	}

	if vp.DowntimeGoodPercentage == nil {
		return nil, fmt.Errorf("DowntimeGoodPercentage is nil")
	}
	if vp.DowntimeGoodPercentage.ToDecimal().LessThan(decimal.Zero) || vp.DowntimeGoodPercentage.ToDecimal().GreaterThan(decimal.NewFromInt(1)) {
		return nil, fmt.Errorf("DowntimeGoodPercentage must be between 0 and 1, got: %s", vp.DowntimeGoodPercentage.String())
	}

	in := sprtInputs{*vp.BadParticipantInvalidationRate, *vp.FalsePositiveRate, *vp.DowntimeBadPercentage, *vp.DowntimeGoodPercentage}
	m := lastSPRT.Load()
	if m == nil || m.in != in {
		precomputed := &types.SPRTPrecomputedValues{
			InvalidationLogFail: types.DecimalFromDecimal(CalculateLogLLR(vp.BadParticipantInvalidationRate.ToDecimal(), vp.FalsePositiveRate.ToDecimal(), true)),
			InvalidationLogPass: types.DecimalFromDecimal(CalculateLogLLR(vp.BadParticipantInvalidationRate.ToDecimal(), vp.FalsePositiveRate.ToDecimal(), false)),
			InactiveLogFail:     types.DecimalFromDecimal(CalculateLogLLR(vp.DowntimeBadPercentage.ToDecimal(), vp.DowntimeGoodPercentage.ToDecimal(), true)),
			InactiveLogPass:     types.DecimalFromDecimal(CalculateLogLLR(vp.DowntimeBadPercentage.ToDecimal(), vp.DowntimeGoodPercentage.ToDecimal(), false)),
		}
		bz, err := precomputed.Marshal()
		if err != nil {
			return nil, err
		}
		m = &sprtMemo{in: in, bz: bz}
		lastSPRT.Store(m)
	}
	return m.bz, nil
}

// The values are a pure function of four governance params, so the Ln work is
// redone only when one of them changes.
type sprtInputs [4]types.Decimal

type sprtMemo struct {
	in sprtInputs
	bz []byte
}

var lastSPRT atomic.Pointer[sprtMemo]

// In the rare case of some kind of error, default to zero
// This effectively turns off SPRT tracking, meaning no one will be removed from the network
// while we figure out what has gone wrong. It's the best alternative to an unlikely
// situation.
var zeroSprtValues = types.SPRTPrecomputedValues{
	InactiveLogFail:     &types.DecimalZero,
	InactiveLogPass:     &types.DecimalZero,
	InvalidationLogFail: &types.DecimalZero,
	InvalidationLogPass: &types.DecimalZero,
}

// GetPrecomputedSPRTValues returns the SPRT values for the current params.
func (k Keeper) GetPrecomputedSPRTValues(ctx context.Context) types.SPRTPrecomputedValues {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Failed to get params for SPRT values", types.Validation, "error", err)
		return zeroSprtValues
	}
	return k.sprtValuesFor(params)
}

// sprtValuesFor derives the SPRT values from params the caller already holds.
func (k Keeper) sprtValuesFor(params types.Params) types.SPRTPrecomputedValues {
	bz, err := sprtValuesBytes(params)
	if err != nil {
		k.LogError("Invalid SPRT params", types.Validation, "error", err)
		return zeroSprtValues
	}

	var precomputed types.SPRTPrecomputedValues
	if err := precomputed.Unmarshal(bz); err != nil {
		k.LogError("Failed to unmarshal SPRT precomputed values", types.Validation, "error", err)
		return zeroSprtValues
	}

	return precomputed
}

const precision = int32(12)

// CalculateLogLLR calculates the log-likelihood ratio for a given success/failure.
func CalculateLogLLR(p1, p0 decimal.Decimal, isFail bool) decimal.Decimal {
	one := decimal.NewFromInt(1)
	// We default to zero for errors/edges because we have no logical fallback and these values
	// are invalid and will never get past governance, so the default of "don't impact SPRT" is the
	// best we can do
	if isFail {
		// ln(p1/p0)
		if p1.IsZero() || p0.IsZero() {
			return decimal.Zero
		}
		res, _ := p1.Div(p0).Ln(precision)
		return res
	}
	// ln((1-p1)/(1-p0))
	if p1.Equal(one) || p0.Equal(one) {
		return decimal.Zero
	}
	res, _ := one.Sub(p1).Div(one.Sub(p0)).Ln(precision)
	return res
}
