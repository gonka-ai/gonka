package transport

import (
	"bytes"
	"fmt"

	json "github.com/goccy/go-json"
	"google.golang.org/protobuf/proto"

	"devshard/heightsync"
	"devshard/types"
)

// CurrentInferenceEnvelopeSchemaVersion is the schema_version field for protobuf envelopes.
const CurrentInferenceEnvelopeSchemaVersion = 1

// UnwrappedInferenceRequest is the result of decoding an inference POST body.
type UnwrappedInferenceRequest struct {
	SchemaVersion int
	HeightSync    *heightsync.HeightSyncSection
	Request       InferenceRequest
	// WholeBodyJSON is true when the body was legacy JSON (InferenceRequest only),
	// with no protobuf envelope. Height sync is treated as omitted (HeightSync nil).
	WholeBodyJSON bool
}

// UnwrappedInferenceResponse is the result of decoding an inference response body.
type UnwrappedInferenceResponse struct {
	SchemaVersion int
	HeightSync    *heightsync.HeightSyncSection
	Response      InferenceResponse
	// WholeBodyJSON is true when the body was legacy JSON (InferenceResponse only).
	// Height sync is treated as omitted (HeightSync nil).
	WholeBodyJSON bool
}

// MarshalWrappedInferenceRequest protobuf-encodes schema_version, optional height_sync, and nested JSON InferenceRequest bytes.
func MarshalWrappedInferenceRequest(schemaVersion int, hs *heightsync.HeightSyncSection, req InferenceRequest) ([]byte, error) {
	inner, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal inference request json: %w", err)
	}
	env := &types.InferenceRequestEnvelope{
		SchemaVersion:        int32(schemaVersion),
		HeightSync:           heightSyncToProto(hs),
		InferenceRequestJson: inner,
	}
	out, err := proto.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal inference request envelope: %w", err)
	}
	return out, nil
}

// MarshalWrappedInferenceResponse protobuf-encodes schema_version, optional height_sync, and nested JSON InferenceResponse bytes.
func MarshalWrappedInferenceResponse(schemaVersion int, hs *heightsync.HeightSyncSection, resp InferenceResponse) ([]byte, error) {
	inner, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("marshal inference response json: %w", err)
	}
	env := &types.InferenceResponseEnvelope{
		SchemaVersion:         int32(schemaVersion),
		HeightSync:            heightSyncToProto(hs),
		InferenceResponseJson: inner,
	}
	out, err := proto.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal inference response envelope: %w", err)
	}
	return out, nil
}

// UnwrapInferenceRequestBody decodes either:
//   - Legacy whole-body JSON InferenceRequest (existing clients): HeightSync nil, WholeBodyJSON true; or
//   - Protobuf InferenceRequestEnvelope (see proto/devshard/v1/inference_envelope.proto).
//
// Bodies whose first non-whitespace byte is '{' are parsed as JSON. A top-level
// "message" key is rejected (deprecated JSON envelope shape).
func UnwrapInferenceRequestBody(raw []byte) (UnwrappedInferenceRequest, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return UnwrappedInferenceRequest{}, fmt.Errorf("empty body")
	}

	if isLikelyJSONObject(raw) {
		if err := rejectDeprecatedJSONEnvelopeKeys(raw); err != nil {
			return UnwrappedInferenceRequest{}, err
		}
		var req InferenceRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return UnwrappedInferenceRequest{}, fmt.Errorf("decode legacy inference request json: %w", err)
		}
		return UnwrappedInferenceRequest{
			SchemaVersion: 0,
			HeightSync:    nil,
			Request:       req,
			WholeBodyJSON: true,
		}, nil
	}

	var env types.InferenceRequestEnvelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		return UnwrappedInferenceRequest{}, fmt.Errorf("decode inference request envelope protobuf: %w", err)
	}
	if len(env.InferenceRequestJson) == 0 {
		return UnwrappedInferenceRequest{}, fmt.Errorf("inference_request_json is empty")
	}
	var req InferenceRequest
	if err := json.Unmarshal(env.InferenceRequestJson, &req); err != nil {
		return UnwrappedInferenceRequest{}, fmt.Errorf("decode inference request json: %w", err)
	}
	return UnwrappedInferenceRequest{
		SchemaVersion: int(env.SchemaVersion),
		HeightSync:    heightSyncFromProto(env.HeightSync),
		Request:       req,
		WholeBodyJSON: false,
	}, nil
}

// UnwrapInferenceResponseBody decodes legacy whole-body JSON InferenceResponse or protobuf InferenceResponseEnvelope.
func UnwrapInferenceResponseBody(raw []byte) (UnwrappedInferenceResponse, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return UnwrappedInferenceResponse{}, fmt.Errorf("empty body")
	}

	if isLikelyJSONObject(raw) {
		if err := rejectDeprecatedJSONEnvelopeKeys(raw); err != nil {
			return UnwrappedInferenceResponse{}, err
		}
		var resp InferenceResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return UnwrappedInferenceResponse{}, fmt.Errorf("decode legacy inference response json: %w", err)
		}
		return UnwrappedInferenceResponse{
			SchemaVersion: 0,
			HeightSync:    nil,
			Response:      resp,
			WholeBodyJSON: true,
		}, nil
	}

	var env types.InferenceResponseEnvelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		return UnwrappedInferenceResponse{}, fmt.Errorf("decode inference response envelope protobuf: %w", err)
	}
	if len(env.InferenceResponseJson) == 0 {
		return UnwrappedInferenceResponse{}, fmt.Errorf("inference_response_json is empty")
	}
	var resp InferenceResponse
	if err := json.Unmarshal(env.InferenceResponseJson, &resp); err != nil {
		return UnwrappedInferenceResponse{}, fmt.Errorf("decode inference response json: %w", err)
	}
	return UnwrappedInferenceResponse{
		SchemaVersion: int(env.SchemaVersion),
		HeightSync:    heightSyncFromProto(env.HeightSync),
		Response:      resp,
		WholeBodyJSON: false,
	}, nil
}

func isLikelyJSONObject(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
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

func rejectDeprecatedJSONEnvelopeKeys(raw []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil
	}
	if _, ok := top["message"]; ok {
		return fmt.Errorf("deprecated JSON inference envelope (top-level \"message\") is not supported; use protobuf InferenceRequestEnvelope / InferenceResponseEnvelope")
	}
	return nil
}

func heightSyncToProto(hs *heightsync.HeightSyncSection) *types.InferenceHeightSyncSection {
	if hs == nil {
		return nil
	}
	pt := types.InferenceHeightSyncProofType_INFERENCE_HEIGHT_SYNC_PROOF_TYPE_UNSPECIFIED
	if hs.ProofType == heightsync.AnchorProofType {
		pt = types.InferenceHeightSyncProofType_INFERENCE_HEIGHT_SYNC_PROOF_TYPE_HEIGHT_ANCHOR_V1
	}
	resp := hs.Direction == "response"
	return &types.InferenceHeightSyncSection{
		ProofType:                 pt,
		MainnetHeight:             hs.MainnetHeight,
		MainnetBlockHashHex:       hs.MainnetBlockHashHex,
		TimestampUnixMs:           hs.TimestampUnixMs,
		Response:                  resp,
		OriginatorSenderId:        hs.OriginatorSenderID,
		OriginatorTimestampUnixMs: hs.OriginatorTimestampMs,
		SenderSignature:           cappedBytes(hs.SenderSignature, heightsync.MaxOriginSignatureBytes),
	}
}

func heightSyncFromProto(hs *types.InferenceHeightSyncSection) *heightsync.HeightSyncSection {
	if hs == nil {
		return nil
	}
	proof := ""
	switch hs.GetProofType() {
	case types.InferenceHeightSyncProofType_INFERENCE_HEIGHT_SYNC_PROOF_TYPE_HEIGHT_ANCHOR_V1:
		proof = heightsync.AnchorProofType
	}
	dir := "request"
	if hs.GetResponse() {
		dir = "response"
	}
	return &heightsync.HeightSyncSection{
		ChainID:               "",
		ProofType:             proof,
		MainnetHeight:         hs.GetMainnetHeight(),
		MainnetBlockHashHex:   hs.GetMainnetBlockHashHex(),
		TimestampUnixMs:       hs.GetTimestampUnixMs(),
		Direction:             dir,
		OriginatorSenderID:    hs.GetOriginatorSenderId(),
		OriginatorTimestampMs: hs.GetOriginatorTimestampUnixMs(),
		SenderSignature:       cappedBytes(hs.GetSenderSignature(), heightsync.MaxOriginSignatureBytes),
	}
}

func cappedBytes(b []byte, max int) []byte {
	if len(b) == 0 {
		return nil
	}
	if len(b) > max {
		return nil
	}
	return append([]byte(nil), b...)
}
