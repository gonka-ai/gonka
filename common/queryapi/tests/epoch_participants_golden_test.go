package queryapitest

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"bytes"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmversion "github.com/cometbft/cometbft/proto/tendermint/version"
	cmtversion "github.com/cometbft/cometbft/version"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cosmosed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/golang/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func TestEpochParticipantsJSONMatchesDapiGolden(t *testing.T) {
	golden := loadEpochParticipantsGolden(t)

	srv := &stubEpochParticipantsComet{}
	inf := &stubEpochParticipantsInference{}
	h := handlersWithInferenceAndComet(t, inf, srv)

	ctx, rec := echoContext(t, http.MethodGet, "/v1/epochs/1/participants")
	require.NoError(t, h.GetEpochParticipants(ctx, "1"))
	require.Equal(t, http.StatusOK, rec.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	assert.Equal(t, goldenKeys(golden), goldenKeys(got))

	// Proof-bearing contract: bytes + proof_ops must be present on real handler output.
	assert.NotEmpty(t, got["active_participants_bytes"])
	assert.NotNil(t, got["proof_ops"])
	assert.Contains(t, got, "validators")
	assert.Contains(t, got, "excluded_participants")

	ap, ok := got["active_participants"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(100), ap["created_at_block_height"], "int64 fields must stay JSON numbers")
	require.Equal(t, float64(1), ap["epoch_group_id"])

	// Legacy dapi block shape: native comet types.Block via encoding/json —
	// numeric height, hex-string app_hash (not protojson string/base64).
	blk, ok := got["block"].(map[string]any)
	require.True(t, ok, "block must be present")
	hdr, ok := blk["header"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(101), hdr["height"], "block height must be a JSON number")
	appHash, ok := hdr["app_hash"].(string)
	require.True(t, ok)
	require.Regexp(t, `^[0-9A-F]+$`, appHash, "app_hash must be hex-uppercase (comet HexBytes)")
}

func TestEpochParticipantsJSONEncodesValidatorsWithPubKeyAny(t *testing.T) {
	srv := &stubEpochParticipantsComet{withValidators: true}
	h := handlersWithInferenceAndComet(t, &stubEpochParticipantsInference{}, srv)

	ctx, rec := echoContext(t, http.MethodGet, "/v1/epochs/1/participants")
	require.NoError(t, h.GetEpochParticipants(ctx, "1"))
	require.Equal(t, http.StatusOK, rec.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	validators, ok := got["validators"].([]any)
	require.True(t, ok, "validators should be a JSON array, body=%s", rec.Body.String())
	require.Len(t, validators, 1)

	val, ok := validators[0].(map[string]any)
	require.True(t, ok)

	pubKey, ok := val["pub_key"].(string)
	require.True(t, ok, "pub_key must be a base64 string for testermint compatibility, got %T: %v", val["pub_key"], val["pub_key"])
	require.NotEmpty(t, pubKey)
	require.NotContains(t, pubKey, "@type")

	address, ok := val["address"].(string)
	require.True(t, ok)
	require.Regexp(t, `^[0-9A-F]+$`, address, "validator address must be hex-uppercase")

	require.Equal(t, float64(100), val["voting_power"], "voting_power must be a JSON number (legacy encoding/json shape)")
}

func loadEpochParticipantsGolden(t *testing.T) map[string]any {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(file), "testdata", "dapi_epoch_participants_golden.json")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var golden map[string]any
	require.NoError(t, json.Unmarshal(b, &golden))
	return golden
}

func goldenKeys(body map[string]any) []string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type stubEpochParticipantsInference struct {
	inferencetypes.UnimplementedQueryServer
}

func (s *stubEpochParticipantsInference) ExcludedParticipants(_ context.Context, _ *inferencetypes.QueryExcludedParticipantsRequest) (*inferencetypes.QueryExcludedParticipantsResponse, error) {
	return &inferencetypes.QueryExcludedParticipantsResponse{}, nil
}

type stubEpochParticipantsComet struct {
	cmtservice.UnimplementedServiceServer
	value          []byte
	withValidators bool
}

func (s *stubEpochParticipantsComet) ABCIQuery(_ context.Context, req *cmtservice.ABCIQueryRequest) (*cmtservice.ABCIQueryResponse, error) {
	if s.value == nil {
		ap := inferencetypes.ActiveParticipants{
			CreatedAtBlockHeight: 100,
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
			Height: 100,
		}, nil
	}
	return &cmtservice.ABCIQueryResponse{Code: 0, Value: s.value}, nil
}

func (s *stubEpochParticipantsComet) GetBlockByHeight(_ context.Context, req *cmtservice.GetBlockByHeightRequest) (*cmtservice.GetBlockByHeightResponse, error) {
	return &cmtservice.GetBlockByHeightResponse{
		// The handler encodes the comet proto block (converted to native
		// comet types.Block) for legacy dapi wire compatibility. Version and
		// ProposerAddress must satisfy comet Header.ValidateBasic, as real
		// blocks do.
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

func (s *stubEpochParticipantsComet) GetValidatorSetByHeight(_ context.Context, _ *cmtservice.GetValidatorSetByHeightRequest) (*cmtservice.GetValidatorSetByHeightResponse, error) {
	if !s.withValidators {
		return &cmtservice.GetValidatorSetByHeightResponse{Validators: nil}, nil
	}
	pk := cosmosed25519.PubKey{Key: []byte("01234567890123456789012345678901")}
	anyPK, err := codectypes.NewAnyWithValue(&pk)
	if err != nil {
		return nil, err
	}
	return &cmtservice.GetValidatorSetByHeightResponse{
		Validators: []*cmtservice.Validator{{
			Address:     "gonkavalcons1stubvalidator",
			PubKey:      anyPK,
			VotingPower: 100,
		}},
	}, nil
}
