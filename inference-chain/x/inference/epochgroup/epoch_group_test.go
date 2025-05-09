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
	participantKeeper := keeper.NewInMemoryParticipantKeeper()
	groupDataKeeper := keeper.NewInMemoryEpochGroupDataKeeper()

	return &EpochGroupMock{
		EpochGroup: epochgroup.NewEpochGroup(
			groupMock,
			participantKeeper,
			authority,
			logger,
			groupDataKeeper,
			epochGroupData,
		),
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
	data, found := epochGroup.EpochGroup.GroupDataKeeper.GetEpochGroupData(context.Background(), epochGroupData.PocStartBlockHeight,
		epochGroupData.ModelId)
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
	testEG.GroupMock.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	testEG.GroupMock.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	testEG.EpochGroup.AddMember(context.Background(), "member1", 12, "pubkey1", "seedsignature", 1, []string{})
}

func TestAddMembersWithModels(t *testing.T) {
	testEG := createTestEpochGroup(t)

	// Mock for parent group
	testEG.GroupMock.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	testEG.GroupMock.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	// Mock for creating sub-group
	subGroupResponse := &group.MsgCreateGroupWithPolicyResponse{
		GroupId:            9,
		GroupPolicyAddress: "subGroupPolicyAddress",
	}
	testEG.GroupMock.EXPECT().CreateGroupWithPolicy(gomock.Any(), gomock.Any()).Return(subGroupResponse, nil).AnyTimes()

	// Mock for adding member to sub-group - these are now called through the CreateSubGroup method
	// which is called by GetSubGroup
	testEG.GroupMock.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	testEG.GroupMock.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	// Add member with model
	err := testEG.EpochGroup.AddMember(context.Background(), "member1", 12, "pubkey1", "seedsignature", 1, []string{"model1"})
	require.NoError(t, err)

	// Verify sub-group was created
	require.Equal(t, 1, len(testEG.EpochGroup.GroupData.SubGroupHeights))

	// Get the sub-group
	subGroup, err := testEG.EpochGroup.GetSubGroup(context.Background(), "model1")
	require.NoError(t, err)
	require.NotNil(t, subGroup)
	require.Equal(t, "model1", subGroup.GroupData.ModelId)
	require.Equal(t, uint64(9), subGroup.GroupData.EpochGroupId)
}

func TestGetRandomMemberForModel(t *testing.T) {
	testEG := createTestEpochGroup(t)

	// Mock for parent group
	testEG.GroupMock.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	testEG.GroupMock.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	// Mock for creating sub-group
	subGroupResponse := &group.MsgCreateGroupWithPolicyResponse{
		GroupId:            9,
		GroupPolicyAddress: "subGroupPolicyAddress",
	}
	testEG.GroupMock.EXPECT().CreateGroupWithPolicy(gomock.Any(), gomock.Any()).Return(subGroupResponse, nil).AnyTimes()

	// Mock for adding member to sub-group - these are now called through the CreateSubGroup method
	// which is called by GetSubGroup
	testEG.GroupMock.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	testEG.GroupMock.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	// Add member with model
	err := testEG.EpochGroup.AddMember(context.Background(), "member1", 12, "pubkey1", "seedsignature", 1, []string{"model1"})
	require.NoError(t, err)

	// Mock for getting group members
	groupMembers := &group.QueryGroupMembersResponse{
		Members: []*group.GroupMember{
			{
				Member: &group.Member{
					Address: "member1",
					Weight:  "12",
				},
			},
		},
	}
	testEG.GroupMock.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(groupMembers, nil).AnyTimes()

	// Add participant to the keeper
	participant := types.Participant{
		Index:   "member1", // Index must match the address used in the GroupMembers
		Address: "member1",
		Status:  types.ParticipantStatus_ACTIVE,
	}
	testEG.EpochGroup.ParticipantKeeper.SetParticipant(context.Background(), participant)

	// Create a proper SDK context for the test
	ctx := context.Background()

	// Get random member for model
	member, err := testEG.EpochGroup.GetRandomMemberForModel(ctx, "model1", func(members []*group.GroupMember) []*group.GroupMember {
		return members
	})
	require.NoError(t, err)
	require.NotNil(t, member)
	require.Equal(t, "member1", member.Address)
}
