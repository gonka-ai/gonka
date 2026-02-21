package voting

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/cosmos/cosmos-sdk/x/group"

	"decentralized-api/cosmosclient"
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// SampledVoter contains a voter's address and inference URL.
type SampledVoter struct {
	Address      string
	InferenceURL string
}

// replayableRandomContext holds state for deterministic weighted random sampling.
// This matches the algorithm used by epochgroup.EpochGroup for consistency.
type replayableRandomContext struct {
	participants    []*group.GroupMember
	seed            [32]byte
	seenIndices     map[int]bool
	cumulativeArray []int64
}

// SampleVotersForInference deterministically samples voter candidates for a given inference.
// The sampling is seeded by the block hash at StartBlockHeight and uses the epoch group's
// member list, so results are replayable by on-chain validators.
//
// The inference's EpochGroupId is not set at MsgStartInference time, so we look
// up the root EpochGroupData from the chain using the inference's EpochId.
func SampleVotersForInference(
	ctx context.Context,
	cosmosClient cosmosclient.CosmosMessageClient,
	inf *types.Inference,
	excludeAddresses ...string,
) ([]SampledVoter, error) {
	maxVoters := types.DefaultMaxVotersToSample

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

	// Fetch all group members via gRPC
	groupClient := group.NewQueryClient(cosmosClient.GetClientContext())
	members, err := getAllGroupMembersPaginated(ctx, groupClient, epochGroupData.EpochGroupId)
	if err != nil {
		return nil, fmt.Errorf("failed to get group members: %w", err)
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("no group members found for epoch group %d", epochGroupData.EpochGroupId)
	}

	// Initialize replayable random context with deterministic seed
	seed := initialSeed(blockHash)
	cumulativeArray := computeCumulativeArray(members)
	randomCtx := &replayableRandomContext{
		participants:    members,
		seed:            seed,
		seenIndices:     make(map[int]bool),
		cumulativeArray: cumulativeArray,
	}

	// Sample voters using deterministic weighted random selection
	var voters []SampledVoter
	for uint32(len(voters)) < maxVoters {
		participantAddress, err := selectRandomParticipantReplayable(randomCtx)
		if err != nil {
			logging.Debug("Voter sampling exhausted participants", types.Voting,
				"error", err, "sampled", len(voters))
			break
		}

		// Fetch participant details to get inference URL
		participantResp, err := queryClient.Participant(ctx, &types.QueryGetParticipantRequest{
			Index: participantAddress,
		})
		if err != nil {
			logging.Debug("Failed to get participant details", types.Voting,
				"address", participantAddress, "error", err)
			continue
		}
		participant := participantResp.Participant

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

// getAllGroupMembersPaginated fetches all group members using pagination via gRPC.
func getAllGroupMembersPaginated(ctx context.Context, groupClient group.QueryClient, groupId uint64) ([]*group.GroupMember, error) {
	var allMembers []*group.GroupMember
	var nextKey []byte

	for {
		resp, err := groupClient.GroupMembers(ctx, &group.QueryGroupMembersRequest{
			GroupId: groupId,
			Pagination: &query.PageRequest{
				Key:   nextKey,
				Limit: 100,
			},
		})
		if err != nil {
			return nil, err
		}

		allMembers = append(allMembers, resp.Members...)

		if resp.Pagination == nil || len(resp.Pagination.NextKey) == 0 {
			break
		}
		nextKey = resp.Pagination.NextKey
	}

	return allMembers, nil
}

// initialSeed creates a deterministic seed from the block hash.
// This matches epochgroup.InitialSeed for consistency.
func initialSeed(blockHash bytes.HexBytes) [32]byte {
	hashBytes := blockHash.Bytes()
	return sha256.Sum256(hashBytes)
}

// computeCumulativeArray computes cumulative weights for weighted random selection.
func computeCumulativeArray(participants []*group.GroupMember) []int64 {
	cumulativeArray := make([]int64, len(participants))
	if len(participants) == 0 {
		return cumulativeArray
	}
	cumulativeArray[0] = getWeight(participants[0])
	for i := 1; i < len(participants); i++ {
		cumulativeArray[i] = cumulativeArray[i-1] + getWeight(participants[i])
	}
	return cumulativeArray
}

// getWeight extracts the weight from a group member.
func getWeight(participant *group.GroupMember) int64 {
	weight, err := strconv.Atoi(participant.Member.Weight)
	if err != nil {
		return 0
	}
	return int64(weight)
}

// selectRandomParticipantReplayable performs deterministic weighted random selection
// without replacement. This matches epochgroup.selectRandomParticipantReplayable.
func selectRandomParticipantReplayable(ctx *replayableRandomContext) (string, error) {
	participantsCnt := len(ctx.participants)
	if len(ctx.seenIndices) >= participantsCnt {
		return "", fmt.Errorf("no participants to sample")
	}

	weightSum := ctx.cumulativeArray[participantsCnt-1]
	if weightSum == 0 {
		return "", fmt.Errorf("total weight is zero")
	}

	for {
		currentSeed := ctx.seed[:]
		randomWeight := int64(binary.LittleEndian.Uint64(currentSeed)) % weightSum

		index := upperBound(randomWeight, ctx.cumulativeArray)
		if index >= participantsCnt {
			index = participantsCnt - 1
		}

		ctx.seed = sha256.Sum256(currentSeed)
		if !ctx.seenIndices[index] {
			ctx.seenIndices[index] = true
			return ctx.participants[index].Member.Address, nil
		}
	}
}

// upperBound performs a binary search for the lowest value greater than the needle.
// Assumes the input array is already sorted. Matches epochgroup.UpperBound.
func upperBound[T cmp.Ordered](needle T, haystack []T) int {
	low, high := 0, len(haystack)
	for low < high {
		middle := low + (high-low)/2
		if needle < haystack[middle] {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return low
}

