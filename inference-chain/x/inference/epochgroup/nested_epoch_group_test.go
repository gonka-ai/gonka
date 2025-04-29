package epochgroup_test

import (
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGetModelValidationWeights(t *testing.T) {
	// Create an EpochGroup with a model and members
	epochGroupData := &types.EpochGroupData{
		PocStartBlockHeight: 100,
		EpochGroupId:        1,
		EpochPolicy:         "policy1",
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress: "address1",
				Weight:        100,
				Reputation:    10,
			},
			{
				MemberAddress: "address2",
				Weight:        200,
				Reputation:    20,
			},
		},
		TotalWeight: 300,
		ModelEpochGroups: []*types.ModelEpochGroup{
			{
				ModelId:      "model1",
				EpochGroupId: 2,
				EpochPolicy:  "policy2",
				ValidationWeights: []*types.ValidationWeight{
					{
						MemberAddress: "address1",
						Weight:        100,
						Reputation:    10,
					},
				},
				TotalWeight: 100,
			},
		},
	}

	eg := &epochgroup.EpochGroup{
		GroupData: epochGroupData,
	}

	// Test GetValidationWeights
	votingData, err := eg.GetValidationWeights()
	require.NoError(t, err)
	require.Equal(t, int64(300), votingData.TotalWeight)
	require.Len(t, votingData.Members, 2)
	require.Equal(t, int64(100), votingData.Members["address1"])
	require.Equal(t, int64(200), votingData.Members["address2"])

	// Test GetModelValidationWeights
	modelVotingData, err := eg.GetModelValidationWeights("model1")
	require.NoError(t, err)
	require.Equal(t, int64(100), modelVotingData.TotalWeight)
	require.Len(t, modelVotingData.Members, 1)
	require.Equal(t, int64(100), modelVotingData.Members["address1"])

	// Test GetModelValidationWeights for non-existent model
	nonExistentModelVotingData, err := eg.GetModelValidationWeights("non-existent-model")
	require.NoError(t, err)
	require.Equal(t, int64(0), nonExistentModelVotingData.TotalWeight)
	require.Len(t, nonExistentModelVotingData.Members, 0)
}
