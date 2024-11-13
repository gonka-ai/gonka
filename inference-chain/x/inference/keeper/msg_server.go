package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
)

type msgServer struct {
	Keeper
}

func (k msgServer) startValidationVote(ctx sdk.Context, inference *types.Inference, invalidator string) (*types.ProposalDetails, error) {
	proposalDetails, err := k.submitValidationProposals(ctx, inference.InferenceId, invalidator)
	if err != nil {
		return nil, err
	}
	k.LogInfo("Validation: Invalidation Proposals submitted.", "proposalDetails", proposalDetails, "inference", inference.InferenceId, "invalidator", invalidator)
	return proposalDetails, nil
}

func (k msgServer) submitValidationProposals(ctx sdk.Context, inferenceId string, invalidator string) (*types.ProposalDetails, error) {
	policyAddress := k.GetEpochPolicy(ctx)
	invalidateMessage := &types.MsgInvalidateInference{
		InferenceId: inferenceId,
		Creator:     policyAddress,
	}
	revalidateMessage := &types.MsgRevalidateInference{
		InferenceId: inferenceId,
		Creator:     policyAddress,
	}
	invalidateProposal := group.MsgSubmitProposal{
		GroupPolicyAddress: policyAddress,
		Proposers:          []string{invalidator},
		Metadata:           "Invalidation of inference " + inferenceId,
	}
	revalidateProposal := group.MsgSubmitProposal{
		GroupPolicyAddress: policyAddress,
		Proposers:          []string{invalidator},
		Metadata:           "Revalidation of inference " + inferenceId,
	}
	err := invalidateProposal.SetMsgs([]sdk.Msg{invalidateMessage})
	if err != nil {
		return nil, err
	}
	err = revalidateProposal.SetMsgs([]sdk.Msg{revalidateMessage})
	invalidateResponse, err := k.group.SubmitProposal(ctx, &invalidateProposal)
	if err != nil {
		return nil, err
	}
	revalidateResponse, err := k.group.SubmitProposal(ctx, &revalidateProposal)
	if err != nil {
		return nil, err
	}
	return &types.ProposalDetails{
		InvalidatePolicyId: invalidateResponse.ProposalId,
		ReValidatePolicyId: revalidateResponse.ProposalId,
		PolicyAddress:      policyAddress,
	}, nil
}

// NewMsgServerImpl returns an implementation of the MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}
