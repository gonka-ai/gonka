package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
)

// bridgeReissueSigners records eligibility at creation, before a completion hook
// removes refund context. A later governance change cannot opt a legacy request
// into reissue. The destination's current allowlist entry is checked again on use.
func (k Keeper) bridgeReissueSigners(ctx context.Context, chainID, contract string, signers ...string) ([]string, error) {
	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	if params.BridgeReissueParams == nil {
		return nil, nil
	}
	for _, target := range params.BridgeReissueParams.AllowedTargets {
		if target.ChainId == chainID && strings.EqualFold(target.ContractAddress, contract) {
			result := make([]string, 0, len(signers))
			for _, signer := range signers {
				address, err := sdk.AccAddressFromBech32(signer)
				if err != nil {
					return nil, fmt.Errorf("invalid bridge reissue signer: %w", err)
				}
				canonical := address.String()
				if len(result) == 0 || result[0] != canonical {
					result = append(result, canonical)
				}
			}
			return result, nil
		}
	}
	return nil, nil
}

func (k msgServer) ReissueBridgeSignature(goCtx context.Context, msg *types.MsgReissueBridgeSignature) (*types.MsgReissueBridgeSignatureResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	requestID := keccak256Hash([]byte(msg.RequestId))
	request, err := k.BlsKeeper.GetSigningStatus(ctx, requestID[:])
	if err != nil {
		return nil, err
	}
	creator, _ := sdk.AccAddressFromBech32(msg.Creator)
	authorized := false
	for _, signer := range request.ReissueSigners {
		if signer == creator.String() {
			authorized = true
			break
		}
	}
	if !authorized {
		return nil, errorsmod.Wrap(sdkerrors.ErrUnauthorized, "bridge request is not eligible for reissue by this signer")
	}
	if err := k.validateBridgeReissueDestination(ctx, request); err != nil {
		return nil, err
	}
	epochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		return nil, err
	}
	epoch := epochGroup.GroupData.EpochIndex
	// The BLS keeper enforces the age/rate limits and replaces only the epoch.
	// No escrow movement, token mint/burn, or refund is permitted on this path.
	if err := k.BlsKeeper.ReissueThresholdSignature(ctx, requestID[:], epoch); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent("bridge_signature_reissued",
		sdk.NewAttribute("request_id", msg.RequestId),
		sdk.NewAttribute("bls_request_id", hex.EncodeToString(requestID[:])),
		sdk.NewAttribute("creator", creator.String()),
		sdk.NewAttribute("epoch_index", fmt.Sprint(epoch)),
	))
	return &types.MsgReissueBridgeSignatureResponse{EpochIndex: epoch}, nil
}

func (k Keeper) validateBridgeReissueDestination(ctx sdk.Context, request *blstypes.ThresholdSigningRequest) error {
	sourceChain := sha256.Sum256([]byte(ctx.ChainID()))
	if !bytes.Equal(sourceChain[:], request.ChainId) {
		return fmt.Errorf("bridge signing request belongs to another source chain")
	}
	data := request.Data
	if len(data) != 5 && len(data) != 6 {
		return fmt.Errorf("invalid bridge signing payload")
	}
	if len(data[0]) != 32 || len(data[1]) != 32 || len(data[2]) != 20 || len(data[3]) != 20 || len(data[len(data)-1]) != 32 {
		return fmt.Errorf("invalid bridge signing payload field lengths")
	}
	if (len(data) == 5 && !bytes.Equal(data[1], mintOperationHash[:])) ||
		(len(data) == 6 && (!bytes.Equal(data[1], withdrawOperationHash[:]) || len(data[4]) != 20)) {
		return fmt.Errorf("request is not a bridge mint or withdrawal")
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.BridgeReissueParams != nil {
		for _, target := range params.BridgeReissueParams.AllowedTargets {
			numericID, supported := chainIdMapping[target.ChainId]
			if !supported {
				continue
			}
			chainBytes, err := chainIdToBytes32(numericID)
			if err != nil {
				return err
			}
			address, err := ethereumAddressToBytes(target.ContractAddress)
			if err != nil {
				return err
			}
			if bytes.Equal(chainBytes, data[0]) && bytes.Equal(address, data[3]) &&
				k.IsBridgeContractAddress(ctx, target.ChainId, target.ContractAddress) {
				return nil
			}
		}
	}
	return errorsmod.Wrap(sdkerrors.ErrUnauthorized, "destination is not an enabled, registered bridge with global request-id deduplication")
}
