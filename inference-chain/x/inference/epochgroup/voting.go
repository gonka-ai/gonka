package epochgroup

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
)

func (eg *EpochGroup) StartValidationVote(ctx sdk.Context, inference *types.Inference, invalidator string) (*types.ProposalDetails, error) {
	proposalDetails, err := eg.submitValidationProposals(ctx, inference.InferenceId, invalidator, inference.ExecutedBy)
	if err != nil {
		return nil, err
	}
	eg.Logger.LogInfo("Validation: Invalidation Proposals submitted.", "proposalDetails", proposalDetails, "inference", inference.InferenceId, "invalidator", invalidator)
	return proposalDetails, nil
}

func (eg *EpochGroup) submitValidationProposals(ctx sdk.Context, inferenceId string, invalidator string, executor string) (*types.ProposalDetails, error) {
	policyAddress := eg.GroupData.EpochPolicy
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
		Proposers:          []string{executor},
		Metadata:           "Revalidation of inference " + inferenceId,
	}
	err := invalidateProposal.SetMsgs([]sdk.Msg{invalidateMessage})
	if err != nil {
		return nil, err
	}
	err = revalidateProposal.SetMsgs([]sdk.Msg{revalidateMessage})
	invalidateResponse, err := eg.GroupKeeper.SubmitProposal(ctx, &invalidateProposal)
	if err != nil {
		return nil, err
	}
	revalidateResponse, err := eg.GroupKeeper.SubmitProposal(ctx, &revalidateProposal)
	if err != nil {
		return nil, err
	}
	return &types.ProposalDetails{
		InvalidatePolicyId: invalidateResponse.ProposalId,
		ReValidatePolicyId: revalidateResponse.ProposalId,
	}, nil
}

func (eg *EpochGroup) Revalidate(passed bool, inference types.Inference, msg *types.MsgValidation, ctx sdk.Context) (*types.MsgValidationResponse, error) {
	invalidateOption := group.VOTE_OPTION_YES
	revalidationOption := group.VOTE_OPTION_NO
	if passed {
		invalidateOption = group.VOTE_OPTION_NO
		revalidationOption = group.VOTE_OPTION_YES
	}
	voteMsg := &group.MsgVote{
		ProposalId: inference.ProposalDetails.InvalidatePolicyId,
		Voter:      msg.Creator,
		Option:     invalidateOption,
		Metadata:   "Invalidate inference " + inference.InferenceId,
		Exec:       group.Exec_EXEC_TRY,
	}
	err := eg.vote(ctx, voteMsg)
	if err != nil {
		return nil, err
	}
	voteMsg.ProposalId = inference.ProposalDetails.ReValidatePolicyId
	voteMsg.Option = revalidationOption
	voteMsg.Metadata = "Revalidate inference " + inference.InferenceId
	err = eg.vote(ctx, voteMsg)
	if err != nil {
		return nil, err
	}
	return &types.MsgValidationResponse{}, nil
}

func (eg *EpochGroup) vote(ctx sdk.Context, vote *group.MsgVote) error {
	eg.Logger.LogInfo("Validation: Voting", "vote", vote)
	_, err := eg.GroupKeeper.Vote(ctx, vote)
	if err != nil {
		if err.Error() == "proposal not open for voting: invalid value" {
			eg.Logger.LogInfo("Validation: Proposal already decided", "vote", vote)
			return nil
		}
		eg.Logger.LogError("Validation: Error voting", "error", err, "vote", vote)
		return err
	}
	eg.Logger.LogInfo("Validation: Voted on validation", "vote", vote)
	return nil
}
