package epochgroup

import (
	"context"
	"errors"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type mockLogger struct{}

func (m *mockLogger) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{})  {}
func (m *mockLogger) LogError(msg string, subSystem types.SubSystem, keyvals ...interface{}) {}
func (m *mockLogger) LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{})  {}
func (m *mockLogger) LogDebug(msg string, subSystem types.SubSystem, keyvals ...interface{}) {}

type mockGroupKeeperFunc func(ctx context.Context, req *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error)

type mockGroupKeeper struct {
	fn mockGroupKeeperFunc
}

func (m *mockGroupKeeper) GroupMembers(ctx context.Context, req *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
	return m.fn(ctx, req)
}

func (m *mockGroupKeeper) GroupsByMember(ctx context.Context, req *group.QueryGroupsByMemberRequest) (*group.QueryGroupsByMemberResponse, error) {
	return nil, nil
}

func (m *mockGroupKeeper) CreateGroup(ctx context.Context, msg *group.MsgCreateGroup) (*group.MsgCreateGroupResponse, error) {
	return nil, nil
}

func (m *mockGroupKeeper) CreateGroupWithPolicy(ctx context.Context, msg *group.MsgCreateGroupWithPolicy) (*group.MsgCreateGroupWithPolicyResponse, error) {
	return nil, nil
}

func (m *mockGroupKeeper) GroupInfo(ctx context.Context, req *group.QueryGroupInfoRequest) (*group.QueryGroupInfoResponse, error) {
	return nil, nil
}

func (m *mockGroupKeeper) UpdateGroupMembers(ctx context.Context, msg *group.MsgUpdateGroupMembers) (*group.MsgUpdateGroupMembersResponse, error) {
	return nil, nil
}

func (m *mockGroupKeeper) UpdateGroupMetadata(ctx context.Context, msg *group.MsgUpdateGroupMetadata) (*group.MsgUpdateGroupMetadataResponse, error) {
	return nil, nil
}

func (m *mockGroupKeeper) SubmitProposal(ctx context.Context, msg *group.MsgSubmitProposal) (*group.MsgSubmitProposalResponse, error) {
	return nil, nil
}

func (m *mockGroupKeeper) Vote(ctx context.Context, msg *group.MsgVote) (*group.MsgVoteResponse, error) {
	return nil, nil
}

func (m *mockGroupKeeper) ProposalsByGroupPolicy(ctx context.Context, req *group.QueryProposalsByGroupPolicyRequest) (*group.QueryProposalsByGroupPolicyResponse, error) {
	return nil, nil
}

func TestCalculatePocParticipatingNodesWeight_AllServeInference(t *testing.T) {
	mlNodes := []*types.ModelMLNodes{
		{
			MlNodes: []*types.MLNodeInfo{
				{
					NodeId:             "node1",
					PocWeight:          100,
					TimeslotAllocation: []bool{true, true}, // POC_SLOT=true (serves inference)
				},
				{
					NodeId:             "node2",
					PocWeight:          200,
					TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
				},
			},
		},
	}

	weight := calculatePocParticipatingNodesWeight(mlNodes)

	// Should be 0 since all nodes have POC_SLOT=true
	require.Equal(t, int64(0), weight)
}

func TestCalculatePocParticipatingNodesWeight_NoneServeInference(t *testing.T) {
	mlNodes := []*types.ModelMLNodes{
		{
			MlNodes: []*types.MLNodeInfo{
				{
					NodeId:             "node1",
					PocWeight:          100,
					TimeslotAllocation: []bool{true, false},
				},
				{
					NodeId:             "node2",
					PocWeight:          200,
					TimeslotAllocation: []bool{false, false},
				},
			},
		},
	}

	weight := calculatePocParticipatingNodesWeight(mlNodes)

	// Should be sum of all weights since all have POC_SLOT=false,
	//  meaning no nodes serve inference during PoC
	require.Equal(t, int64(300), weight)
}

func TestCalculatePocParticipatingNodesWeight_Mixed(t *testing.T) {
	mlNodes := []*types.ModelMLNodes{
		{
			MlNodes: []*types.MLNodeInfo{
				{
					NodeId:             "node1",
					PocWeight:          100,
					TimeslotAllocation: []bool{true, false}, // POC_SLOT=false - INCLUDE
				},
				{
					NodeId:             "node2",
					PocWeight:          200,
					TimeslotAllocation: []bool{true, true}, // POC_SLOT=true - EXCLUDE
				},
				{
					NodeId:             "node3",
					PocWeight:          300,
					TimeslotAllocation: []bool{false, false}, // POC_SLOT=false - INCLUDE
				},
				{
					NodeId:             "node4",
					PocWeight:          400,
					TimeslotAllocation: []bool{false, true}, // POC_SLOT=true - EXCLUDE
				},
			},
		},
	}

	weight := calculatePocParticipatingNodesWeight(mlNodes)

	// Should be 100 + 300 = 400 (only POC_SLOT=false nodes)
	require.Equal(t, int64(400), weight)
}

func TestCalculatePocParticipatingNodesWeight_EmptySlots(t *testing.T) {
	// Nodes with empty or short TimeslotAllocation arrays
	mlNodes := []*types.ModelMLNodes{
		{
			MlNodes: []*types.MLNodeInfo{
				{
					NodeId:             "node1",
					PocWeight:          100,
					TimeslotAllocation: []bool{}, // Empty - should be excluded
				},
				{
					NodeId:             "node2",
					PocWeight:          200,
					TimeslotAllocation: []bool{true}, // Only 1 slot - should be excluded
				},
				{
					NodeId:             "node3",
					PocWeight:          300,
					TimeslotAllocation: []bool{true, false}, // Has index 1 = false - INCLUDE
				},
			},
		},
	}

	weight := calculatePocParticipatingNodesWeight(mlNodes)

	// Should be 300 (only node3 has valid POC_SLOT at index 1)
	require.Equal(t, int64(300), weight)
}

func TestCalculatePocParticipatingNodesWeight_NilNodes(t *testing.T) {
	// Test handling of nil nodes
	mlNodes := []*types.ModelMLNodes{
		nil, // Nil model nodes
		{
			MlNodes: []*types.MLNodeInfo{
				nil, // Nil node
				{
					NodeId:             "node1",
					PocWeight:          100,
					TimeslotAllocation: []bool{true, false},
				},
			},
		},
	}

	weight := calculatePocParticipatingNodesWeight(mlNodes)

	// Should handle nils gracefully and count only valid node
	require.Equal(t, int64(100), weight)
}

func TestSanitizeMembers_FiltersNilMembers(t *testing.T) {
	members := []*group.GroupMember{
		nil,
		{Member: nil},
		{Member: &group.Member{Address: "addr1", Weight: "1"}},
	}

	filtered := sanitizeMembers(members)

	require.Len(t, filtered, 1)
	require.Equal(t, "addr1", filtered[0].Member.Address)
}

func TestCalculatePocParticipatingNodesWeight_MultipleModelArrays(t *testing.T) {
	// Multiple model arrays (though typically there's only one)
	mlNodes := []*types.ModelMLNodes{
		{
			MlNodes: []*types.MLNodeInfo{
				{
					NodeId:             "node1",
					PocWeight:          100,
					TimeslotAllocation: []bool{true, false},
				},
			},
		},
		{
			MlNodes: []*types.MLNodeInfo{
				{
					NodeId:             "node2",
					PocWeight:          200,
					TimeslotAllocation: []bool{false, false},
				},
			},
		},
	}

	weight := calculatePocParticipatingNodesWeight(mlNodes)

	// Should sum across all model arrays
	require.Equal(t, int64(300), weight)
}

// Test confirmation weight initialization when creating EpochMember
func TestNewEpochMemberFromActiveParticipant_ConfirmationWeightInitialization(t *testing.T) {
	// Create ActiveParticipant with mixed timeslot allocations
	p := &types.ActiveParticipant{
		Index:        "test-participant",
		ValidatorKey: "test-pubkey",
		Weight:       450,
		MlNodes: []*types.ModelMLNodes{
			{
				MlNodes: []*types.MLNodeInfo{
					{
						NodeId:             "node1",
						PocWeight:          100,
						TimeslotAllocation: []bool{true, false}, // POC_SLOT=false - INCLUDE
					},
					{
						NodeId:             "node2",
						PocWeight:          200,
						TimeslotAllocation: []bool{true, true}, // POC_SLOT=true - EXCLUDE
					},
					{
						NodeId:             "node3",
						PocWeight:          150,
						TimeslotAllocation: []bool{true, false}, // POC_SLOT=false - INCLUDE
					},
				},
			},
		},
	}

	// Call with confirmationWeight = 0 to trigger initialization
	member := NewEpochMemberFromActiveParticipant(p, 1, 0)

	// Should sum only POC_SLOT=false weights: 100 + 150 = 250
	require.Equal(t, int64(250), member.ConfirmationWeight, "confirmation_weight should equal sum of POC_SLOT=false weights")
	require.Equal(t, int64(450), member.Weight, "total weight should remain unchanged")
}

func TestNewEpochMemberFromActiveParticipant_ConfirmationWeightProvided(t *testing.T) {
	// Create ActiveParticipant with mixed timeslot allocations
	p := &types.ActiveParticipant{
		Index:        "test-participant",
		ValidatorKey: "test-pubkey",
		Weight:       450,
		MlNodes: []*types.ModelMLNodes{
			{
				MlNodes: []*types.MLNodeInfo{
					{
						NodeId:             "node1",
						PocWeight:          100,
						TimeslotAllocation: []bool{true, false},
					},
					{
						NodeId:             "node2",
						PocWeight:          150,
						TimeslotAllocation: []bool{true, false},
					},
				},
			},
		},
	}

	// Call with confirmationWeight already provided (e.g., from previous confirmation PoC)
	member := NewEpochMemberFromActiveParticipant(p, 1, 180)

	// Should use the provided value (180), not recalculate (which would be 250)
	require.Equal(t, int64(180), member.ConfirmationWeight, "confirmation_weight should use provided value")
}

func TestNewEpochMemberFromActiveParticipant_AllPreservedNodes(t *testing.T) {
	// All nodes have POC_SLOT=true (preserved for inference)
	p := &types.ActiveParticipant{
		Index:        "test-participant",
		ValidatorKey: "test-pubkey",
		Weight:       300,
		MlNodes: []*types.ModelMLNodes{
			{
				MlNodes: []*types.MLNodeInfo{
					{
						NodeId:             "node1",
						PocWeight:          100,
						TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
					},
					{
						NodeId:             "node2",
						PocWeight:          200,
						TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
					},
				},
			},
		},
	}

	member := NewEpochMemberFromActiveParticipant(p, 1, 0)

	// Should be 0 since no nodes available for confirmation PoC
	require.Equal(t, int64(0), member.ConfirmationWeight, "confirmation_weight should be 0 when all nodes preserved")
}

func TestGetAllGroupMembersPaginated_SinglePage(t *testing.T) {
	members := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "100"}},
		{Member: &group.Member{Address: "addr2", Weight: "200"}},
		{Member: &group.Member{Address: "addr3", Weight: "300"}},
	}

	mockGK := &mockGroupKeeper{
		fn: func(ctx context.Context, req *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
			return &group.QueryGroupMembersResponse{
				Members:    members,
				Pagination: &query.PageResponse{NextKey: nil},
			}, nil
		},
	}

	eg := &EpochGroup{
		GroupKeeper: mockGK,
		Logger:      &mockLogger{},
		GroupData: &types.EpochGroupData{
			EpochGroupId: 1,
		},
	}

	ctx := context.Background()
	result, err := eg.getAllGroupMembersPaginated(ctx, 1)

	require.NoError(t, err)
	require.Len(t, result, 3)
	require.Equal(t, "addr1", result[0].Member.Address)
	require.Equal(t, "addr2", result[1].Member.Address)
	require.Equal(t, "addr3", result[2].Member.Address)
}

func TestGetAllGroupMembersPaginated_MultiplePages(t *testing.T) {
	page1Members := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "100"}},
		{Member: &group.Member{Address: "addr2", Weight: "200"}},
	}

	page2Members := []*group.GroupMember{
		{Member: &group.Member{Address: "addr3", Weight: "300"}},
		{Member: &group.Member{Address: "addr4", Weight: "400"}},
	}

	page3Members := []*group.GroupMember{
		{Member: &group.Member{Address: "addr5", Weight: "500"}},
	}

	nextKey2 := []byte("key2")
	nextKey3 := []byte("key3")

	callCount := 0
	mockGK := &mockGroupKeeper{
		fn: func(ctx context.Context, req *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
			callCount++
			switch callCount {
			case 1:
				return &group.QueryGroupMembersResponse{
					Members:    page1Members,
					Pagination: &query.PageResponse{NextKey: nextKey2},
				}, nil
			case 2:
				return &group.QueryGroupMembersResponse{
					Members:    page2Members,
					Pagination: &query.PageResponse{NextKey: nextKey3},
				}, nil
			case 3:
				return &group.QueryGroupMembersResponse{
					Members:    page3Members,
					Pagination: &query.PageResponse{NextKey: nil},
				}, nil
			default:
				return nil, errors.New("unexpected call")
			}
		},
	}

	eg := &EpochGroup{
		GroupKeeper: mockGK,
		Logger:      &mockLogger{},
		GroupData: &types.EpochGroupData{
			EpochGroupId: 1,
		},
	}

	ctx := context.Background()
	result, err := eg.getAllGroupMembersPaginated(ctx, 1)

	require.NoError(t, err)
	require.Len(t, result, 5)
	require.Equal(t, "addr1", result[0].Member.Address)
	require.Equal(t, "addr2", result[1].Member.Address)
	require.Equal(t, "addr3", result[2].Member.Address)
	require.Equal(t, "addr4", result[3].Member.Address)
	require.Equal(t, "addr5", result[4].Member.Address)
}

func TestGetAllGroupMembersPaginated_EmptyResult(t *testing.T) {
	mockGK := &mockGroupKeeper{
		fn: func(ctx context.Context, req *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
			return &group.QueryGroupMembersResponse{
				Members:    []*group.GroupMember{},
				Pagination: &query.PageResponse{NextKey: nil},
			}, nil
		},
	}

	eg := &EpochGroup{
		GroupKeeper: mockGK,
		Logger:      &mockLogger{},
		GroupData: &types.EpochGroupData{
			EpochGroupId: 1,
		},
	}

	ctx := context.Background()
	result, err := eg.getAllGroupMembersPaginated(ctx, 1)

	require.NoError(t, err)
	require.Len(t, result, 0)
}

func TestGetAllGroupMembersPaginated_Over100Members(t *testing.T) {
	page1Members := make([]*group.GroupMember, 100)
	for i := 0; i < 100; i++ {
		page1Members[i] = &group.GroupMember{
			Member: &group.Member{Address: "addr" + string(rune(i)), Weight: "100"},
		}
	}

	page2Members := make([]*group.GroupMember, 50)
	for i := 0; i < 50; i++ {
		page2Members[i] = &group.GroupMember{
			Member: &group.Member{Address: "addr" + string(rune(100+i)), Weight: "100"},
		}
	}

	nextKey := []byte("page2key")

	callCount := 0
	mockGK := &mockGroupKeeper{
		fn: func(ctx context.Context, req *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
			callCount++
			if callCount == 1 {
				return &group.QueryGroupMembersResponse{
					Members:    page1Members,
					Pagination: &query.PageResponse{NextKey: nextKey},
				}, nil
			}
			return &group.QueryGroupMembersResponse{
				Members:    page2Members,
				Pagination: &query.PageResponse{NextKey: nil},
			}, nil
		},
	}

	eg := &EpochGroup{
		GroupKeeper: mockGK,
		Logger:      &mockLogger{},
		GroupData: &types.EpochGroupData{
			EpochGroupId: 1,
		},
	}

	ctx := context.Background()
	result, err := eg.getAllGroupMembersPaginated(ctx, 1)

	require.NoError(t, err)
	require.Len(t, result, 150)
}

func TestGetGroupMembers_UsesPagination(t *testing.T) {
	members := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "100"}},
		{Member: &group.Member{Address: "addr2", Weight: "200"}},
	}

	mockGK := &mockGroupKeeper{
		fn: func(ctx context.Context, req *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
			return &group.QueryGroupMembersResponse{
				Members:    members,
				Pagination: &query.PageResponse{NextKey: nil},
			}, nil
		},
	}

	eg := &EpochGroup{
		GroupKeeper: mockGK,
		Logger:      &mockLogger{},
		GroupData: &types.EpochGroupData{
			EpochGroupId: 42,
		},
	}

	ctx := context.Background()
	result, err := eg.GetGroupMembers(ctx)

	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Equal(t, "addr1", result[0].Member.Address)
	require.Equal(t, "addr2", result[1].Member.Address)
}
