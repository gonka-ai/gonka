package epochgroup_test

import (
	"context"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"testing"
)

const (
	authority = "authority"
)

type EpochGroupMock struct {
	EpochGroup *epochgroup.EpochGroup
	GroupMock  *keeper.MockGroupMessageKeeper
	Logger     *keeper.MockLogger
}

func createEpochGroupObject(t testing.TB, epochGroupData *types.EpochGroupData) *EpochGroupMock {
	ctrl := gomock.NewController(t)
	groupMock := keeper.NewMockGroupMessageKeeper(ctrl)
	logger := keeper.NewMockLogger()
	return &EpochGroupMock{
		EpochGroup: &epochgroup.EpochGroup{
			GroupKeeper:       groupMock,
			ParticipantKeeper: keeper.NewInMemoryParticipantKeeper(),
			Authority:         authority,
			Logger:            logger,
			GroupDataKeeper:   keeper.NewInMemoryEpochGroupDataKeeper(),
			GroupData:         epochGroupData,
		},
		GroupMock: groupMock,
		Logger:    logger,
	}
}

func TestCreateEpochGroup(t *testing.T) {
	epochGroupData := &types.EpochGroupData{
		PocStartBlockHeight: 10,
	}
	epochGroup := createEpochGroupObject(t, epochGroupData)
	response := &group.MsgCreateGroupWithPolicyResponse{
		GroupId:            8,
		GroupPolicyAddress: "groupPolicyAddress",
	}

	epochGroup.GroupMock.EXPECT().CreateGroupWithPolicy(gomock.Any(), gomock.Any()).Return(response, nil)
	err := epochGroup.EpochGroup.CreateGroup(context.Background())
	require.NoError(t, err)
	data, found := epochGroup.EpochGroup.GroupDataKeeper.GetEpochGroupData(context.Background(), epochGroupData.PocStartBlockHeight)
	require.True(t, found)
	require.Equal(t, uint64(8), data.EpochGroupId)
	require.Equal(t, "groupPolicyAddress", data.EpochPolicy)
}

func createTestEpochGroup(t *testing.T) *EpochGroupMock {
	epochGroupData := &types.EpochGroupData{
		PocStartBlockHeight: 10,
		EpochGroupId:        8,
		EpochPolicy:         "epochPolicy",
	}
	epochGroup := createEpochGroupObject(t, epochGroupData)
	epochGroup.EpochGroup.GroupDataKeeper.SetEpochGroupData(context.Background(), *epochGroupData)
	return epochGroup

}

func TestAddMembers(t *testing.T) {
	testEG := createTestEpochGroup(t)
	testEG.GroupMock.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(nil, nil)
	testEG.GroupMock.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any()).Return(nil, nil)
	testEG.EpochGroup.AddMember(context.Background(), "member1", 12, "pubkey1", "seedsignature", 1)
}

func TestCreateModelEpochGroups(t *testing.T) {
	epochGroupData := &types.EpochGroupData{
		PocStartBlockHeight: 10,
		EpochGroupId:        8,
		EpochPolicy:         "epochPolicy",
	}
	epochGroup := createEpochGroupObject(t, epochGroupData)
	epochGroup.EpochGroup.GroupDataKeeper.SetEpochGroupData(context.Background(), *epochGroupData)

	// Create test models
	models := []*types.Model{
		{
			Id:                     "model1",
			ProposedBy:             "proposer1",
			UnitsOfComputePerToken: 100,
		},
		{
			Id:                     "model2",
			ProposedBy:             "proposer2",
			UnitsOfComputePerToken: 200,
		},
	}

	// Mock the CreateGroupWithPolicy calls
	response1 := &group.MsgCreateGroupWithPolicyResponse{
		GroupId:            10,
		GroupPolicyAddress: "groupPolicyAddress1",
	}
	response2 := &group.MsgCreateGroupWithPolicyResponse{
		GroupId:            11,
		GroupPolicyAddress: "groupPolicyAddress2",
	}

	// Expect two calls to CreateGroupWithPolicy, one for each model
	epochGroup.GroupMock.EXPECT().CreateGroupWithPolicy(gomock.Any(), gomock.Any()).Return(response1, nil)
	epochGroup.GroupMock.EXPECT().CreateGroupWithPolicy(gomock.Any(), gomock.Any()).Return(response2, nil)

	// Call the method under test
	err := epochGroup.EpochGroup.CreateModelEpochGroups(context.Background(), models)
	require.NoError(t, err)

	// Verify the model epoch groups were created
	data, found := epochGroup.EpochGroup.GroupDataKeeper.GetEpochGroupData(context.Background(), epochGroupData.PocStartBlockHeight)
	require.True(t, found)
	require.Equal(t, 2, len(data.ModelEpochGroups))

	// Verify the first model group
	require.Equal(t, "model1", data.ModelEpochGroups[0].ModelId)
	require.Equal(t, uint64(10), data.ModelEpochGroups[0].EpochGroupId)
	require.Equal(t, "groupPolicyAddress1", data.ModelEpochGroups[0].EpochPolicy)

	// Verify the second model group
	require.Equal(t, "model2", data.ModelEpochGroups[1].ModelId)
	require.Equal(t, uint64(11), data.ModelEpochGroups[1].EpochGroupId)
	require.Equal(t, "groupPolicyAddress2", data.ModelEpochGroups[1].EpochPolicy)
}

func TestAddMemberToModelGroups(t *testing.T) {
	// Create a test epoch group with model groups
	epochGroupData := &types.EpochGroupData{
		PocStartBlockHeight: 10,
		EpochGroupId:        8,
		EpochPolicy:         "epochPolicy",
		ModelEpochGroups: []*types.ModelEpochGroup{
			{
				ModelId:           "model1",
				EpochGroupId:      10,
				EpochPolicy:       "groupPolicyAddress1",
				ValidationWeights: []*types.ValidationWeight{},
				TotalWeight:       0,
			},
			{
				ModelId:           "model2",
				EpochGroupId:      11,
				EpochPolicy:       "groupPolicyAddress2",
				ValidationWeights: []*types.ValidationWeight{},
				TotalWeight:       0,
			},
		},
	}
	epochGroup := createEpochGroupObject(t, epochGroupData)
	epochGroup.EpochGroup.GroupDataKeeper.SetEpochGroupData(context.Background(), *epochGroupData)

	// Mock the UpdateGroupMembers calls
	epochGroup.GroupMock.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(nil, nil).Times(2)

	// Call the method under test
	err := epochGroup.EpochGroup.AddMemberToModelGroups(context.Background(), "member1", 12, "pubkey1", []string{"model1", "model2"})
	require.NoError(t, err)

	// Verify the member was added to both model groups
	data, found := epochGroup.EpochGroup.GroupDataKeeper.GetEpochGroupData(context.Background(), epochGroupData.PocStartBlockHeight)
	require.True(t, found)

	// Verify the first model group
	require.Equal(t, 1, len(data.ModelEpochGroups[0].ValidationWeights))
	require.Equal(t, "member1", data.ModelEpochGroups[0].ValidationWeights[0].MemberAddress)
	require.Equal(t, int64(12), data.ModelEpochGroups[0].ValidationWeights[0].Weight)
	require.Equal(t, int64(12), data.ModelEpochGroups[0].TotalWeight)

	// Verify the second model group
	require.Equal(t, 1, len(data.ModelEpochGroups[1].ValidationWeights))
	require.Equal(t, "member1", data.ModelEpochGroups[1].ValidationWeights[0].MemberAddress)
	require.Equal(t, int64(12), data.ModelEpochGroups[1].ValidationWeights[0].Weight)
	require.Equal(t, int64(12), data.ModelEpochGroups[1].TotalWeight)
}
