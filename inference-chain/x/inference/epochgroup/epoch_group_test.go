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
	testEG.EpochGroup.AddMember(context.Background(), "member1", 12, "pubkey1")

}
