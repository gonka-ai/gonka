package cosmosclient

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/productscience/inference/x/inference/types"
)

type ProposalData struct {
	Metadata  string
	Title     string
	Summary   string
	Expedited bool
}

func SubmitProposal(cosmosClient CosmosMessageClient, msg sdk.Msg, proposalData *ProposalData) error {
	proposalMsg, err := v1.NewMsgSubmitProposal(
		[]sdk.Msg{msg},
		types.GetCoins(100000000), // FIXME: this should be equal to min deposit
		cosmosClient.GetAddress(),
		proposalData.Metadata,
		proposalData.Title,
		proposalData.Summary,
		proposalData.Expedited,
	)
	if err != nil {
		return err
	}
	return cosmosClient.SendTransaction(proposalMsg)
}

func GetProposalMsgSigner() string {
	return authtypes.NewModuleAddress(govtypes.ModuleName).String()
}
