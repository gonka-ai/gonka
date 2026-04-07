package transport

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"subnet/host"
	"subnet/types"
)

// DiffJSON is the JSON wire format for a single diff.
// Proto-serialized fields travel as base64 to preserve signature integrity.
type DiffJSON struct {
	Nonce         uint64 `json:"nonce"`
	Txs           []byte `json:"txs"`                        // proto bytes of DiffContent.Txs wrapper
	UserSig       []byte `json:"user_sig"`                   // raw sig bytes
	PostStateRoot []byte `json:"post_state_root,omitempty"`  // state root after applying txs
}

// PayloadJSON is the JSON wire format for inference payload.
type PayloadJSON struct {
	Prompt      []byte `json:"prompt"`
	Model       string `json:"model"`
	InputLength uint64 `json:"input_length"`
	MaxTokens   uint64 `json:"max_tokens"`
	StartedAt   int64  `json:"started_at"`
}

// InferenceRequest is the JSON body for POST /sessions/:id/chat/completions.
type InferenceRequest struct {
	Diffs   []DiffJSON   `json:"diffs"`
	Nonce   uint64       `json:"nonce"`
	Payload *PayloadJSON `json:"payload,omitempty"`
	Stream  bool         `json:"stream,omitempty"` // hint: stream SSE deltas vs single JSON event
}

// InferenceResponse is the JSON body returned by the inference endpoint.
type InferenceResponse struct {
	StateSig    []byte   `json:"state_sig,omitempty"`
	StateHash   []byte   `json:"state_hash,omitempty"`
	Nonce       uint64   `json:"nonce"`
	Receipt     []byte   `json:"receipt,omitempty"`
	ConfirmedAt int64    `json:"confirmed_at,omitempty"`
	Mempool     [][]byte `json:"mempool,omitempty"` // each: proto bytes of SubnetTx
}

// VerifyTimeoutRequest is the JSON body for POST /sessions/:id/verify-timeout.
type VerifyTimeoutRequest struct {
	InferenceID uint64       `json:"inference_id"`
	Reason      string       `json:"reason"` // "refused" or "execution"
	Payload     *PayloadJSON `json:"payload,omitempty"`
	Diffs       []DiffJSON   `json:"diffs,omitempty"` // catch-up diffs so verifier knows about the inference
}

// VerifyTimeoutResponse is returned by the timeout verification endpoint.
type VerifyTimeoutResponse struct {
	Accept    bool   `json:"accept"`
	Signature []byte `json:"signature,omitempty"` // signed TimeoutVoteContent
	VoterSlot uint32 `json:"voter_slot"`
}

// ChallengeReceiptRequest is the JSON body for POST /sessions/:id/challenge-receipt.
type ChallengeReceiptRequest struct {
	InferenceID uint64       `json:"inference_id"`
	Payload     *PayloadJSON `json:"payload"`
	Diffs       []DiffJSON   `json:"diffs"`
}

// ChallengeReceiptResponse is returned by the challenge-receipt endpoint.
type ChallengeReceiptResponse struct {
	Receipt []byte `json:"receipt,omitempty"`
}

// GossipNonceRequest is the JSON body for POST /sessions/:id/gossip/nonce.
type GossipNonceRequest struct {
	Nonce     uint64 `json:"nonce"`
	StateHash []byte `json:"state_hash"`
	StateSig  []byte `json:"state_sig"`
	SlotID    uint32 `json:"slot_id"`
}

// GossipTxsRequest is the JSON body for POST /sessions/:id/gossip/txs.
type GossipTxsRequest struct {
	Txs [][]byte `json:"txs"` // each: proto bytes of SubnetTx
}

// SignaturesResponse is returned by the signatures endpoint.
type SignaturesResponse struct {
	Signatures map[uint32][]byte `json:"signatures"` // slotID -> sig bytes
}

// DiffToJSON converts a domain Diff to its JSON wire format.
func DiffToJSON(d types.Diff) (DiffJSON, error) {
	// Serialize the txs as a DiffContent proto (nonce + txs together)
	// to preserve the exact bytes that were signed.
	content := &types.DiffContent{Nonce: d.Nonce, Txs: d.Txs}
	txsBytes, err := proto.Marshal(content)
	if err != nil {
		return DiffJSON{}, fmt.Errorf("marshal diff content: %w", err)
	}
	return DiffJSON{
		Nonce:         d.Nonce,
		Txs:           txsBytes,
		UserSig:       d.UserSig,
		PostStateRoot: d.PostStateRoot,
	}, nil
}

// DiffFromJSON converts a JSON wire diff back to the domain Diff.
func DiffFromJSON(dj DiffJSON) (types.Diff, error) {
	var content types.DiffContent
	if err := proto.Unmarshal(dj.Txs, &content); err != nil {
		return types.Diff{}, fmt.Errorf("unmarshal diff content: %w", err)
	}
	return types.Diff{
		Nonce:         dj.Nonce,
		Txs:           content.Txs,
		UserSig:       dj.UserSig,
		PostStateRoot: dj.PostStateRoot,
	}, nil
}

// HostRequestToJSON converts a HostRequest to InferenceRequest.
func HostRequestToJSON(req host.HostRequest) (InferenceRequest, error) {
	diffs := make([]DiffJSON, len(req.Diffs))
	for i, d := range req.Diffs {
		dj, err := DiffToJSON(d)
		if err != nil {
			return InferenceRequest{}, fmt.Errorf("diff %d: %w", i, err)
		}
		diffs[i] = dj
	}

	ir := InferenceRequest{
		Diffs: diffs,
		Nonce: req.Nonce,
	}
	ir.Payload = PayloadToJSON(req.Payload)
	return ir, nil
}

// HostRequestFromJSON converts an InferenceRequest back to HostRequest.
func HostRequestFromJSON(ir InferenceRequest) (host.HostRequest, error) {
	diffs := make([]types.Diff, len(ir.Diffs))
	for i, dj := range ir.Diffs {
		d, err := DiffFromJSON(dj)
		if err != nil {
			return host.HostRequest{}, fmt.Errorf("diff %d: %w", i, err)
		}
		diffs[i] = d
	}

	req := host.HostRequest{
		Diffs: diffs,
		Nonce: ir.Nonce,
	}
	req.Payload = PayloadFromJSON(ir.Payload)
	return req, nil
}

// HostResponseToJSON converts a HostResponse to InferenceResponse.
func HostResponseToJSON(resp *host.HostResponse) (InferenceResponse, error) {
	var mempool [][]byte
	for _, tx := range resp.Mempool {
		b, err := proto.Marshal(tx)
		if err != nil {
			return InferenceResponse{}, fmt.Errorf("marshal mempool tx: %w", err)
		}
		mempool = append(mempool, b)
	}
	return InferenceResponse{
		StateSig:    resp.StateSig,
		StateHash:   resp.StateHash,
		Nonce:       resp.Nonce,
		Receipt:     resp.Receipt,
		ConfirmedAt: resp.ConfirmedAt,
		Mempool:     mempool,
	}, nil
}

// HostResponseFromJSON converts an InferenceResponse back to HostResponse.
func HostResponseFromJSON(ir InferenceResponse) (*host.HostResponse, error) {
	var mempool []*types.SubnetTx
	for i, b := range ir.Mempool {
		tx := &types.SubnetTx{}
		if err := proto.Unmarshal(b, tx); err != nil {
			return nil, fmt.Errorf("unmarshal mempool tx %d: %w", i, err)
		}
		mempool = append(mempool, tx)
	}
	return &host.HostResponse{
		StateSig:    ir.StateSig,
		StateHash:   ir.StateHash,
		Nonce:       ir.Nonce,
		Receipt:     ir.Receipt,
		ConfirmedAt: ir.ConfirmedAt,
		Mempool:     mempool,
	}, nil
}

// SubnetTxsToBytes serializes a slice of SubnetTx to proto byte slices.
func SubnetTxsToBytes(txs []*types.SubnetTx) ([][]byte, error) {
	result := make([][]byte, len(txs))
	for i, tx := range txs {
		b, err := proto.Marshal(tx)
		if err != nil {
			return nil, fmt.Errorf("marshal tx %d: %w", i, err)
		}
		result[i] = b
	}
	return result, nil
}

// SubnetTxsFromBytes deserializes proto byte slices to SubnetTx.
func SubnetTxsFromBytes(data [][]byte) ([]*types.SubnetTx, error) {
	result := make([]*types.SubnetTx, len(data))
	for i, b := range data {
		tx := &types.SubnetTx{}
		if err := proto.Unmarshal(b, tx); err != nil {
			return nil, fmt.Errorf("unmarshal tx %d: %w", i, err)
		}
		result[i] = tx
	}
	return result, nil
}

// TimeoutReasonToString converts proto enum to wire string.
func TimeoutReasonToString(r types.TimeoutReason) string {
	switch r {
	case types.TimeoutReason_TIMEOUT_REASON_REFUSED:
		return "refused"
	case types.TimeoutReason_TIMEOUT_REASON_EXECUTION:
		return "execution"
	default:
		return "unknown"
	}
}

// PayloadToJSON converts a domain InferencePayload to its JSON wire format.
func PayloadToJSON(p *host.InferencePayload) *PayloadJSON {
	if p == nil {
		return nil
	}
	return &PayloadJSON{
		Prompt:      p.Prompt,
		Model:       p.Model,
		InputLength: p.InputLength,
		MaxTokens:   p.MaxTokens,
		StartedAt:   p.StartedAt,
	}
}

// PayloadFromJSON converts a JSON wire payload back to the domain type.
func PayloadFromJSON(pj *PayloadJSON) *host.InferencePayload {
	if pj == nil {
		return nil
	}
	return &host.InferencePayload{
		Prompt:      pj.Prompt,
		Model:       pj.Model,
		InputLength: pj.InputLength,
		MaxTokens:   pj.MaxTokens,
		StartedAt:   pj.StartedAt,
	}
}

// TimeoutReasonFromString converts wire string to proto enum.
func TimeoutReasonFromString(s string) (types.TimeoutReason, error) {
	switch s {
	case "refused":
		return types.TimeoutReason_TIMEOUT_REASON_REFUSED, nil
	case "execution":
		return types.TimeoutReason_TIMEOUT_REASON_EXECUTION, nil
	default:
		return 0, fmt.Errorf("unknown timeout reason: %s", s)
	}
}

// SubnetReceiptEvent is the first SSE event, sent before execution starts.
type SubnetReceiptEvent struct {
	StateSig    []byte `json:"state_sig,omitempty"`
	StateHash   []byte `json:"state_hash,omitempty"`
	Nonce       uint64 `json:"nonce"`
	Receipt     []byte `json:"receipt,omitempty"`
	ConfirmedAt int64  `json:"confirmed_at,omitempty"`
}

// SubnetMetaEvent is the final SSE event, sent after execution completes.
type SubnetMetaEvent struct {
	Mempool [][]byte `json:"mempool,omitempty"`
}
