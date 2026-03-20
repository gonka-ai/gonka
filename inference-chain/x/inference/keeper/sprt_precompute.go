package keeper

import (
	"context"
	"fmt"

	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

// PrecomputeSPRTValues calculates the log-likelihood ratios for SPRT based on current parameters
// and stores them in the transient store for fast access during the block.
func (k Keeper) PrecomputeSPRTValues(ctx context.Context) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	vp := params.ValidationParams
	if vp == nil {
		vp = types.DefaultValidationParams()
	}

	// Validate all required parameters
	if vp.BadParticipantInvalidationRate == nil {
		return fmt.Errorf("BadParticipantInvalidationRate is nil")
	}
	if vp.BadParticipantInvalidationRate.ToDecimal().LessThan(decimal.Zero) || vp.BadParticipantInvalidationRate.ToDecimal().GreaterThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("BadParticipantInvalidationRate must be between 0 and 1, got: %s", vp.BadParticipantInvalidationRate.String())
	}

	if vp.FalsePositiveRate == nil {
		return fmt.Errorf("FalsePositiveRate is nil")
	}
	if vp.FalsePositiveRate.ToDecimal().LessThan(decimal.Zero) || vp.FalsePositiveRate.ToDecimal().GreaterThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("FalsePositiveRate must be between 0 and 1, got: %s", vp.FalsePositiveRate.String())
	}

	if vp.DowntimeBadPercentage == nil {
		return fmt.Errorf("DowntimeBadPercentage is nil")
	}
	if vp.DowntimeBadPercentage.ToDecimal().LessThan(decimal.Zero) || vp.DowntimeBadPercentage.ToDecimal().GreaterThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("DowntimeBadPercentage must be between 0 and 1, got: %s", vp.DowntimeBadPercentage.String())
	}

	if vp.DowntimeGoodPercentage == nil {
		return fmt.Errorf("DowntimeGoodPercentage is nil")
	}
	if vp.DowntimeGoodPercentage.ToDecimal().LessThan(decimal.Zero) || vp.DowntimeGoodPercentage.ToDecimal().GreaterThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("DowntimeGoodPercentage must be between 0 and 1, got: %s", vp.DowntimeGoodPercentage.String())
	}

	precomputed := &types.SPRTPrecomputedValues{
		InvalidationLogFail: types.DecimalFromDecimal(CalculateLogLLR(vp.BadParticipantInvalidationRate.ToDecimal(), vp.FalsePositiveRate.ToDecimal(), true)),
		InvalidationLogPass: types.DecimalFromDecimal(CalculateLogLLR(vp.BadParticipantInvalidationRate.ToDecimal(), vp.FalsePositiveRate.ToDecimal(), false)),
		InactiveLogFail:     types.DecimalFromDecimal(CalculateLogLLR(vp.DowntimeBadPercentage.ToDecimal(), vp.DowntimeGoodPercentage.ToDecimal(), true)),
		InactiveLogPass:     types.DecimalFromDecimal(CalculateLogLLR(vp.DowntimeBadPercentage.ToDecimal(), vp.DowntimeGoodPercentage.ToDecimal(), false)),
	}

	bz, err := precomputed.Marshal()
	if err != nil {
		return err
	}

	transientStore := k.transientStoreService.OpenTransientStore(ctx)
	return transientStore.Set(types.TransientSPRTValuesKey, bz)
}

// In the rare case of some kind of error or not finding this, default to zero
// This effectively turns off SPRT tracking, meaning no one will be removed from the network
// while we figure out what has gone wrong. It's the best alternative to an unlikely
// situation.
var zeroSprtValues = types.SPRTPrecomputedValues{
	InactiveLogFail:     &types.DecimalZero,
	InactiveLogPass:     &types.DecimalZero,
	InvalidationLogFail: &types.DecimalZero,
	InvalidationLogPass: &types.DecimalZero,
}

// GetPrecomputedSPRTValues retrieves the precomputed SPRT values from the transient store.
func (k Keeper) GetPrecomputedSPRTValues(ctx context.Context) types.SPRTPrecomputedValues {
	transientStore := k.transientStoreService.OpenTransientStore(ctx)
	bz, err := transientStore.Get(types.TransientSPRTValuesKey)
	if err != nil || len(bz) == 0 {
		k.LogError("Failed to get SPRT precomputed values from transient store", types.Validation, "error", err)
		return zeroSprtValues
	}

	var precomputed types.SPRTPrecomputedValues
	if err := precomputed.Unmarshal(bz); err != nil {
		k.LogError("Failed to unmarshal SPRT precomputed values from transient store", types.Validation, "error", err)
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
