package queryapitest

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/golang/protobuf/proto"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestEpochParticipantsZeroHeightSkipsValidatorLookup covers the case where a
// stored ActiveParticipants blob has CreatedAtBlockHeight == 0 (blobs written
// before the created_at_block_height proto field existed decode with the zero
// value). CometBFT rejects GetValidatorSetByHeight for
// height <= 0 with "height must be greater than 0, but got 0" — the handler
// must treat that as non-fatal and still return the participants data instead
// of surfacing a 500.
func TestEpochParticipantsZeroHeightSkipsValidatorLookup(t *testing.T) {
	srv := &stubEpochParticipantsZeroHeightComet{}
	inf := &stubEpochParticipantsInference{}
	h := handlersWithInferenceAndComet(t, inf, srv)

	ctx, rec := echoContext(t, http.MethodGet, "/v1/epochs/1/participants")
	require.NoError(t, h.GetEpochParticipants(ctx, "1"))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.False(t, srv.validatorSetCalled, "GetValidatorSetByHeight must not be called for height <= 0")

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	assert.Contains(t, got, "active_participants")
	assert.NotEmpty(t, got["active_participants_bytes"])

	validators, ok := got["validators"].([]any)
	require.True(t, ok, "validators should still be a JSON array, body=%s", rec.Body.String())
	assert.Empty(t, validators)
}

type stubEpochParticipantsZeroHeightComet struct {
	cmtservice.UnimplementedServiceServer
	value              []byte
	validatorSetCalled bool
}

func (s *stubEpochParticipantsZeroHeightComet) ABCIQuery(_ context.Context, req *cmtservice.ABCIQueryRequest) (*cmtservice.ABCIQueryResponse, error) {
	if s.value == nil {
		ap := inferencetypes.ActiveParticipants{
			CreatedAtBlockHeight: 0,
			EpochGroupId:         1,
		}
		var err error
		s.value, err = proto.Marshal(&ap)
		if err != nil {
			return nil, err
		}
	}

	if req.Prove {
		return &cmtservice.ABCIQueryResponse{
			Code:  0,
			Value: s.value,
			ProofOps: &cmtservice.ProofOps{
				Ops: []cmtservice.ProofOp{{Type: "iavl:v", Key: []byte("key"), Data: []byte("value")}},
			},
			Height: 0,
		}, nil
	}
	return &cmtservice.ABCIQueryResponse{Code: 0, Value: s.value}, nil
}

func (s *stubEpochParticipantsZeroHeightComet) GetBlockByHeight(_ context.Context, req *cmtservice.GetBlockByHeightRequest) (*cmtservice.GetBlockByHeightResponse, error) {
	return &cmtservice.GetBlockByHeightResponse{
		SdkBlock: &cmtservice.Block{
			Header: cmtservice.Header{
				Height:  req.Height,
				ChainID: "gonka-test",
				AppHash: []byte("apphash"),
			},
		},
	}, nil
}

func (s *stubEpochParticipantsZeroHeightComet) GetValidatorSetByHeight(_ context.Context, _ *cmtservice.GetValidatorSetByHeightRequest) (*cmtservice.GetValidatorSetByHeightResponse, error) {
	s.validatorSetCalled = true
	// Mirrors the real CometBFT node's rejection of non-positive heights, so
	// this test fails loudly (instead of silently passing) if the handler
	// regresses and calls this for a zero height.
	return nil, status.Error(codes.InvalidArgument, "height must be greater than 0, but got 0")
}
