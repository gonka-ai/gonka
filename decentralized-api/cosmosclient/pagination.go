package cosmosclient

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/types/query"
)

func GetAllWithPagination[T any](
	queryFunc func(*query.PageRequest) ([]T, *query.PageResponse, error),
) ([]T, error) {
	var allItems []T
	var nextKey []byte

	for {
		req := &query.PageRequest{
			Key:   nextKey,
			Limit: 1000,
		}

		items, pagination, err := queryFunc(req)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch page (items so far: %d): %w", len(allItems), err)
		}

		allItems = append(allItems, items...)

		if pagination == nil || len(pagination.NextKey) == 0 {
			break
		}
		nextKey = pagination.NextKey
	}

	return allItems, nil
}
