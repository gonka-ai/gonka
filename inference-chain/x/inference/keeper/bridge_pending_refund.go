package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) setBridgeMintPendingRefund(ctx context.Context, blsRequestID []byte, msg *types.MsgRequestBridgeMint) error {
	if len(blsRequestID) == 0 {
		return fmt.Errorf("bls request id cannot be empty")
	}
	if msg == nil {
		return fmt.Errorf("bridge mint message cannot be nil")
	}

	requestKey := hex.EncodeToString(blsRequestID)
	exists, err := k.BridgeMintRefundsMap.Has(ctx, requestKey)
	if err != nil {
		return fmt.Errorf("failed to check pending bridge mint refund %s: %w", requestKey, err)
	}
	if exists {
		return fmt.Errorf("pending bridge mint refund already exists for request id: %s", requestKey)
	}

	return k.BridgeMintRefundsMap.Set(ctx, requestKey, *msg)
}

func (k Keeper) setBridgeWithdrawalPendingRefund(
	ctx context.Context,
	blsRequestID []byte,
	msg *types.MsgRequestBridgeWithdrawal,
	tokenReference types.BridgeTokenReference,
) error {
	if len(blsRequestID) == 0 {
		return fmt.Errorf("bls request id cannot be empty")
	}
	if msg == nil {
		return fmt.Errorf("bridge withdrawal message cannot be nil")
	}
	if tokenReference.ChainId == "" {
		return fmt.Errorf("bridge withdrawal token reference chain id cannot be empty")
	}
	if tokenReference.ContractAddress == "" {
		return fmt.Errorf("bridge withdrawal token reference contract address cannot be empty")
	}

	requestKey := hex.EncodeToString(blsRequestID)
	exists, err := k.BridgeWithdrawalRefundsMap.Has(ctx, requestKey)
	if err != nil {
		return fmt.Errorf("failed to check pending bridge withdrawal refund %s: %w", requestKey, err)
	}
	if exists {
		return fmt.Errorf("pending bridge withdrawal refund already exists for request id: %s", requestKey)
	}
	tokenRefExists, err := k.BridgeWithdrawalTokenRefsMap.Has(ctx, requestKey)
	if err != nil {
		return fmt.Errorf("failed to check pending bridge withdrawal token reference %s: %w", requestKey, err)
	}
	if tokenRefExists {
		return fmt.Errorf("pending bridge withdrawal token reference already exists for request id: %s", requestKey)
	}

	if err := k.BridgeWithdrawalRefundsMap.Set(ctx, requestKey, *msg); err != nil {
		return err
	}
	if err := k.BridgeWithdrawalTokenRefsMap.Set(ctx, requestKey, tokenReference); err != nil {
		rollbackErr := k.BridgeWithdrawalRefundsMap.Remove(ctx, requestKey)
		if rollbackErr != nil {
			return fmt.Errorf("failed to persist pending bridge withdrawal token reference %s: %w (rollback failed: %v)", requestKey, err, rollbackErr)
		}
		return fmt.Errorf("failed to persist pending bridge withdrawal token reference %s: %w", requestKey, err)
	}

	return nil
}

func (k Keeper) cancelThresholdSigningRequest(ctx sdk.Context, requestID []byte) error {
	request, err := k.BlsKeeper.GetSigningStatus(ctx, requestID)
	if err != nil {
		return err
	}
	if request.Status == blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED {
		return fmt.Errorf("threshold signing already completed")
	}
	return k.BlsKeeper.CancelThresholdSignature(ctx, requestID)
}

func (k Keeper) refundPendingBridgeMintFromEscrow(ctx sdk.Context, pendingMint *types.MsgRequestBridgeMint) error {
	if pendingMint == nil {
		return fmt.Errorf("pending bridge mint request is nil")
	}

	recipientAddr, err := sdk.AccAddressFromBech32(pendingMint.Creator)
	if err != nil {
		return fmt.Errorf("invalid bridge mint creator address %q: %w", pendingMint.Creator, err)
	}

	amountInt, ok := math.NewIntFromString(pendingMint.Amount)
	if !ok || !amountInt.IsPositive() {
		return fmt.Errorf("invalid bridge mint amount %q", pendingMint.Amount)
	}
	refundCoins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, amountInt))

	return k.ReleaseFromEscrow(ctx, recipientAddr, refundCoins)
}

func (k Keeper) refundPendingBridgeWithdrawalByMint(ctx sdk.Context, pendingWithdrawal *types.MsgRequestBridgeWithdrawal) error {
	if pendingWithdrawal == nil {
		return fmt.Errorf("pending bridge withdrawal request is nil")
	}

	recipientAddr, err := sdk.AccAddressFromBech32(pendingWithdrawal.UserAddress)
	if err != nil {
		return fmt.Errorf("invalid bridge withdrawal user address %q: %w", pendingWithdrawal.UserAddress, err)
	}

	amountInt, ok := math.NewIntFromString(pendingWithdrawal.Amount)
	if !ok || !amountInt.IsPositive() {
		return fmt.Errorf("invalid bridge withdrawal amount %q", pendingWithdrawal.Amount)
	}
	if pendingWithdrawal.Creator == "" {
		return fmt.Errorf("bridge withdrawal wrapped contract cannot be empty")
	}

	if k.mintTokensFn != nil {
		return k.mintTokensFn(ctx, pendingWithdrawal.Creator, recipientAddr.String(), pendingWithdrawal.Amount)
	}

	return k.MintTokens(ctx, pendingWithdrawal.Creator, recipientAddr.String(), pendingWithdrawal.Amount)
}

func (k Keeper) getPendingWithdrawalTokenReference(
	ctx context.Context,
	requestKey string,
	pendingWithdrawal *types.MsgRequestBridgeWithdrawal,
) (types.BridgeTokenReference, error) {
	reference, err := k.BridgeWithdrawalTokenRefsMap.Get(ctx, requestKey)
	if err == nil {
		if reference.ChainId == "" || reference.ContractAddress == "" {
			return types.BridgeTokenReference{}, fmt.Errorf("pending bridge withdrawal token reference is incomplete for request id: %s", requestKey)
		}
		return reference, nil
	}
	if !errors.Is(err, collections.ErrNotFound) {
		return types.BridgeTokenReference{}, fmt.Errorf("failed to load pending bridge withdrawal token reference %s: %w", requestKey, err)
	}

	if pendingWithdrawal == nil {
		return types.BridgeTokenReference{}, fmt.Errorf("pending bridge withdrawal request is nil")
	}

	legacyReference, legacyErr := k.WrappedContractReverseIndex.Get(ctx, strings.ToLower(pendingWithdrawal.Creator))
	if legacyErr != nil {
		if errors.Is(legacyErr, collections.ErrNotFound) {
			return types.BridgeTokenReference{}, fmt.Errorf("pending bridge withdrawal token reference not found for request id %s: %w", requestKey, collections.ErrNotFound)
		}
		return types.BridgeTokenReference{}, fmt.Errorf("failed to load legacy bridge withdrawal token reference %s: %w", requestKey, legacyErr)
	}

	if legacyReference.ChainId == "" || legacyReference.ContractAddress == "" {
		return types.BridgeTokenReference{}, fmt.Errorf("legacy bridge withdrawal token reference is incomplete for request id: %s", requestKey)
	}

	return legacyReference, nil
}

func (k Keeper) resolveActiveWrappedContractByTokenReference(
	ctx sdk.Context,
	tokenReference types.BridgeTokenReference,
) (string, error) {
	contract, found := k.GetWrappedTokenContract(ctx, tokenReference.ChainId, tokenReference.ContractAddress)
	if !found {
		return "", fmt.Errorf("active wrapped token contract not found for chain %s contract %s", tokenReference.ChainId, tokenReference.ContractAddress)
	}
	return contract.WrappedContractAddress, nil
}

func (k Keeper) ProcessAutoRefundForFailedBridgeOperation(ctx context.Context, blsRequestID []byte, reason string) (bool, error) {
	if len(blsRequestID) == 0 {
		return false, fmt.Errorf("bls request id cannot be empty")
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	requestKey := hex.EncodeToString(blsRequestID)

	pendingMint, err := k.BridgeMintRefundsMap.Get(ctx, requestKey)
	switch {
	case err == nil:
		if err := k.processAutoRefundMint(sdkCtx, requestKey, pendingMint, reason); err != nil {
			return false, err
		}
		return true, nil
	case !errors.Is(err, collections.ErrNotFound):
		return false, fmt.Errorf("failed to load pending bridge mint request %s for auto-refund: %w", requestKey, err)
	}

	pendingWithdrawal, err := k.BridgeWithdrawalRefundsMap.Get(ctx, requestKey)
	switch {
	case err == nil:
		if err := k.processAutoRefundWithdrawal(sdkCtx, requestKey, pendingWithdrawal, reason); err != nil {
			return false, err
		}
		return true, nil
	case !errors.Is(err, collections.ErrNotFound):
		return false, fmt.Errorf("failed to load pending bridge withdrawal request %s for auto-refund: %w", requestKey, err)
	}

	return false, nil
}

func (k Keeper) processAutoRefundMint(
	ctx sdk.Context,
	requestKey string,
	pendingMint types.MsgRequestBridgeMint,
	reason string,
) error {
	if err := k.refundPendingBridgeMintFromEscrow(ctx, &pendingMint); err != nil {
		return fmt.Errorf("failed to auto-refund pending bridge mint request %s: %w", requestKey, err)
	}
	if err := k.BridgeMintRefundsMap.Remove(ctx, requestKey); err != nil {
		return fmt.Errorf("failed to cleanup pending bridge mint request %s after auto-refund: %w", requestKey, err)
	}

	emitBridgeAutoRefundEvent(ctx, requestKey, "mint", reason)
	return nil
}

func (k Keeper) processAutoRefundWithdrawal(
	ctx sdk.Context,
	requestKey string,
	pendingWithdrawal types.MsgRequestBridgeWithdrawal,
	reason string,
) error {
	tokenReference, err := k.getPendingWithdrawalTokenReference(ctx, requestKey, &pendingWithdrawal)
	if err != nil {
		return fmt.Errorf("failed to resolve pending bridge withdrawal token reference %s: %w", requestKey, err)
	}
	refundWrappedContract, err := k.resolveActiveWrappedContractByTokenReference(ctx, tokenReference)
	if err != nil {
		return fmt.Errorf("failed to resolve wrapped token contract for pending bridge withdrawal request %s: %w", requestKey, err)
	}

	refundRequest := pendingWithdrawal
	refundRequest.Creator = refundWrappedContract

	if err := k.refundPendingBridgeWithdrawalByMint(ctx, &refundRequest); err != nil {
		return fmt.Errorf("failed to auto-refund pending bridge withdrawal request %s: %w", requestKey, err)
	}
	if err := k.BridgeWithdrawalRefundsMap.Remove(ctx, requestKey); err != nil {
		return fmt.Errorf("failed to cleanup pending bridge withdrawal request %s after auto-refund: %w", requestKey, err)
	}
	if err := k.BridgeWithdrawalTokenRefsMap.Remove(ctx, requestKey); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return fmt.Errorf("failed to cleanup pending bridge withdrawal token reference %s after auto-refund: %w", requestKey, err)
	}

	emitBridgeAutoRefundEvent(ctx, requestKey, "withdrawal", reason)
	return nil
}

func emitBridgeAutoRefundEvent(ctx sdk.Context, requestKey, operationType, reason string) {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"bridge_operation_auto_refunded",
			sdk.NewAttribute("request_id", requestKey),
			sdk.NewAttribute("operation_type", operationType),
			sdk.NewAttribute("reason", reason),
		),
	)
}

func (k Keeper) GetAllBridgePendingMintRefunds(ctx context.Context) []types.BridgePendingMintRefund {
	iter, err := k.BridgeMintRefundsMap.Iterate(ctx, nil)
	if err != nil {
		k.LogError("failed to iterate bridge mint pending refunds", types.Messages, "error", err)
		return nil
	}
	defer iter.Close()

	refunds := make([]types.BridgePendingMintRefund, 0)
	for ; iter.Valid(); iter.Next() {
		key, keyErr := iter.Key()
		if keyErr != nil {
			k.LogError("failed to read bridge mint pending refund key", types.Messages, "error", keyErr)
			continue
		}
		value, valueErr := iter.Value()
		if valueErr != nil {
			k.LogError("failed to read bridge mint pending refund value", types.Messages, "request_id", key, "error", valueErr)
			continue
		}
		refunds = append(refunds, types.BridgePendingMintRefund{
			RequestId:          key,
			Creator:            value.Creator,
			Amount:             value.Amount,
			DestinationAddress: value.DestinationAddress,
			ChainId:            value.ChainId,
		})
	}
	return refunds
}

func (k Keeper) GetAllBridgePendingWithdrawalRefunds(ctx context.Context) []types.BridgePendingWithdrawalRefund {
	iter, err := k.BridgeWithdrawalRefundsMap.Iterate(ctx, nil)
	if err != nil {
		k.LogError("failed to iterate bridge withdrawal pending refunds", types.Messages, "error", err)
		return nil
	}
	defer iter.Close()

	refunds := make([]types.BridgePendingWithdrawalRefund, 0)
	for ; iter.Valid(); iter.Next() {
		key, keyErr := iter.Key()
		if keyErr != nil {
			k.LogError("failed to read bridge withdrawal pending refund key", types.Messages, "error", keyErr)
			continue
		}
		value, valueErr := iter.Value()
		if valueErr != nil {
			k.LogError("failed to read bridge withdrawal pending refund value", types.Messages, "request_id", key, "error", valueErr)
			continue
		}
		refunds = append(refunds, types.BridgePendingWithdrawalRefund{
			RequestId:          key,
			Creator:            value.Creator,
			UserAddress:        value.UserAddress,
			Amount:             value.Amount,
			DestinationAddress: value.DestinationAddress,
			ChainId:            "",
			ContractAddress:    "",
		})
		lastIdx := len(refunds) - 1
		tokenReference, tokenRefErr := k.getPendingWithdrawalTokenReference(ctx, key, &value)
		if tokenRefErr != nil {
			if !errors.Is(tokenRefErr, collections.ErrNotFound) {
				k.LogError("failed to resolve bridge withdrawal pending refund token reference", types.Messages, "request_id", key, "error", tokenRefErr)
			}
			continue
		}
		refunds[lastIdx].ChainId = tokenReference.ChainId
		refunds[lastIdx].ContractAddress = tokenReference.ContractAddress
	}
	return refunds
}

func (k Keeper) CleanupBridgePendingRefundByBlsRequestID(ctx context.Context, blsRequestID []byte) error {
	if len(blsRequestID) == 0 {
		return fmt.Errorf("bls request id cannot be empty")
	}

	requestKey := hex.EncodeToString(blsRequestID)

	if err := k.BridgeMintRefundsMap.Remove(ctx, requestKey); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return fmt.Errorf("failed to cleanup pending bridge mint request %s: %w", requestKey, err)
	}

	if err := k.BridgeWithdrawalRefundsMap.Remove(ctx, requestKey); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return fmt.Errorf("failed to cleanup pending bridge withdrawal request %s: %w", requestKey, err)
	}
	if err := k.BridgeWithdrawalTokenRefsMap.Remove(ctx, requestKey); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return fmt.Errorf("failed to cleanup pending bridge withdrawal token reference %s: %w", requestKey, err)
	}

	return nil
}
