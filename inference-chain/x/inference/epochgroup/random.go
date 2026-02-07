package epochgroup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"
	"strconv"

	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetRandomMemberForModel gets a random member for a specific model
func (eg *EpochGroup) GetRandomMemberForModel(
	goCtx context.Context,
	modelId string,
	filterFn func([]*group.GroupMember) []*group.GroupMember,
) (*types.Participant, error) {
	// If modelId is provided and this is the parent group, delegate to the sub-group
	if modelId != "" && eg.GroupData.GetModelId() == "" {
		subGroup, err := eg.GetSubGroup(goCtx, modelId)
		if err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("Error getting sub-group for model %s: %v", modelId, err))
		}
		return subGroup.GetRandomMember(goCtx, filterFn)
	}

	// Otherwise, get a random member from this group
	return eg.GetRandomMember(goCtx, filterFn)
}

func (eg *EpochGroup) GetRandomMember(
	goCtx context.Context,
	filterFn func([]*group.GroupMember) []*group.GroupMember,
) (*types.Participant, error) {
	// Use the context as is, don't try to unwrap it
	// This allows the method to work with both SDK contexts and regular contexts
	ctx := goCtx

	activeParticipants, err := eg.getAllGroupMembersPaginated(ctx, uint64(eg.GroupData.EpochGroupId))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if len(activeParticipants) == 0 {
		return nil, status.Error(codes.Internal, "Active participants found, but length is 0")
	}

	filteredParticipants := filterFn(activeParticipants)
	if len(filteredParticipants) == 0 {
		return nil, status.Error(codes.Internal, "After filtering participants the length is 0")
	}

	// Create deterministic seed from epoch data for reproducible selection based on blockchain state
	epochIndex := eg.GroupData.EpochIndex
	pocStartHeight := eg.GroupData.PocStartBlockHeight
	modelId := eg.GroupData.ModelId

	participantIndex := selectRandomParticipant(filteredParticipants, epochIndex, pocStartHeight, modelId)

	participant, ok := eg.ParticipantKeeper.GetParticipant(ctx, participantIndex)
	if !ok {
		msg := fmt.Sprintf(
			"Selected active participant, but not found in participants list. index =  %s", participantIndex,
		)
		return nil, status.Error(codes.Internal, msg)
	}
	return &participant, nil
}

// selectRandomParticipant selects a participant using deterministic weighted random selection.
// Uses deterministic seeding from blockchain state (epoch index, block height, model ID)
// to ensure reproducible selection across all nodes for consensus.
// Note: This is NOT cryptographically secure - the selection is predictable given public blockchain state.
func selectRandomParticipant(participants []*group.GroupMember, epochIndex uint64, pocStartHeight uint64, modelId string) string {
	cumulativeArray := computeCumulativeArray(participants)

	// Check for zero total weight to prevent panic
	totalWeight := cumulativeArray[len(cumulativeArray)-1]
	if totalWeight <= 0 {
		// Fallback to first participant if all weights are zero/invalid
		return participants[0].Member.Address
	}

	// Create deterministic seed from blockchain state
	// This ensures all nodes select the same participant for the same request
	seed := computeSelectionSeed(epochIndex, pocStartHeight, modelId, participants)
	rng := rand.New(rand.NewSource(seed))

	randomNumber := rng.Int63n(totalWeight)
	for i, cumulativeWeight := range cumulativeArray {
		if randomNumber < cumulativeWeight {
			return participants[i].Member.Address
		}
	}

	return participants[len(participants)-1].Member.Address
}

// computeSelectionSeed creates a deterministic seed from blockchain state and participant data.
// The seed incorporates:
// - Epoch index: changes each epoch
// - PoC start block height: ties selection to specific block
// - Model ID: different seeds per model
// - Participant addresses hash: seed changes if participant set changes
func computeSelectionSeed(epochIndex uint64, pocStartHeight uint64, modelId string, participants []*group.GroupMember) int64 {
	h := sha256.New()

	// Add epoch index
	epochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBytes, epochIndex)
	h.Write(epochBytes)

	// Add PoC start block height
	heightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(heightBytes, pocStartHeight)
	h.Write(heightBytes)

	// Add model ID
	h.Write([]byte(modelId))

	// Sort participant addresses to ensure deterministic ordering across all nodes
	// This is critical for consensus - different nodes may receive participants in different order
	addresses := make([]string, len(participants))
	for i, p := range participants {
		addresses[i] = p.Member.Address
	}
	sort.Strings(addresses)

	// Add sorted participant addresses for determinism when participant set changes
	for _, addr := range addresses {
		h.Write([]byte(addr))
	}

	hash := h.Sum(nil)
	return int64(binary.BigEndian.Uint64(hash[:8]))
}

func computeCumulativeArray(participants []*group.GroupMember) []int64 {
	cumulativeArray := make([]int64, len(participants))
	cumulativeArray[0] = int64(getWeight(participants[0]))
	for i := 1; i < len(participants); i++ {
		cumulativeArray[i] = cumulativeArray[i-1] + getWeight(participants[i])
	}
	return cumulativeArray
}

func getWeight(participant *group.GroupMember) int64 {
	weight, err := strconv.Atoi(participant.Member.Weight)
	if err != nil {
		return 0
	}
	return int64(weight)
}
