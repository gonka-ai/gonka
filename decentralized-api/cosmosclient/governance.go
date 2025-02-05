package cosmosclient

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/productscience/inference/x/inference/types"
)

func SubmitProposal(cosmosClient CosmosMessageClient, msg sdk.Msg, depositBaseCoins int64) error {
	proposalMsg, err := v1.NewMsgSubmitProposal(
		[]sdk.Msg{msg},
		types.GetCoins(depositBaseCoins),
		cosmosClient.GetAddress(),
		"Made from decentralized-api", // TODO
		"my-proposal",                 // TODO
		"Hey this is a proposal",      // TODO
		true,                          // TODO: ?
	)
	if err != nil {
		return err
	}

	err = cosmosClient.SendTransaction(proposalMsg)
	if err != nil {
		return err
	}

	return nil
}
