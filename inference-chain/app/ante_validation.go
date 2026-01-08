package app

import (
	"slices"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// ValidationEarlyRejectDecorator performs cheap, read-only checks during CheckTx
// to reject MsgValidation transactions that will fail anyway:
//   - duplicate validation by the same participant (ErrDuplicateValidation)
//   - validator not in current epoch's model subgroup (ErrParticipantNotFound)
//
// Note: We intentionally only run this during CheckTx (mempool admission). DeliverTx
// will still enforce these rules inside the Msg handler.
type ValidationEarlyRejectDecorator struct {
	inferenceKeeper *inferencemodulekeeper.Keeper
}

func NewValidationEarlyRejectDecorator(ik *inferencemodulekeeper.Keeper) ValidationEarlyRejectDecorator {
	return ValidationEarlyRejectDecorator{inferenceKeeper: ik}
}

func (d ValidationEarlyRejectDecorator) checkValidationMsg(ctx sdk.Context, msg *inferencetypes.MsgValidation) error {
	if d.inferenceKeeper == nil {
		return nil
	}

	inference, found := d.inferenceKeeper.GetInference(ctx, msg.InferenceId)
	if !found {
		d.inferenceKeeper.LogDebug(
			"AnteHandle: ValidationEarlyReject - inference not found",
			inferencetypes.Validation,
			"creator", msg.Creator,
			"inferenceId", msg.InferenceId,
		)
		// It may filter legit transaction if the node is behind (node lag / state sync),
		// But hope that it will be propogated by other nodes
		// TODO: In the next release, skip the filter on CheckTx, and enforce only on DeliverTx.
		return inferencetypes.ErrInferenceNotFound
	}

	// Validate membership in the *current effective epoch* model subgroup.
	effectiveEpochIndex, ok := d.inferenceKeeper.GetEffectiveEpochIndex(ctx)
	if !ok {
		d.inferenceKeeper.LogDebug(
			"AnteHandle: ValidationEarlyReject - no effective epoch index",
			inferencetypes.Validation,
			"creator", msg.Creator,
			"inferenceId", msg.InferenceId,
		)
		return inferencetypes.ErrEffectiveEpochNotFound
	}

	groupData, found := d.inferenceKeeper.GetEpochGroupData(ctx, effectiveEpochIndex, inference.Model)
	if !found {
		d.inferenceKeeper.LogDebug(
			"AnteHandle: ValidationEarlyReject - epoch group data not found",
			inferencetypes.Validation,
			"creator", msg.Creator,
			"inferenceId", msg.InferenceId,
			"modelId", inference.Model,
			"epochIndex", effectiveEpochIndex,
		)
		return inferencetypes.ErrEpochGroupDataNotFound
	}

	if groupData.ValidationWeight(msg.Creator) == nil {
		d.inferenceKeeper.LogDebug(
			"AnteHandle: ValidationEarlyReject - participant not in current epoch group for model",
			inferencetypes.Validation,
			"creator", msg.Creator,
			"inferenceId", msg.InferenceId,
			"modelId", inference.Model,
			"epochIndex", effectiveEpochIndex,
		)
		return inferencetypes.ErrParticipantNotFound
	}

	// Duplicate detection is only relevant for non-revalidation flows.
	if !msg.Revalidation {
		egv, found := d.inferenceKeeper.GetEpochGroupValidations(ctx, msg.Creator, inference.EpochId)
		if found {
			// Msg handler maintains this slice sorted, but we intentionally do not rely on that here.
			if slices.Contains(egv.ValidatedInferences, msg.InferenceId) {
				d.inferenceKeeper.LogDebug(
					"AnteHandle: ValidationEarlyReject - duplicate validation",
					inferencetypes.Validation,
					"creator", msg.Creator,
					"inferenceId", msg.InferenceId,
					"inferenceEpochId", inference.EpochId,
				)
				return inferencetypes.ErrDuplicateValidation
			}
		}
	}

	return nil
}

func (d ValidationEarlyRejectDecorator) checkMessage(ctx sdk.Context, msg sdk.Msg) error {
	switch m := msg.(type) {
	case *inferencetypes.MsgValidation:
		return d.checkValidationMsg(ctx, m)

	case *authztypes.MsgExec:
		if d.inferenceKeeper == nil {
			return nil
		}
		for _, innerMsg := range m.Msgs {
			var unwrapped sdk.Msg
			if err := d.inferenceKeeper.Codec().UnpackAny(innerMsg, &unwrapped); err != nil {
				d.inferenceKeeper.LogDebug(
					"AnteHandle: ValidationEarlyReject - failed to unpack authz MsgExec inner msg",
					inferencetypes.Validation,
					"error", err,
				)
				continue
			}
			if err := d.checkMessage(ctx, unwrapped); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d ValidationEarlyRejectDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if simulate {
		return next(ctx, tx, simulate)
	}

	// Only perform validation during CheckTx (including ReCheckTx).
	if !ctx.IsCheckTx() {
		return next(ctx, tx, simulate)
	}

	for _, msg := range tx.GetMsgs() {
		if err := d.checkMessage(ctx, msg); err != nil {
			return ctx, err
		}
	}

	return next(ctx, tx, simulate)
}
