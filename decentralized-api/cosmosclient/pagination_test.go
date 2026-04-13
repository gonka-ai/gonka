package cosmosclient

import (
	"fmt"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"
)

func TestGetAllWithPagination_SinglePage(t *testing.T) {
	// Mock data
	testItems := []string{"item1", "item2", "item3"}

	queryFunc := func(pageReq *query.PageRequest) ([]string, *query.PageResponse, error) {
		require.NotNil(t, pageReq)
		require.Equal(t, uint64(1000), pageReq.Limit)
		require.Nil(t, pageReq.Key)

		return testItems, &query.PageResponse{
			NextKey: nil,
			Total:   uint64(len(testItems)),
		}, nil
	}

	result, err := GetAllWithPagination(queryFunc)
	require.NoError(t, err)
	require.Equal(t, testItems, result)
}

func TestGetAllWithPagination_MultiplePages(t *testing.T) {
	// Mock data split across pages
	page1Items := []string{"item1", "item2"}
	page2Items := []string{"item3", "item4"}
	page3Items := []string{"item5"}

	callCount := 0
	queryFunc := func(pageReq *query.PageRequest) ([]string, *query.PageResponse, error) {
		require.NotNil(t, pageReq)
		require.Equal(t, uint64(1000), pageReq.Limit)

		callCount++
		switch callCount {
		case 1:
			require.Nil(t, pageReq.Key)
			return page1Items, &query.PageResponse{
				NextKey: []byte("key2"),
				Total:   5,
			}, nil
		case 2:
			require.Equal(t, []byte("key2"), pageReq.Key)
			return page2Items, &query.PageResponse{
				NextKey: []byte("key3"),
				Total:   5,
			}, nil
		case 3:
			require.Equal(t, []byte("key3"), pageReq.Key)
			return page3Items, &query.PageResponse{
				NextKey: nil,
				Total:   5,
			}, nil
		default:
			t.Fatalf("Unexpected call count: %d", callCount)
			return nil, nil, nil
		}
	}

	result, err := GetAllWithPagination(queryFunc)
	require.NoError(t, err)
	require.Equal(t, 3, callCount)

	expected := append(page1Items, page2Items...)
	expected = append(expected, page3Items...)
	require.Equal(t, expected, result)
}

func TestGetAllWithPagination_ErrorHandling(t *testing.T) {
	queryFunc := func(pageReq *query.PageRequest) ([]string, *query.PageResponse, error) {
		return nil, nil, fmt.Errorf("query error")
	}

	result, err := GetAllWithPagination(queryFunc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to fetch page (items so far: 0)")
	require.Contains(t, err.Error(), "query error")
	require.Nil(t, result)
}

func TestGetAllWithPagination_ErrorOnSecondPage(t *testing.T) {
	callCount := 0
	queryFunc := func(pageReq *query.PageRequest) ([]string, *query.PageResponse, error) {
		callCount++
		if callCount == 1 {
			return []string{"item1"}, &query.PageResponse{
				NextKey: []byte("key2"),
				Total:   2,
			}, nil
		}
		return nil, nil, fmt.Errorf("second page error")
	}

	result, err := GetAllWithPagination(queryFunc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to fetch page (items so far: 1)")
	require.Contains(t, err.Error(), "second page error")
	require.Nil(t, result)
}

func TestGetAllWithPagination_EmptyResult(t *testing.T) {
	queryFunc := func(pageReq *query.PageRequest) ([]string, *query.PageResponse, error) {
		return []string{}, &query.PageResponse{
			NextKey: nil,
			Total:   0,
		}, nil
	}

	result, err := GetAllWithPagination(queryFunc)
	require.NoError(t, err)
	require.Empty(t, result)
}

func TestGetAllWithPagination_NilPagination(t *testing.T) {
	testItems := []string{"item1", "item2"}

	queryFunc := func(pageReq *query.PageRequest) ([]string, *query.PageResponse, error) {
		return testItems, nil, nil
	}

	result, err := GetAllWithPagination(queryFunc)
	require.NoError(t, err)
	require.Equal(t, testItems, result)
}
