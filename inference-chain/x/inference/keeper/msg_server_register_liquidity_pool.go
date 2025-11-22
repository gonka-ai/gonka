package keeper

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	errorsmod "cosmossdk.io/errors"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/types"
)

// RegisterLiquidityPool handles the registration of a singleton liquidity pool.
// This operation instantiates a new contract and registers it atomically.
// Only the authorized governance account can perform this registration.
func (k msgServer) RegisterLiquidityPool(goCtx context.Context, msg *types.MsgRegisterLiquidityPool) (*types.MsgRegisterLiquidityPoolResponse, error) {
	// Validate authority explicitly and consistently
	expectedAuthority := k.GetAuthority()
	if strings.TrimSpace(msg.Authority) == "" {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "authority cannot be empty")
	}
	if msg.Authority != expectedAuthority {
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "unauthorized: expected authority %s, got %s", expectedAuthority, msg.Authority)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// Validate message and propagate errors with context
	if err := validateRegisterLiquidityPoolMsg(msg); err != nil {
		return nil, errorsmod.Wrap(err, "failed to validate RegisterLiquidityPool message")
	}

	// Perform atomic check and set to avoid race conditions
	if k.LiquidityPoolExists(ctx) {
		k.LogInfo("Failed liquidity pool registration attempt: pool already registered", types.System,
			"authority", msg.Authority,
			"label", msg.Label,
			"block_height", ctx.BlockHeight(),
		)
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "registration failed: a liquidity pool is already registered")
	}

	// Convert codeId from string to uint64
	codeId, err := strconv.ParseUint(msg.CodeId, 10, 64)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "invalid code ID format: %v", err)
	}

	// Get WASM keeper for contract instantiation
	wasmKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.getWasmKeeper())

	authorityAddr, err := sdk.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid authority address: %v", err)
	}

	var instantiateMsgRaw json.RawMessage
	if err := json.Unmarshal([]byte(msg.InstantiateMsg), &instantiateMsgRaw); err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "invalid instantiate message JSON: %v", err)
	}

	contractAddress, _, err := wasmKeeper.Instantiate(
		ctx,
		codeId,
		authorityAddr,
		authorityAddr,
		wasmtypes.RawContractMessage(instantiateMsgRaw),
		msg.Label,
		sdk.NewCoins(),
	)
	if err != nil {
		return nil, errorsmod.Wrapf(err, "failed to instantiate liquidity pool contract")
	}

	k.LogInfo("Successfully instantiated liquidity pool contract", types.System,
		"authority", msg.Authority,
		"code_id", codeId,
		"label", msg.Label,
		"contract_address", contractAddress.String(),
		"block_height", ctx.BlockHeight(),
	)

	// Create and store the liquidity pool
	pool := types.LiquidityPool{
		Address:     contractAddress.String(),
		CodeId:      codeId,
		BlockHeight: uint64(ctx.BlockHeight()),
	}

	k.SetLiquidityPool(ctx, pool)

	k.LogInfo("Successfully registered liquidity pool", types.System,
		"authority", msg.Authority,
		"address", pool.Address,
		"code_id", pool.CodeId,
		"block_height", pool.BlockHeight,
	)

	return &types.MsgRegisterLiquidityPoolResponse{}, nil
}

func validateRegisterLiquidityPoolMsg(msg *types.MsgRegisterLiquidityPool) error {
	// Validate authority
	if strings.TrimSpace(msg.Authority) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "authority cannot be empty")
	}

	// Validate code ID
	if strings.TrimSpace(msg.CodeId) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "code ID cannot be empty")
	}

	// Validate code ID is a valid number
	_, err := strconv.ParseUint(msg.CodeId, 10, 64)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "invalid code ID format: must be a positive integer, got %s", msg.CodeId)
	}

	// Validate label
	if strings.TrimSpace(msg.Label) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "label cannot be empty")
	}

	// Validate instantiate message is valid JSON
	if strings.TrimSpace(msg.InstantiateMsg) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "instantiate message cannot be empty")
	}

	var temp interface{}
	if err := json.Unmarshal([]byte(msg.InstantiateMsg), &temp); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "instantiate message must be valid JSON: %v", err)
	}

	return nil
}
