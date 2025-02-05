package keeper

import (
	"context"
	"encoding/base64"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const FaucetRequests = 50

func (k msgServer) SubmitNewUnfundedParticipant(goCtx context.Context, msg *types.MsgSubmitNewUnfundedParticipant) (*types.MsgSubmitNewUnfundedParticipantResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("Adding new account directly", "address", msg.Address)
	// First, add the account
	if k.AccountKeeper.GetAccount(ctx, sdk.MustAccAddressFromBech32(msg.Address)) != nil {
		k.LogError("Account already exists", "address", msg.Address)
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
			WorkerKey:    msg.GetWorkerKey(),
		})
	k.LogDebug("Adding new participant", "participant", newParticipant)
	k.SetParticipant(ctx, newParticipant)
	if newParticipant.GetInferenceUrl() == "" {
		// Consumer only!
		k.LogInfo("Funding new consumer", "consumer", newParticipant)
		starterAmount := int64(DefaultMaxTokens * TokenCost * FaucetRequests)
		starterCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, starterAmount))
		err := k.MintRewardCoins(ctx, starterAmount)
		if err != nil {
			k.LogError("Error minting coins", "error", err)
			return nil, err
		}
		err = k.bank.SendCoinsFromModuleToAccount(ctx, types.ModuleName, sdk.MustAccAddressFromBech32(msg.GetAddress()), starterCoins)
		if err != nil {
			k.LogError("Error sending coins", "error", err)
			return nil, err
		}
	}
	return &types.MsgSubmitNewUnfundedParticipantResponse{}, nil
}
