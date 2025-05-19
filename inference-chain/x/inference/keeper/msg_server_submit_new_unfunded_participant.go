package keeper

import (
	"context"
	"encoding/base64"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const FaucetRequests = 1000

func (k msgServer) SubmitNewUnfundedParticipant(goCtx context.Context, msg *types.MsgSubmitNewUnfundedParticipant) (*types.MsgSubmitNewUnfundedParticipantResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("Adding new account directly", types.Participants, "address", msg.Address)
	// First, add the account
	if k.AccountKeeper.GetAccount(ctx, sdk.MustAccAddressFromBech32(msg.Address)) != nil {
		k.LogError("Account already exists", types.Participants, "address", msg.Address)
		return nil, types.ErrAccountAlreadyExists
	}
	newAccount := k.AccountKeeper.NewAccountWithAddress(ctx, sdk.MustAccAddressFromBech32(msg.Address))
	pubKeyBytes, err := base64.StdEncoding.DecodeString(msg.PubKey)
	if err != nil {
		return nil, err
	}
	actualKey := secp256k1.PubKey{Key: pubKeyBytes}
	err = newAccount.SetPubKey(&actualKey)
	if err != nil {
		k.LogError("Error setting pubkey", types.Participants, "error", err)
		return nil, err
	}
	k.LogInfo("added account with pubkey", types.Participants, "pubkey", newAccount.GetPubKey(), "address", newAccount.GetAddress())

	k.AccountKeeper.SetAccount(ctx, newAccount)
	// TODO: Handling the message
	newParticipant := createNewParticipant(ctx,
		&types.MsgSubmitNewParticipant{
			Creator:      msg.GetAddress(),
			Url:          msg.GetUrl(),
			ValidatorKey: msg.GetValidatorKey(),
			WorkerKey:    msg.GetWorkerKey(),
		})
	k.LogDebug("Adding new participant", types.Participants, "participant", newParticipant)
	k.SetParticipant(ctx, newParticipant)
	if newParticipant.GetInferenceUrl() == "" {
		// Consumer only!
		k.LogInfo("Funding new consumer", types.Participants, "consumer", newParticipant)
		starterAmount := int64(DefaultMaxTokens * TokenCost * FaucetRequests)
		err := k.MintRewardCoins(ctx, starterAmount, "starter_coins:"+newParticipant.GetAddress())
		if err != nil {
			k.LogError("Error minting coins", types.Participants, "error", err)
			return nil, err
		}
		err = k.PayParticipantFromModule(ctx, msg.GetAddress(), uint64(starterAmount), types.ModuleName, "starter_coins:"+newParticipant.GetAddress())
		if err != nil {
			k.LogError("Error sending coins", types.Participants, "error", err)
			return nil, err
		}
	}
	return &types.MsgSubmitNewUnfundedParticipantResponse{}, nil
}
