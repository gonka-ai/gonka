package transport_test

import (
	"encoding/json"
	"testing"

	jsonfast "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/heightsync"
	"devshard/transport"
	"devshard/types"
)

func TestEnvelope_OriginatorFields_RoundTrip(t *testing.T) {
	hs := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         99,
		MainnetBlockHashHex:   "aabb",
		TimestampUnixMs:       1000,
		Direction:             "request",
		OriginatorSenderID:    "gonka1origin",
		OriginatorTimestampMs: 900,
	}
	req := transport.InferenceRequest{Nonce: 2}

	raw, err := transport.MarshalWrappedInferenceRequest(transport.CurrentInferenceEnvelopeSchemaVersion, hs, req)
	require.NoError(t, err)

	var env types.InferenceRequestEnvelope
	require.NoError(t, proto.Unmarshal(raw, &env))
	require.NotNil(t, env.GetHeightSync())
	require.Equal(t, hs.OriginatorSenderID, env.GetHeightSync().GetOriginatorSenderId())
	require.Equal(t, hs.OriginatorTimestampMs, env.GetHeightSync().GetOriginatorTimestampUnixMs())

	got, err := transport.UnwrapInferenceRequestBody(raw)
	require.NoError(t, err)
	want := *hs
	want.ChainID = ""
	require.Equal(t, &want, got.HeightSync)

	hsResp := *hs
	hsResp.Direction = "response"
	rawResp, err := transport.MarshalWrappedInferenceResponse(transport.CurrentInferenceEnvelopeSchemaVersion, &hsResp, transport.InferenceResponse{Nonce: 2})
	require.NoError(t, err)
	gotResp, err := transport.UnwrapInferenceResponseBody(rawResp)
	require.NoError(t, err)
	wantResp := hsResp
	wantResp.ChainID = ""
	require.Equal(t, &wantResp, gotResp.HeightSync)
}

func TestMarshalWrappedInferenceRequest_RoundTrip_Anchor(t *testing.T) {
	hs := &heightsync.HeightSyncSection{
		ChainID:             "gonka-testenv-1",
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       1234,
		MainnetBlockHashHex: "deadbeef",
		TimestampUnixMs:     1714975123456,
		Direction:           "request",
	}
	req := transport.InferenceRequest{
		Nonce:  7,
		Diffs:  nil,
		Stream: true,
	}

	raw, err := transport.MarshalWrappedInferenceRequest(transport.CurrentInferenceEnvelopeSchemaVersion, hs, req)
	require.NoError(t, err)
	require.False(t, startsWithJSONObject(raw), "protobuf envelope must not look like raw JSON object")

	got, err := transport.UnwrapInferenceRequestBody(raw)
	require.NoError(t, err)
	require.False(t, got.WholeBodyJSON)
	require.Equal(t, transport.CurrentInferenceEnvelopeSchemaVersion, got.SchemaVersion)
	want := *hs
	want.ChainID = ""
	require.Equal(t, &want, got.HeightSync)
	require.Equal(t, req, got.Request)
}

func TestMarshalWrappedInferenceRequest_RoundTrip_Omit(t *testing.T) {
	req := transport.InferenceRequest{Nonce: 3}

	raw, err := transport.MarshalWrappedInferenceRequest(transport.CurrentInferenceEnvelopeSchemaVersion, nil, req)
	require.NoError(t, err)

	var env types.InferenceRequestEnvelope
	require.NoError(t, proto.Unmarshal(raw, &env))
	require.Nil(t, env.GetHeightSync())

	got, err := transport.UnwrapInferenceRequestBody(raw)
	require.NoError(t, err)
	require.False(t, got.WholeBodyJSON)
	require.Nil(t, got.HeightSync)
	require.Equal(t, req, got.Request)
}

func TestUnwrapInferenceRequestBody_LegacyWholeBodyJSON_OmitsHeightSync(t *testing.T) {
	req := transport.InferenceRequest{
		Nonce: 42,
		Diffs: []transport.DiffJSON{{Nonce: 1, Txs: []byte{1, 2, 3}}},
	}
	raw, err := jsonfast.Marshal(req)
	require.NoError(t, err)

	got, err := transport.UnwrapInferenceRequestBody(raw)
	require.NoError(t, err)
	require.True(t, got.WholeBodyJSON)
	require.Nil(t, got.HeightSync)
	require.Equal(t, 0, got.SchemaVersion)
	require.Equal(t, req, got.Request)
}

func TestUnwrapInferenceRequestBody_Legacy_StdlibJSON(t *testing.T) {
	req := transport.InferenceRequest{Nonce: 1}
	raw, err := json.Marshal(req)
	require.NoError(t, err)

	got, err := transport.UnwrapInferenceRequestBody(raw)
	require.NoError(t, err)
	require.True(t, got.WholeBodyJSON)
	require.Nil(t, got.HeightSync)
	require.Equal(t, req, got.Request)
}

func TestUnwrapInferenceRequestBody_DeprecatedJSONEnvelopeRejected(t *testing.T) {
	const blob = `{"schema_version":1,"message":{"nonce":5},"height_sync":{}}`
	_, err := transport.UnwrapInferenceRequestBody([]byte(blob))
	require.Error(t, err)
	require.Contains(t, err.Error(), "deprecated JSON inference envelope")
}

func TestMarshalWrappedInferenceResponse_RoundTrip(t *testing.T) {
	hs := &heightsync.HeightSyncSection{
		ChainID:             "gonka-testenv-1",
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       50,
		MainnetBlockHashHex: "00ff",
		TimestampUnixMs:     2,
		Direction:           "response",
	}
	resp := transport.InferenceResponse{Nonce: 9, Receipt: []byte{7}}

	raw, err := transport.MarshalWrappedInferenceResponse(transport.CurrentInferenceEnvelopeSchemaVersion, hs, resp)
	require.NoError(t, err)

	got, err := transport.UnwrapInferenceResponseBody(raw)
	require.NoError(t, err)
	require.False(t, got.WholeBodyJSON)
	want := *hs
	want.ChainID = ""
	require.Equal(t, &want, got.HeightSync)
	require.Equal(t, resp, got.Response)
}

func TestUnwrapInferenceResponseBody_LegacyWholeBodyJSON_OmitsHeightSync(t *testing.T) {
	resp := transport.InferenceResponse{Nonce: 1, StateHash: []byte{9}}
	raw, err := jsonfast.Marshal(resp)
	require.NoError(t, err)

	got, err := transport.UnwrapInferenceResponseBody(raw)
	require.NoError(t, err)
	require.True(t, got.WholeBodyJSON)
	require.Nil(t, got.HeightSync)
	require.Equal(t, resp, got.Response)
}

func startsWithJSONObject(b []byte) bool {
	for i := range b {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

func TestUnwrapInferenceRequestBody_OversizedOriginSigDropped(t *testing.T) {
	inner, err := jsonfast.Marshal(transport.InferenceRequest{Nonce: 1})
	require.NoError(t, err)
	env := &types.InferenceRequestEnvelope{
		SchemaVersion:        int32(transport.CurrentInferenceEnvelopeSchemaVersion),
		InferenceRequestJson: inner,
		HeightSync: &types.InferenceHeightSyncSection{
			ProofType:           types.InferenceHeightSyncProofType_INFERENCE_HEIGHT_SYNC_PROOF_TYPE_HEIGHT_ANCHOR_V1,
			MainnetHeight:       11,
			MainnetBlockHashHex: "aa",
			SenderSignature:     make([]byte, heightsync.MaxOriginSignatureBytes+1),
		},
	}
	raw, err := proto.Marshal(env)
	require.NoError(t, err)
	got, err := transport.UnwrapInferenceRequestBody(raw)
	require.NoError(t, err)
	require.NotNil(t, got.HeightSync)
	require.Empty(t, got.HeightSync.SenderSignature, "oversized field-8 is dropped at unwrap")
}
