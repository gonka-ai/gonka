package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

type rollingWindowState struct {
	Values []uint64
	Sum    uint64
}

func (k Keeper) UpdateModelRollingWindows(
	ctx context.Context,
	groupData *types.EpochGroupData,
	params *types.Params,
	modelBlockLoads map[string]uint64,
	modelBlockInferenceCounts map[string]uint64,
) error {
	if groupData == nil {
		return nil
	}

	utilizationWindowSeconds := uint64(0)
	invalidationsSamplePeriodSeconds := uint64(0)
	if params != nil {
		if params.DynamicPricingParams != nil {
			utilizationWindowSeconds = params.DynamicPricingParams.UtilizationWindowDuration
		}
		if params.BandwidthLimitsParams != nil {
			invalidationsSamplePeriodSeconds = params.BandwidthLimitsParams.InvalidationsSamplePeriod
		}
	}

	return k.UpdateModelRollingWindowsForActiveModels(
		ctx,
		groupData.SubGroupModels,
		modelBlockLoads,
		utilizationWindowSeconds,
		modelBlockInferenceCounts,
		invalidationsSamplePeriodSeconds,
	)
}

func (k Keeper) UpdateModelRollingWindowsForActiveModels(
	ctx context.Context,
	activeModels []string,
	modelBlockLoads map[string]uint64,
	utilizationWindowSeconds uint64,
	modelBlockInferenceCounts map[string]uint64,
	invalidationsSamplePeriodSeconds uint64,
) error {
	loadWindowBlocks := types.UtilizationWindowToBlocks(utilizationWindowSeconds)
	inferenceCountWindowBlocks := types.InvalidationsSamplePeriodToBlocks(invalidationsSamplePeriodSeconds)

	activeSet := make(map[string]struct{}, len(activeModels))
	for _, modelID := range activeModels {
		activeSet[modelID] = struct{}{}

		if err := k.updateModelRollingWindowState(
			ctx,
			k.ModelLoadRollingWindowMap,
			modelID,
			loadWindowBlocks,
			modelBlockLoads[modelID],
		); err != nil {
			return fmt.Errorf("update model load rolling window for %s: %w", modelID, err)
		}

		if err := k.updateModelRollingWindowState(
			ctx,
			k.ModelInferenceCountRollingWindowMap,
			modelID,
			inferenceCountWindowBlocks,
			modelBlockInferenceCounts[modelID],
		); err != nil {
			return fmt.Errorf("update model inference-count rolling window for %s: %w", modelID, err)
		}
	}

	if err := k.removeInactiveModelRollingStates(ctx, k.ModelLoadRollingWindowMap, activeSet, "load"); err != nil {
		return err
	}
	if err := k.removeInactiveModelRollingStates(ctx, k.ModelInferenceCountRollingWindowMap, activeSet, "inference_count"); err != nil {
		return err
	}

	return nil
}

func (k Keeper) updateModelRollingWindowState(
	ctx context.Context,
	stateMap collections.Map[string, types.RollingWindowState],
	modelID string,
	windowBlocks uint64,
	newValue uint64,
) error {
	state, found, err := k.getModelRollingWindowState(ctx, stateMap, modelID)
	if err != nil {
		return err
	}
	if !found {
		state = rollingWindowState{}
	}

	state = state.normalize(types.WindowBlocksToSize(windowBlocks))
	state = state.push(newValue)
	return k.setModelRollingWindowState(ctx, stateMap, modelID, state)
}

func (k Keeper) GetModelLoadRollingAveragePerBlock(ctx context.Context, modelID string, windowBlocks uint64) (decimal.Decimal, bool, error) {
	state, found, err := k.getModelRollingWindowState(ctx, k.ModelLoadRollingWindowMap, modelID)
	if err != nil {
		return decimal.Zero, false, err
	}
	if !found {
		return decimal.Zero, false, nil
	}

	state = state.normalize(types.WindowBlocksToSize(windowBlocks))
	if len(state.Values) == 0 {
		return decimal.Zero, true, nil
	}

	return decimal.NewFromUint64(state.Sum).Div(decimal.NewFromInt(int64(len(state.Values)))), true, nil
}

func (k Keeper) GetModelInferenceCountRollingSum(ctx context.Context, modelID string, windowBlocks uint64) (uint64, bool, error) {
	state, found, err := k.getModelRollingWindowState(ctx, k.ModelInferenceCountRollingWindowMap, modelID)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, nil
	}

	state = state.normalize(types.WindowBlocksToSize(windowBlocks))
	return state.Sum, true, nil
}

func (k Keeper) getModelRollingWindowState(
	ctx context.Context,
	stateMap collections.Map[string, types.RollingWindowState],
	modelID string,
) (rollingWindowState, bool, error) {
	stateValue, err := stateMap.Get(ctx, modelID)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return rollingWindowState{}, false, nil
		}
		return rollingWindowState{}, false, err
	}
	return rollingWindowStateFromProto(stateValue), true, nil
}

func (k Keeper) setModelRollingWindowState(
	ctx context.Context,
	stateMap collections.Map[string, types.RollingWindowState],
	modelID string,
	state rollingWindowState,
) error {
	return stateMap.Set(ctx, modelID, state.toProto())
}

func (k Keeper) removeInactiveModelRollingStates(
	ctx context.Context,
	stateMap collections.Map[string, types.RollingWindowState],
	activeSet map[string]struct{},
	stateName string,
) error {
	staleModels := make([]string, 0)
	if err := stateMap.Walk(ctx, nil, func(modelID string, _ types.RollingWindowState) (bool, error) {
		if _, ok := activeSet[modelID]; !ok {
			staleModels = append(staleModels, modelID)
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("iterate %s rolling state map: %w", stateName, err)
	}

	for _, modelID := range staleModels {
		if err := stateMap.Remove(ctx, modelID); err != nil {
			return fmt.Errorf("remove stale %s rolling state for %s: %w", stateName, modelID, err)
		}
	}
	return nil
}

func (s rollingWindowState) normalize(targetWindow int64) rollingWindowState {
	if targetWindow < 1 {
		targetWindow = 1
	}

	values := append([]uint64(nil), s.Values...)
	targetWindowInt := int(targetWindow)
	switch {
	case len(values) < targetWindowInt:
		padded := make([]uint64, targetWindowInt-len(values))
		values = append(padded, values...)
	case len(values) > targetWindowInt:
		values = values[len(values)-targetWindowInt:]
	}

	sum := uint64(0)
	for _, v := range values {
		sum += v
	}
	return rollingWindowState{
		Values: values,
		Sum:    sum,
	}
}

func (s rollingWindowState) push(newValue uint64) rollingWindowState {
	if len(s.Values) == 0 {
		return rollingWindowState{
			Values: []uint64{newValue},
			Sum:    newValue,
		}
	}

	oldest := s.Values[0]
	if oldest > s.Sum {
		s.Sum = 0
	} else {
		s.Sum -= oldest
	}

	copy(s.Values, s.Values[1:])
	s.Values[len(s.Values)-1] = newValue
	s.Sum += newValue

	return s
}

func rollingWindowStateFromProto(state types.RollingWindowState) rollingWindowState {
	return rollingWindowState{
		Values: append([]uint64(nil), state.Values...),
		Sum:    state.Sum,
	}.normalize(int64(len(state.Values)))
}

func (s rollingWindowState) toProto() types.RollingWindowState {
	return types.RollingWindowState{
		Values: append([]uint64(nil), s.Values...),
		Sum:    s.Sum,
	}
}
