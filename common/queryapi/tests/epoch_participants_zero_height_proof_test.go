package queryapitest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmversion "github.com/cometbft/cometbft/proto/tendermint/version"
	cmtversion "github.com/cometbft/cometbft/version"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/golang/protobuf/proto"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A stored ActiveParticipants blob can carry CreatedAtBlockHeight == 0 (blobs
// written before the field existed decode with the zero value). The handler
// anchors its proof at that height and verifies it against block
// CreatedAtBlockHeight+1, so at height 0 it would prove against the latest
// state root and check that against block 1 — a bundle that can never verify.
// Both calls must be skipped so the response carries no proof rather than an
// inconsistent one.
func TestEpochParticipantsZeroHeightSkipsProofAnchoring(t *testing.T) {
	srv := &stubZeroHeightProofComet{}
	h := handlersWithInferenceAndComet(t, &stubEpochParticipantsInference{}, srv)

	ctx, rec := echoContext(t, http.MethodGet, "/v1/epochs/1/participants")
	require.NoError(t, h.GetEpochParticipants(ctx, "1"))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	assert.False(t, srv.proveQueried, "ABCIQuery with Prove must not run for height <= 0")
	assert.False(t, srv.blockQueried, "GetBlockByHeight must not run for height <= 0")

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	assert.NotContains(t, got, "proof_ops", "a proof anchored at the wrong height must not be returned")
	assert.NotContains(t, got, "block", "block 1 is not the creation block and must not be returned")

	// The participants themselves still come back, from the first query.
	assert.Contains(t, got, "active_participants")
	assert.NotEmpty(t, got["active_participants_bytes"])
}

type stubZeroHeightProofComet struct {
	cmtservice.UnimplementedServiceServer
	value        []byte
	proveQueried bool
	blockQueried bool
}

func (s *stubZeroHeightProofComet) ABCIQuery(_ context.Context, req *cmtservice.ABCIQueryRequest) (*cmtservice.ABCIQueryResponse, error) {
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
		s.proveQueried = true
		// What a node actually serves at height 0: the latest state, with a
		// proof anchored to the latest root.
		return &cmtservice.ABCIQueryResponse{
			Code:  0,
			Value: s.value,
			ProofOps: &cmtservice.ProofOps{
				Ops: []cmtservice.ProofOp{{Type: "iavl:v", Key: []byte("key"), Data: []byte("value")}},
			},
			Height: 100,
		}, nil
	}
	return &cmtservice.ABCIQueryResponse{Code: 0, Value: s.value}, nil
}

func (s *stubZeroHeightProofComet) GetBlockByHeight(_ context.Context, req *cmtservice.GetBlockByHeightRequest) (*cmtservice.GetBlockByHeightResponse, error) {
	s.blockQueried = true
	return &cmtservice.GetBlockByHeightResponse{
		// Block 1 exists on any live chain, so the unguarded path gets a real
		// block back and puts it in the response as if it were the creation block.
		Block: &tmproto.Block{
			Header: tmproto.Header{
				Version:         tmversion.Consensus{Block: cmtversion.BlockProtocol},
				Height:          req.Height,
				ChainID:         "gonka-test",
				AppHash:         []byte("apphash"),
				ProposerAddress: bytes.Repeat([]byte{0xAB}, 20),
			},
		},
		SdkBlock: &cmtservice.Block{
			Header: cmtservice.Header{
				Height:  req.Height,
				ChainID: "gonka-test",
				AppHash: []byte("apphash"),
			},
		},
	}, nil
}

func (s *stubZeroHeightProofComet) GetValidatorSetByHeight(_ context.Context, _ *cmtservice.GetValidatorSetByHeightRequest) (*cmtservice.GetValidatorSetByHeightResponse, error) {
	return &cmtservice.GetValidatorSetByHeightResponse{Validators: nil}, nil
}
