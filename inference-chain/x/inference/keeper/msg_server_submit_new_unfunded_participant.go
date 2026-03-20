package keeper

import (
	"context"
	"encoding/base64"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitNewUnfundedParticipant(goCtx context.Context, msg *types.MsgSubmitNewUnfundedParticipant) (*types.MsgSubmitNewUnfundedParticipantResponse, error) {
	if err := k.CheckPermission(goCtx, msg, OpenRegistrationPermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("Adding new account directly", types.Participants, "address", msg.Address)
	addr, err := sdk.AccAddressFromBech32(msg.Address)
	if err != nil {
		return nil, err
	}
	// First, add the account
	if k.AccountKeeper.GetAccount(ctx, addr) != nil {
		k.LogError("Account already exists", types.Participants, "address", msg.Address)
		return nil, types.ErrAccountAlreadyExists
	}
	newAccount := k.AccountKeeper.NewAccountWithAddress(ctx, addr)
	pubKeyBytes, err := base64.StdEncoding.DecodeString(msg.PubKey)
	if err != nil {
		return nil, err
	}
	actualKey := secp256k1.PubKey{Key: pubKeyBytes}
	expectedAddress := sdk.AccAddress(actualKey.Address())
	if msg.Address != expectedAddress.String() {
		k.LogError("Pubkey does not match address", types.Participants, "address", msg.Address, "expected", expectedAddress.String())
		return nil, types.ErrPubKeyDoesNotMatchAddress
	}
	err = newAccount.SetPubKey(&actualKey)
	if err != nil {
		k.LogError("Error setting pubkey", types.Participants, "error", err)
		return nil, err
	}
	k.LogInfo("added account with pubkey", types.Participants, "pubkey", newAccount.GetPubKey(), "address", newAccount.GetAddress())

	k.AccountKeeper.SetAccount(ctx, newAccount)
	newParticipant := createNewParticipant(ctx,
		&types.MsgSubmitNewParticipant{
			Creator:      msg.GetAddress(),
			Url:          msg.GetUrl(),
			ValidatorKey: msg.GetValidatorKey(),
			WorkerKey:    msg.GetWorkerKey(),
		})
	k.LogDebug("Adding new participant", types.Participants, "participant", newParticipant)
	err = k.SetParticipant(ctx, newParticipant)
	if err != nil {
		return nil, err
	}
	return &types.MsgSubmitNewUnfundedParticipantResponse{}, nil
}
