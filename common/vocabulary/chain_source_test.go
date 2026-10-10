package vocabulary

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

type fakeEpochGroupData struct {
	resp *inferencetypes.QueryGetEpochGroupDataResponse
	err  error
	got  *inferencetypes.QueryGetEpochGroupDataRequest
}

func (f *fakeEpochGroupData) EpochGroupData(_ context.Context, req *inferencetypes.QueryGetEpochGroupDataRequest, _ ...grpc.CallOption) (*inferencetypes.QueryGetEpochGroupDataResponse, error) {
	f.got = req
	return f.resp, f.err
}

func TestChainModelSource_ReadsEpochModelSnapshot(t *testing.T) {
	query := &fakeEpochGroupData{resp: &inferencetypes.QueryGetEpochGroupDataResponse{
		EpochGroupData: inferencetypes.EpochGroupData{ModelSnapshot: &inferencetypes.Model{HfRepo: "org/model", HfCommit: "abc123"}},
	}}
	repo, commit, err := ChainModelSource{Query: query}.GetModelSource(context.Background(), 7, "org/model-id")
	require.NoError(t, err)
	require.Equal(t, "org/model", repo)
	require.Equal(t, "abc123", commit)
	require.EqualValues(t, 7, query.got.EpochIndex)
	require.Equal(t, "org/model-id", query.got.ModelId)

	_, _, err = ChainModelSource{Query: &fakeEpochGroupData{resp: &inferencetypes.QueryGetEpochGroupDataResponse{}}}.GetModelSource(context.Background(), 7, "m")
	require.ErrorContains(t, err, "model snapshot not found")
	_, _, err = ChainModelSource{Query: &fakeEpochGroupData{err: errors.New("down")}}.GetModelSource(context.Background(), 7, "m")
	require.ErrorContains(t, err, "down")
}
