package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type bridgeCancelOptions struct {
	isGovernance            bool
	reason                  string
	overrideRecipient       string
	overrideWrappedContract string
}

func (k msgServer) CancelBridgeOperation(goCtx context.Context, msg *types.MsgCancelBridgeOperation) (*types.MsgCancelBridgeOperationResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := k.cancelBridgeOperation(ctx, msg.RequestId, msg.Creator, bridgeCancelOptions{}); err != nil {
		return nil, err
	}

	return &types.MsgCancelBridgeOperationResponse{}, nil
}

func (k msgServer) cancelBridgeOperation(
	ctx sdk.Context,
	requestID string,
	executor string,
	options bridgeCancelOptions,
) error {
	requestIDHash := keccak256Hash([]byte(requestID))
	requestKey := hex.EncodeToString(requestIDHash[:])

	pendingMint, err := k.BridgeMintRefundsMap.Get(ctx, requestKey)
	if err == nil {
		return k.cancelPendingBridgeMint(ctx, requestIDHash[:], requestID, requestKey, pendingMint, executor, options)
	}
	if !errors.Is(err, collections.ErrNotFound) {
		return fmt.Errorf("failed to load pending bridge mint: %w", err)
	}

	pendingWithdrawal, err := k.BridgeWithdrawalRefundsMap.Get(ctx, requestKey)
	if err == nil {
		return k.cancelPendingBridgeWithdrawal(ctx, requestIDHash[:], requestID, requestKey, pendingWithdrawal, executor, options)
	}
	if !errors.Is(err, collections.ErrNotFound) {
		return fmt.Errorf("failed to load pending bridge withdrawal: %w", err)
	}

	return fmt.Errorf("pending bridge operation not found for request_id: %s", requestID)
}

func (k msgServer) cancelPendingBridgeMint(
	ctx sdk.Context,
	blsRequestID []byte,
	requestID string,
	requestKey string,
	pendingMint types.MsgRequestBridgeMint,
	executor string,
	options bridgeCancelOptions,
) error {
	if !options.isGovernance && pendingMint.Creator != executor {
		return fmt.Errorf("creator mismatch for pending bridge mint request")
	}

	refundRecipient := pendingMint.Creator
	if options.isGovernance && options.overrideRecipient != "" {
		refundRecipient = options.overrideRecipient
	}

	if err := k.cancelThresholdSigningRequest(ctx, blsRequestID); err != nil {
		return fmt.Errorf("failed to cancel threshold signing request: %w", err)
	}

	refundRequest := pendingMint
	refundRequest.Creator = refundRecipient

	if err := k.refundPendingBridgeMintFromEscrow(ctx, &refundRequest); err != nil {
		return fmt.Errorf("failed to refund pending bridge mint request: %w", err)
	}
	if err := k.BridgeMintRefundsMap.Remove(ctx, requestKey); err != nil {
		return fmt.Errorf("failed to cleanup pending bridge mint request: %w", err)
	}

	k.emitBridgeOperationCancelledEvent(ctx, requestID, executor, "mint", options, refundRecipient, "")
	return nil
}

func (k msgServer) cancelPendingBridgeWithdrawal(
	ctx sdk.Context,
	blsRequestID []byte,
	requestID string,
	requestKey string,
	pendingWithdrawal types.MsgRequestBridgeWithdrawal,
	executor string,
	options bridgeCancelOptions,
) error {
	if !options.isGovernance &&
		pendingWithdrawal.Creator != executor &&
		pendingWithdrawal.UserAddress != executor {
		return fmt.Errorf("creator mismatch for pending bridge withdrawal request")
	}

	refundRecipient := pendingWithdrawal.UserAddress
	if options.isGovernance && options.overrideRecipient != "" {
		refundRecipient = options.overrideRecipient
	}

	originalReference, resolveErr := k.getPendingWithdrawalTokenReference(ctx, requestKey, &pendingWithdrawal)
	if resolveErr != nil {
		return fmt.Errorf("failed to resolve pending withdrawal token reference: %w", resolveErr)
	}

	resolvedWrappedContract, resolveErr := k.resolveActiveWrappedContractByTokenReference(ctx, originalReference)
	if resolveErr != nil {
		return fmt.Errorf("failed to resolve wrapped token contract for refund: %w", resolveErr)
	}

	refundWrappedContract := resolvedWrappedContract
	if options.isGovernance && options.overrideWrappedContract != "" {
		overrideReference, overrideResolvedWrappedContract, overrideErr := k.resolveActiveWrappedContract(ctx, options.overrideWrappedContract)
		if overrideErr != nil {
			return fmt.Errorf("failed to resolve override wrapped token contract: %w", overrideErr)
		}

		if !strings.EqualFold(originalReference.ChainId, overrideReference.ChainId) ||
			!strings.EqualFold(originalReference.ContractAddress, overrideReference.ContractAddress) {
			return fmt.Errorf("override wrapped contract does not match pending withdrawal token reference")
		}

		refundWrappedContract = overrideResolvedWrappedContract
	}

	if err := k.cancelThresholdSigningRequest(ctx, blsRequestID); err != nil {
		return fmt.Errorf("failed to cancel threshold signing request: %w", err)
	}

	refundRequest := pendingWithdrawal
	refundRequest.Creator = refundWrappedContract
	refundRequest.UserAddress = refundRecipient

	if err := k.refundPendingBridgeWithdrawalByMint(ctx, &refundRequest); err != nil {
		return fmt.Errorf("failed to refund pending bridge withdrawal request: %w", err)
	}
	if err := k.BridgeWithdrawalRefundsMap.Remove(ctx, requestKey); err != nil {
		return fmt.Errorf("failed to cleanup pending bridge withdrawal request: %w", err)
	}
	if err := k.BridgeWithdrawalTokenRefsMap.Remove(ctx, requestKey); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return fmt.Errorf("failed to cleanup pending bridge withdrawal token reference: %w", err)
	}

	k.emitBridgeOperationCancelledEvent(ctx, requestID, executor, "withdrawal", options, refundRecipient, refundWrappedContract)
	return nil
}

func (k msgServer) resolveActiveWrappedContract(
	ctx sdk.Context,
	wrappedContract string,
) (types.BridgeTokenReference, string, error) {
	reference, err := k.WrappedContractReverseIndex.Get(ctx, strings.ToLower(wrappedContract))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.BridgeTokenReference{}, "", fmt.Errorf("wrapped token contract %q is not registered", wrappedContract)
		}
		return types.BridgeTokenReference{}, "", fmt.Errorf("failed to load wrapped token contract reference: %w", err)
	}

	resolvedWrappedContract, err := k.resolveActiveWrappedContractByTokenReference(ctx, reference)
	if err != nil {
		return types.BridgeTokenReference{}, "", err
	}

	return reference, resolvedWrappedContract, nil
}

func (k msgServer) emitBridgeOperationCancelledEvent(
	ctx sdk.Context,
	requestID string,
	executor string,
	operationType string,
	options bridgeCancelOptions,
	refundRecipient string,
	refundWrappedContract string,
) {
	attributes := []sdk.Attribute{
		sdk.NewAttribute("request_id", requestID),
		sdk.NewAttribute("creator", executor),
		sdk.NewAttribute("operation_type", operationType),
	}

	if options.isGovernance {
		attributes = append(attributes, sdk.NewAttribute("cancel_mode", "governance"))
	} else {
		attributes = append(attributes, sdk.NewAttribute("cancel_mode", "user"))
	}
	if refundRecipient != "" {
		attributes = append(attributes, sdk.NewAttribute("refund_recipient", refundRecipient))
	}
	if refundWrappedContract != "" {
		attributes = append(attributes, sdk.NewAttribute("refund_wrapped_contract", refundWrappedContract))
	}
	if options.reason != "" {
		attributes = append(attributes, sdk.NewAttribute("reason", options.reason))
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent("bridge_operation_cancelled", attributes...))
}
