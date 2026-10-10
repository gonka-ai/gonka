package vocabulary

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// EpochGroupDataQuery is the chain query ChainModelSource reads the model snapshot from.
type EpochGroupDataQuery interface {
	EpochGroupData(context.Context, *inferencetypes.QueryGetEpochGroupDataRequest, ...grpc.CallOption) (*inferencetypes.QueryGetEpochGroupDataResponse, error)
}

// ChainModelSource reads the Hugging Face repo and commit from the epoch's model snapshot on chain.
type ChainModelSource struct {
	Query EpochGroupDataQuery
}

func (s ChainModelSource) GetModelSource(ctx context.Context, epochID uint64, modelID string) (hfRepo, hfCommit string, err error) {
	resp, err := s.Query.EpochGroupData(ctx, &inferencetypes.QueryGetEpochGroupDataRequest{
		EpochIndex: epochID,
		ModelId:    modelID,
	})
	if err != nil {
		return "", "", fmt.Errorf("EpochGroupData epoch=%d model=%s: %w", epochID, modelID, err)
	}
	if resp == nil || resp.EpochGroupData.ModelSnapshot == nil {
		return "", "", fmt.Errorf("model snapshot not found for epoch %d model %s", epochID, modelID)
	}
	return resp.EpochGroupData.ModelSnapshot.HfRepo, resp.EpochGroupData.ModelSnapshot.HfCommit, nil
}
