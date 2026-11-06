package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitNewParticipant(goCtx context.Context, msg *types.MsgSubmitNewParticipant) (*types.MsgSubmitNewParticipantResponse, error) {
	if err := k.CheckPermission(goCtx, msg, OpenRegistrationPermission); err != nil {
		return nil, err
	}
	// ValidateBasic is not consensus admission. Reject a non-canonical creator
	// here, before any read or write, so a proposer that skipped CheckTx
	// cannot register or update an uppercase spelling.
	addr, err := types.CanonicalAccAddress(msg.GetCreator())
	if err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	canonical := addr.String()

	// Check if participant already exists. If it does, restrict updates to only
	// ValidatorKey, WorkerKey, and Url as per requirements.
	if existing, found := k.GetParticipant(ctx, canonical); found {
		storedStats := existing.CurrentEpochStats.StoredCopy()
		// Preserve all existing fields and update only the allowed ones
		if msg.Url != "" {
			existing.InferenceUrl = msg.Url
		}
		if msg.ValidatorKey != "" {
			existing.ValidatorKey = msg.ValidatorKey
		}
		if msg.WorkerKey != "" {
			existing.WorkerPublicKey = msg.WorkerKey
		}
		if err := k.SetParticipantFromStored(ctx, existing, storedStats); err != nil {
			return nil, err
		}
		return &types.MsgSubmitNewParticipantResponse{}, nil
	}

	// If participant does not exist yet, charge the one-time creation gas
	// before the write. An update above does not pay it.
	if err := k.ChargeStaticGas(ctx, msg); err != nil {
		return nil, err
	}
	newParticipant := createNewParticipant(ctx, msg, addr)
	if err := k.setParticipantAsRead(ctx, newParticipant, nil, false); err != nil {
		return nil, err
	}
	return &types.MsgSubmitNewParticipantResponse{}, nil
}

func createNewParticipant(ctx sdk.Context, msg *types.MsgSubmitNewParticipant, addr sdk.AccAddress) types.Participant {
	canonical := addr.String()
	newParticipant := types.Participant{
		Index:             canonical,
		Address:           canonical,
		Weight:            -1,
		JoinTime:          ctx.BlockTime().UnixMilli(),
		JoinHeight:        ctx.BlockHeight(),
		LastInferenceTime: 0,
		InferenceUrl:      msg.GetUrl(),
		Status:            types.ParticipantStatus_ACTIVE,
		ValidatorKey:      msg.GetValidatorKey(),
		WorkerPublicKey:   msg.GetWorkerKey(),
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}

	return newParticipant
}
