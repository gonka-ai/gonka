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
	models := []string{"model-a"}
	mlNodes := []*types.ModelMLNodes{
		{
			MlNodes: []*types.MLNodeInfo{
				{NodeId: "node1", PocWeight: 100},
				{NodeId: "node2", PocWeight: 200},
			},
		},
	}

	// At epoch formation time no episode-scoped preserved snapshot exists yet, so this
	// helper returns the full participant ML-node weight as the initial confirmation
	// weight. Per-event slashing in checkConfirmationSlashing refines the value later.
	weight := calculatePocParticipatingNodesWeight(models, mlNodes, nil)
	require.Equal(t, int64(300), weight)
}

func TestCalculatePocParticipatingNodesWeight_NoneServeInference(t *testing.T) {
	models := []string{"model-a"}
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

	weight := calculatePocParticipatingNodesWeight(models, mlNodes, nil)

	// Should be sum of all weights since all have POC_SLOT=false
	require.Equal(t, int64(300), weight)
}

func TestCalculatePocParticipatingNodesWeight_SumsAllNodes(t *testing.T) {
	models := []string{"model-a"}
	mlNodes := []*types.ModelMLNodes{
		{
			MlNodes: []*types.MLNodeInfo{
				{NodeId: "node1", PocWeight: 100},
				{NodeId: "node2", PocWeight: 200},
				{NodeId: "node3", PocWeight: 300},
				{NodeId: "node4", PocWeight: 400},
			},
		},
	}

	weight := calculatePocParticipatingNodesWeight(models, mlNodes, nil)
	require.Equal(t, int64(1000), weight)
}

func TestCalculatePocParticipatingNodesWeight_IgnoresTimeslotShape(t *testing.T) {
	models := []string{"model-a"}
	mlNodes := []*types.ModelMLNodes{
		{
			MlNodes: []*types.MLNodeInfo{
				{NodeId: "node1", PocWeight: 100, TimeslotAllocation: []bool{}},
				{NodeId: "node2", PocWeight: 200, TimeslotAllocation: []bool{true}},
				{NodeId: "node3", PocWeight: 300, TimeslotAllocation: []bool{true, false}},
			},
		},
	}

	// The deprecated TimeslotAllocation field has no semantic effect on this helper
	// anymore; every non-nil node contributes its PocWeight.
	weight := calculatePocParticipatingNodesWeight(models, mlNodes, nil)
	require.Equal(t, int64(600), weight)
}

func TestCalculatePocParticipatingNodesWeight_NilNodes(t *testing.T) {
	models := []string{"model-a", "model-b"}
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

	weight := calculatePocParticipatingNodesWeight(models, mlNodes, nil)

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
	models := []string{"model-a", "model-b"}
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

	weight := calculatePocParticipatingNodesWeight(models, mlNodes, nil)

	// Should sum across all model arrays (coeff=1.0 for each when nil)
	require.Equal(t, int64(300), weight)
}

// Test confirmation weight initialization when creating EpochMember
func TestNewEpochMemberFromActiveParticipant_ConfirmationWeightInitialization(t *testing.T) {
	p := &types.ActiveParticipant{
		Index:        "test-participant",
		ValidatorKey: "test-pubkey",
		Weight:       450,
		Models:       []string{"model-a"},
		MlNodes: []*types.ModelMLNodes{
			{
				MlNodes: []*types.MLNodeInfo{
					{NodeId: "node1", PocWeight: 100},
					{NodeId: "node2", PocWeight: 200},
					{NodeId: "node3", PocWeight: 150},
				},
			},
		},
	}

	// Call with confirmationWeight = 0 to trigger initialization. No episode-scoped
	// preserved snapshot exists at epoch formation, so the initial value equals the
	// full ML-node weight; per-event confirmation slashing refines it later.
	member := NewEpochMemberFromActiveParticipant(p, 1, 0, nil)

	require.Equal(t, int64(450), member.ConfirmationWeight, "initial confirmation_weight should equal full ML-node weight")
	require.Equal(t, int64(450), member.Weight, "total weight should remain unchanged")
}

func TestNewEpochMemberFromActiveParticipant_ConfirmationWeightProvided(t *testing.T) {
	// Create ActiveParticipant with mixed timeslot allocations
	p := &types.ActiveParticipant{
		Index:        "test-participant",
		ValidatorKey: "test-pubkey",
		Weight:       450,
		Models:       []string{"model-a"},
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
	member := NewEpochMemberFromActiveParticipant(p, 1, 180, nil)

	// Should use the provided value (180), not recalculate (which would be 250)
	require.Equal(t, int64(180), member.ConfirmationWeight, "confirmation_weight should use provided value")
}

func TestNewEpochMemberFromActiveParticipant_EmptyMLNodes(t *testing.T) {
	// Participant with no ML node weight should initialize confirmation_weight to 0.
	p := &types.ActiveParticipant{
		Index:        "test-participant",
		ValidatorKey: "test-pubkey",
		Weight:       300,
		Models:       []string{"model-a"},
		MlNodes:      []*types.ModelMLNodes{{MlNodes: nil}},
	}

	member := NewEpochMemberFromActiveParticipant(p, 1, 0, nil)
	require.Equal(t, int64(0), member.ConfirmationWeight, "confirmation_weight should be 0 when no ML nodes contribute weight")
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
