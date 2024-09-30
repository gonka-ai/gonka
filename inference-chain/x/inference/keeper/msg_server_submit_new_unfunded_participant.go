package keeper

import (
	"context"
	"encoding/base64"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitNewUnfundedParticipant(goCtx context.Context, msg *types.MsgSubmitNewUnfundedParticipant) (*types.MsgSubmitNewUnfundedParticipantResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("Adding new account directly", "address", msg.Address)
	// First, add the account
	newAccount := k.AccountKeeper.NewAccountWithAddress(ctx, sdk.MustAccAddressFromBech32(msg.Address))
	pubKeyBytes, err := base64.StdEncoding.DecodeString(msg.PubKey)
	if err != nil {
		return nil, err
	}
	actualKey := secp256k1.PubKey{Key: pubKeyBytes}
	err = newAccount.SetPubKey(&actualKey)
	if err != nil {
		k.LogError("Error setting pubkey", "error", err)
		return nil, err
	}
	k.LogInfo("added account with pubkey", "pubkey", newAccount.GetPubKey(), "address", newAccount.GetAddress())

	k.AccountKeeper.SetAccount(ctx, newAccount)
	// TODO: Handling the message
	_ = ctx
	newParticipant := createNewParticipant(ctx,
		&types.MsgSubmitNewParticipant{
			Creator:      msg.GetAddress(),
			Url:          msg.GetUrl(),
			Models:       msg.GetModels(),
			ValidatorKey: msg.GetValidatorKey(),
		})
	k.LogDebug("Adding new participant", "participant", newParticipant)
	k.SetParticipant(ctx, newParticipant)
	return &types.MsgSubmitNewUnfundedParticipantResponse{}, nil
}
