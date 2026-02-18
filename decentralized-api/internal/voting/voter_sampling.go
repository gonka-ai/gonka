package voting

import (
	"context"
	"fmt"

	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/cosmos/cosmos-sdk/x/group"

	"decentralized-api/cosmosclient"
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

// SampledVoter contains a voter's address and inference URL.
type SampledVoter struct {
	Address      string
	InferenceURL string
}

// SampleVotersForInference uses epochgroup.MakeRandomMemberReplayableFn to
// deterministically sample voter candidates for a given inference. The sampling
// is seeded by the block hash at StartBlockHeight and uses the epoch group's
// member list, so results are replayable by on-chain validators.
//
// The inference's EpochGroupId is not set at MsgStartInference time, so we look
// up the root EpochGroupData from the chain using the inference's EpochId.
func SampleVotersForInference(
	ctx context.Context,
	cosmosClient cosmosclient.CosmosMessageClient,
	inf *types.Inference,
	maxVoters int,
	excludeAddresses ...string,
) ([]SampledVoter, error) {
	if maxVoters <= 0 {
		maxVoters = DefaultMaxVoters
	}

	excludeSet := make(map[string]bool, len(excludeAddresses))
	for _, addr := range excludeAddresses {
		excludeSet[addr] = true
	}

	queryClient := cosmosClient.NewInferenceQueryClient()

	// Look up the root EpochGroupData for this inference's epoch.
	// The Inference struct only has EpochId (epoch index) set at StartInference time,
	// not EpochGroupId, so we query the chain for the actual group data.
	epochGroupResp, err := queryClient.EpochGroupData(ctx, &types.QueryGetEpochGroupDataRequest{
		EpochIndex: inf.EpochId,
		ModelId:    "", // root group
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query EpochGroupData for epoch %d: %w", inf.EpochId, err)
	}
	epochGroupData := epochGroupResp.EpochGroupData

	logging.Debug("Resolved EpochGroupData for voter sampling", types.Voting,
		"epochId", inf.EpochId,
		"epochGroupId", epochGroupData.EpochGroupId,
		"inferenceId", inf.InferenceId)

	// Get the block hash from the inference's start block height.
	cometClient := cosmosClient.NewCometQueryClient()
	blockResp, err := cometClient.GetBlockByHeight(ctx, &cmtservice.GetBlockByHeightRequest{
		Height: inf.StartBlockHeight,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get block at height %d: %w", inf.StartBlockHeight, err)
	}
	blockHash := bytes.HexBytes(blockResp.BlockId.Hash)

	// Build an EpochGroup backed by gRPC adapters
	eg := epochgroup.NewEpochGroup(
		&grpcGroupKeeper{client: group.NewQueryClient(cosmosClient.GetClientContext())},
		&grpcParticipantKeeper{queryClient: queryClient},
		nil, // ModelKeeper — unused for random sampling
		nil, // HardwareNodeKeeper — unused for random sampling
		"",  // Authority — unused for random sampling
		&loggingAdapter{},
		nil, // GroupDataKeeper — unused for random sampling
		&epochGroupData,
	)

	nextMember := eg.MakeRandomMemberReplayableFn(ctx, blockHash)

	var voters []SampledVoter
	for len(voters) < maxVoters {
		participant, err := nextMember()
		if err != nil {
			logging.Debug("Voter sampling exhausted participants", types.Voting,
				"error", err, "sampled", len(voters))
			break
		}

		if excludeSet[participant.Address] {
			continue
		}
		if participant.InferenceUrl == "" {
			logging.Debug("Skipping sampled voter: no inference URL", types.Voting,
				"address", participant.Address)
			continue
		}

		voters = append(voters, SampledVoter{
			Address:      participant.Address,
			InferenceURL: participant.InferenceUrl,
		})
	}

	logging.Debug("Sampled voter candidates via replayable random", types.Voting,
		"count", len(voters), "maxVoters", maxVoters,
		"epochGroupId", epochGroupData.EpochGroupId,
		"startBlockHeight", inf.StartBlockHeight)

	return voters, nil
}

//
// gRPC-backed keeper adapters for epochgroup.EpochGroup
//
// MakeRandomMemberReplayableFn lives on *epochgroup.EpochGroup, which expects
// on-chain keeper interfaces. The adapters below implement those interfaces by
// delegating to gRPC query clients.
//
// Only the methods actually called by the random-sampling path are implemented:
//   - grpcGroupKeeper.GroupMembers       (used by EpochGroup.GetGroupMembers)
//   - grpcParticipantKeeper.GetParticipant (used by EpochGroup.GetRandomMemberReplayable)
//
// The remaining methods satisfy the interface but are never called; they panic
// to surface accidental misuse immediately.

// grpcGroupKeeper implements types.GroupMessageKeeper via gRPC.
type grpcGroupKeeper struct{ client group.QueryClient }

func (g *grpcGroupKeeper) GroupMembers(ctx context.Context, req *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
	return g.client.GroupMembers(ctx, req)
}

// Unused — required by types.GroupMessageKeeper interface.
func (g *grpcGroupKeeper) CreateGroup(context.Context, *group.MsgCreateGroup) (*group.MsgCreateGroupResponse, error) {
	panic("not implemented")
}
func (g *grpcGroupKeeper) CreateGroupWithPolicy(context.Context, *group.MsgCreateGroupWithPolicy) (*group.MsgCreateGroupWithPolicyResponse, error) {
	panic("not implemented")
}
func (g *grpcGroupKeeper) UpdateGroupMembers(context.Context, *group.MsgUpdateGroupMembers) (*group.MsgUpdateGroupMembersResponse, error) {
	panic("not implemented")
}
func (g *grpcGroupKeeper) UpdateGroupMetadata(context.Context, *group.MsgUpdateGroupMetadata) (*group.MsgUpdateGroupMetadataResponse, error) {
	panic("not implemented")
}
func (g *grpcGroupKeeper) SubmitProposal(context.Context, *group.MsgSubmitProposal) (*group.MsgSubmitProposalResponse, error) {
	panic("not implemented")
}
func (g *grpcGroupKeeper) Vote(context.Context, *group.MsgVote) (*group.MsgVoteResponse, error) {
	panic("not implemented")
}
func (g *grpcGroupKeeper) GroupInfo(context.Context, *group.QueryGroupInfoRequest) (*group.QueryGroupInfoResponse, error) {
	panic("not implemented")
}
func (g *grpcGroupKeeper) ProposalsByGroupPolicy(context.Context, *group.QueryProposalsByGroupPolicyRequest) (*group.QueryProposalsByGroupPolicyResponse, error) {
	panic("not implemented")
}

// grpcParticipantKeeper implements types.ParticipantKeeper via gRPC.
type grpcParticipantKeeper struct{ queryClient types.QueryClient }

func (p *grpcParticipantKeeper) GetParticipant(ctx context.Context, index string) (types.Participant, bool) {
	resp, err := p.queryClient.Participant(ctx, &types.QueryGetParticipantRequest{Index: index})
	if err != nil {
		return types.Participant{}, false
	}
	return resp.Participant, true
}

// Unused — required by types.ParticipantKeeper interface.
func (p *grpcParticipantKeeper) SetParticipant(context.Context, types.Participant) error {
	panic("not implemented")
}
func (p *grpcParticipantKeeper) RemoveParticipant(context.Context, string) { panic("not implemented") }
func (p *grpcParticipantKeeper) GetAllParticipant(context.Context) []types.Participant {
	panic("not implemented")
}
func (p *grpcParticipantKeeper) ParticipantAll(context.Context, *types.QueryAllParticipantRequest) (*types.QueryAllParticipantResponse, error) {
	panic("not implemented")
}

// loggingAdapter implements types.InferenceLogger for off-chain use.
type loggingAdapter struct{}

func (l *loggingAdapter) LogInfo(msg string, s types.SubSystem, kv ...interface{}) {
	logging.Info(msg, s, kv...)
}
func (l *loggingAdapter) LogError(msg string, s types.SubSystem, kv ...interface{}) {
	logging.Error(msg, s, kv...)
}
func (l *loggingAdapter) LogWarn(msg string, s types.SubSystem, kv ...interface{}) {
	logging.Warn(msg, s, kv...)
}
func (l *loggingAdapter) LogDebug(msg string, s types.SubSystem, kv ...interface{}) {
	logging.Debug(msg, s, kv...)
}
