package cosmosclient

import (
	"context"
	"fmt"
	"testing"

	"decentralized-api/utils"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockInferenceQueryClient mocks the inference query client
type MockInferenceQueryClient struct {
	mock.Mock
}

func (m *MockInferenceQueryClient) PartialUpgradeAll(ctx context.Context, req *types.QueryAllPartialUpgradeRequest) (*types.QueryAllPartialUpgradeResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(*types.QueryAllPartialUpgradeResponse), args.Error(1)
}

// MockInferenceCosmosClient for testing
type MockInferenceCosmosClientForTest struct {
	queryClient *MockInferenceQueryClient
	ctx         context.Context
}

func (m *MockInferenceCosmosClientForTest) NewInferenceQueryClient() *MockInferenceQueryClient {
	return m.queryClient
}

func TestGetPartialUpgrades_SinglePage(t *testing.T) {
	mockQueryClient := &MockInferenceQueryClient{}
	client := &MockInferenceCosmosClientForTest{
		queryClient: mockQueryClient,
		ctx:         context.Background(),
	}

	// Mock single page of upgrades
	upgrades := []types.PartialUpgrade{
		{Name: "upgrade1", Height: 100},
		{Name: "upgrade2", Height: 200},
		{Name: "upgrade3", Height: 300},
	}

	mockQueryClient.On("PartialUpgradeAll", mock.Anything, mock.MatchedBy(func(req *types.QueryAllPartialUpgradeRequest) bool {
		return req.Pagination != nil && req.Pagination.Limit == 1000 && req.Pagination.Key == nil
	})).Return(&types.QueryAllPartialUpgradeResponse{
		PartialUpgrade: upgrades,
		Pagination: &query.PageResponse{
			NextKey: nil,
			Total:   3,
		},
	}, nil)

	// Test the pagination wrapper logic directly
	allUpgrades, err := utils.GetAllWithPagination(func(pageReq *query.PageRequest) ([]types.PartialUpgrade, *query.PageResponse, error) {
		resp, err := mockQueryClient.PartialUpgradeAll(client.ctx, &types.QueryAllPartialUpgradeRequest{Pagination: pageReq})
		if err != nil {
			return nil, nil, err
		}
		return resp.PartialUpgrade, resp.Pagination, nil
	})

	require.NoError(t, err)
	require.Len(t, allUpgrades, 3)
	require.Equal(t, "upgrade1", allUpgrades[0].Name)
	require.Equal(t, "upgrade2", allUpgrades[1].Name)
	require.Equal(t, "upgrade3", allUpgrades[2].Name)

	// Verify response structure
	response := &types.QueryAllPartialUpgradeResponse{
		PartialUpgrade: allUpgrades,
		Pagination:     &query.PageResponse{Total: uint64(len(allUpgrades))},
	}
	require.Equal(t, uint64(3), response.Pagination.Total)
	require.Equal(t, upgrades, response.PartialUpgrade)

	mockQueryClient.AssertExpectations(t)
}

func TestGetPartialUpgrades_MultiplePages(t *testing.T) {
	mockQueryClient := &MockInferenceQueryClient{}
	client := &MockInferenceCosmosClientForTest{
		queryClient: mockQueryClient,
		ctx:         context.Background(),
	}

	// Mock multiple pages of upgrades
	page1Upgrades := make([]types.PartialUpgrade, 50)
	page2Upgrades := make([]types.PartialUpgrade, 75)

	for i := 0; i < 50; i++ {
		page1Upgrades[i] = types.PartialUpgrade{
			Name:   fmt.Sprintf("upgrade%d", i),
			Height: uint64(100 + i),
		}
	}

	for i := 0; i < 75; i++ {
		page2Upgrades[i] = types.PartialUpgrade{
			Name:   fmt.Sprintf("upgrade%d", i+50),
			Height: uint64(150 + i),
		}
	}

	// First page
	mockQueryClient.On("PartialUpgradeAll", mock.Anything, mock.MatchedBy(func(req *types.QueryAllPartialUpgradeRequest) bool {
		return req.Pagination != nil && req.Pagination.Limit == 1000 && req.Pagination.Key == nil
	})).Return(&types.QueryAllPartialUpgradeResponse{
		PartialUpgrade: page1Upgrades,
		Pagination: &query.PageResponse{
			NextKey: []byte("next_key"),
			Total:   125,
		},
	}, nil)

	// Second page
	mockQueryClient.On("PartialUpgradeAll", mock.Anything, mock.MatchedBy(func(req *types.QueryAllPartialUpgradeRequest) bool {
		return req.Pagination != nil && req.Pagination.Limit == 1000 && string(req.Pagination.Key) == "next_key"
	})).Return(&types.QueryAllPartialUpgradeResponse{
		PartialUpgrade: page2Upgrades,
		Pagination: &query.PageResponse{
			NextKey: nil,
			Total:   125,
		},
	}, nil)

	// Test the pagination wrapper logic
	allUpgrades, err := utils.GetAllWithPagination(func(pageReq *query.PageRequest) ([]types.PartialUpgrade, *query.PageResponse, error) {
		resp, err := mockQueryClient.PartialUpgradeAll(client.ctx, &types.QueryAllPartialUpgradeRequest{Pagination: pageReq})
		if err != nil {
			return nil, nil, err
		}
		return resp.PartialUpgrade, resp.Pagination, nil
	})

	require.NoError(t, err)
	require.Len(t, allUpgrades, 125)

	// Verify first few and last few items
	require.Equal(t, "upgrade0", allUpgrades[0].Name)
	require.Equal(t, "upgrade49", allUpgrades[49].Name)
	require.Equal(t, "upgrade50", allUpgrades[50].Name)
	require.Equal(t, "upgrade124", allUpgrades[124].Name)

	mockQueryClient.AssertExpectations(t)
}

func TestGetPartialUpgrades_ErrorHandling(t *testing.T) {
	mockQueryClient := &MockInferenceQueryClient{}
	client := &MockInferenceCosmosClientForTest{
		queryClient: mockQueryClient,
		ctx:         context.Background(),
	}

	// Mock error on first page
	mockQueryClient.On("PartialUpgradeAll", mock.Anything, mock.Anything).Return(
		(*types.QueryAllPartialUpgradeResponse)(nil), fmt.Errorf("query failed"))

	// Test the pagination wrapper logic
	allUpgrades, err := utils.GetAllWithPagination(func(pageReq *query.PageRequest) ([]types.PartialUpgrade, *query.PageResponse, error) {
		resp, err := mockQueryClient.PartialUpgradeAll(client.ctx, &types.QueryAllPartialUpgradeRequest{Pagination: pageReq})
		if err != nil {
			return nil, nil, err
		}
		return resp.PartialUpgrade, resp.Pagination, nil
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to fetch page (items so far: 0)")
	require.Contains(t, err.Error(), "query failed")
	require.Nil(t, allUpgrades)

	mockQueryClient.AssertExpectations(t)
}

func TestGetPartialUpgrades_ErrorOnSecondPage(t *testing.T) {
	mockQueryClient := &MockInferenceQueryClient{}
	client := &MockInferenceCosmosClientForTest{
		queryClient: mockQueryClient,
		ctx:         context.Background(),
	}

	// First page succeeds
	mockQueryClient.On("PartialUpgradeAll", mock.Anything, mock.MatchedBy(func(req *types.QueryAllPartialUpgradeRequest) bool {
		return req.Pagination != nil && req.Pagination.Key == nil
	})).Return(&types.QueryAllPartialUpgradeResponse{
		PartialUpgrade: []types.PartialUpgrade{
			{Name: "upgrade1", Height: 100},
		},
		Pagination: &query.PageResponse{
			NextKey: []byte("next_key"),
			Total:   2,
		},
	}, nil)

	// Second page fails
	mockQueryClient.On("PartialUpgradeAll", mock.Anything, mock.MatchedBy(func(req *types.QueryAllPartialUpgradeRequest) bool {
		return req.Pagination != nil && string(req.Pagination.Key) == "next_key"
	})).Return((*types.QueryAllPartialUpgradeResponse)(nil), fmt.Errorf("second page failed"))

	// Test the pagination wrapper logic
	allUpgrades, err := utils.GetAllWithPagination(func(pageReq *query.PageRequest) ([]types.PartialUpgrade, *query.PageResponse, error) {
		resp, err := mockQueryClient.PartialUpgradeAll(client.ctx, &types.QueryAllPartialUpgradeRequest{Pagination: pageReq})
		if err != nil {
			return nil, nil, err
		}
		return resp.PartialUpgrade, resp.Pagination, nil
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to fetch page (items so far: 1)")
	require.Contains(t, err.Error(), "second page failed")
	require.Nil(t, allUpgrades)

	mockQueryClient.AssertExpectations(t)
}

func TestGetPartialUpgrades_EmptyResult(t *testing.T) {
	mockQueryClient := &MockInferenceQueryClient{}
	client := &MockInferenceCosmosClientForTest{
		queryClient: mockQueryClient,
		ctx:         context.Background(),
	}

	// Mock empty response
	mockQueryClient.On("PartialUpgradeAll", mock.Anything, mock.Anything).Return(&types.QueryAllPartialUpgradeResponse{
		PartialUpgrade: []types.PartialUpgrade{},
		Pagination: &query.PageResponse{
			NextKey: nil,
			Total:   0,
		},
	}, nil)

	// Test the pagination wrapper logic
	allUpgrades, err := utils.GetAllWithPagination(func(pageReq *query.PageRequest) ([]types.PartialUpgrade, *query.PageResponse, error) {
		resp, err := mockQueryClient.PartialUpgradeAll(client.ctx, &types.QueryAllPartialUpgradeRequest{Pagination: pageReq})
		if err != nil {
			return nil, nil, err
		}
		return resp.PartialUpgrade, resp.Pagination, nil
	})

	require.NoError(t, err)
	require.Empty(t, allUpgrades)

	// Verify response structure
	response := &types.QueryAllPartialUpgradeResponse{
		PartialUpgrade: allUpgrades,
		Pagination:     &query.PageResponse{Total: uint64(len(allUpgrades))},
	}
	require.Equal(t, uint64(0), response.Pagination.Total)
	require.Empty(t, response.PartialUpgrade)

	mockQueryClient.AssertExpectations(t)
}
