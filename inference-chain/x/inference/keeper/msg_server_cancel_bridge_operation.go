package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CancelBridgeOperation(goCtx context.Context, msg *types.MsgCancelBridgeOperation) (*types.MsgCancelBridgeOperationResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	requestIDHash := keccak256Hash([]byte(msg.RequestId))
	requestKey := hex.EncodeToString(requestIDHash[:])

	pendingMint, err := k.BridgeMintRefundsMap.Get(ctx, requestKey)
	if err == nil {
		return k.cancelPendingBridgeMint(ctx, msg, requestIDHash[:], requestKey, pendingMint)
	}
	if !errors.Is(err, collections.ErrNotFound) {
		return nil, fmt.Errorf("failed to load pending bridge mint: %w", err)
	}

	pendingWithdrawal, err := k.BridgeWithdrawalRefundsMap.Get(ctx, requestKey)
	if err == nil {
		return k.cancelPendingBridgeWithdrawal(ctx, msg, requestIDHash[:], requestKey, pendingWithdrawal)
	}
	if !errors.Is(err, collections.ErrNotFound) {
		return nil, fmt.Errorf("failed to load pending bridge withdrawal: %w", err)
	}

	return nil, fmt.Errorf("pending bridge operation not found for request_id: %s", msg.RequestId)
}

func (k msgServer) cancelPendingBridgeMint(
	ctx sdk.Context,
	msg *types.MsgCancelBridgeOperation,
	blsRequestID []byte,
	requestKey string,
	pendingMint types.MsgRequestBridgeMint,
) (*types.MsgCancelBridgeOperationResponse, error) {
	if pendingMint.Creator != msg.Creator {
		return nil, fmt.Errorf("creator mismatch for pending bridge mint request")
	}

	if err := k.cancelThresholdSigningRequest(ctx, blsRequestID); err != nil {
		return nil, fmt.Errorf("failed to cancel threshold signing request: %w", err)
	}
	if err := k.refundPendingBridgeMintFromEscrow(ctx, &pendingMint); err != nil {
		return nil, fmt.Errorf("failed to refund pending bridge mint request: %w", err)
	}
	if err := k.BridgeMintRefundsMap.Remove(ctx, requestKey); err != nil {
		return nil, fmt.Errorf("failed to cleanup pending bridge mint request: %w", err)
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"bridge_operation_cancelled",
			sdk.NewAttribute("request_id", msg.RequestId),
			sdk.NewAttribute("creator", msg.Creator),
			sdk.NewAttribute("operation_type", "mint"),
		),
	)

	return &types.MsgCancelBridgeOperationResponse{}, nil
}

func (k msgServer) cancelPendingBridgeWithdrawal(
	ctx sdk.Context,
	msg *types.MsgCancelBridgeOperation,
	blsRequestID []byte,
	requestKey string,
	pendingWithdrawal types.MsgRequestBridgeWithdrawal,
) (*types.MsgCancelBridgeOperationResponse, error) {
	if pendingWithdrawal.Creator != msg.Creator && pendingWithdrawal.UserAddress != msg.Creator {
		return nil, fmt.Errorf("creator mismatch for pending bridge withdrawal request")
	}

	if err := k.cancelThresholdSigningRequest(ctx, blsRequestID); err != nil {
		return nil, fmt.Errorf("failed to cancel threshold signing request: %w", err)
	}
	if err := k.refundPendingBridgeWithdrawalByMint(ctx, &pendingWithdrawal); err != nil {
		return nil, fmt.Errorf("failed to refund pending bridge withdrawal request: %w", err)
	}
	if err := k.BridgeWithdrawalRefundsMap.Remove(ctx, requestKey); err != nil {
		return nil, fmt.Errorf("failed to cleanup pending bridge withdrawal request: %w", err)
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"bridge_operation_cancelled",
			sdk.NewAttribute("request_id", msg.RequestId),
			sdk.NewAttribute("creator", msg.Creator),
			sdk.NewAttribute("operation_type", "withdrawal"),
		),
	)

	return &types.MsgCancelBridgeOperationResponse{}, nil
}
