package cosmosclient

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/productscience/inference/x/inference/types"
	anypb "google.golang.org/protobuf/types/known/anypb"
)

func SubmitProposal(cosmosClient CosmosMessageClient, msg *anypb.Any, deposit int64) error {
	proposalMsg, err := v1.NewMsgSubmitProposal(
		[]sdk.Msg{msg},
		sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, deposit)),
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
