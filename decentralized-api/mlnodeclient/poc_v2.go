package mlnodeclient

// PoC v2 (artifact-based) types for MLNode API callbacks.
// These match the schemas in mlnode/packages/api/tests/batch_receiver_v2.py.

// ArtifactV2 represents a single artifact from PoC v2 generation.
type ArtifactV2 struct {
	Nonce     int64  `json:"nonce"`
	VectorB64 string `json:"vector_b64"` // base64-encoded fp16 little-endian vector
}

// EncodingV2 describes the artifact encoding (protocol-level defaults; informational only).
// decentralized-api ignores this field; it's passed through for completeness.
type EncodingV2 struct {
	Dtype  string `json:"dtype"`  // e.g., "f16"
	KDim   int    `json:"k_dim"`  // e.g., 12
	Endian string `json:"endian"` // e.g., "le"
}

// GeneratedArtifactBatchV2 is received from MLNode via /v2/poc-batches/generated callback.
type GeneratedArtifactBatchV2 struct {
	BlockHash   string       `json:"block_hash"`
	BlockHeight int64        `json:"block_height"`
	PublicKey   string       `json:"public_key"`
	NodeId      int          `json:"node_id"`
	Artifacts   []ArtifactV2 `json:"artifacts"`
	Encoding    *EncodingV2  `json:"encoding,omitempty"` // optional; ignored by decentralized-api
	RequestId   string       `json:"request_id,omitempty"`
}

// ValidatedResultV2 is received from MLNode via /v2/poc-batches/validated callback.
type ValidatedResultV2 struct {
	RequestId      string  `json:"request_id,omitempty"`
	BlockHash      string  `json:"block_hash,omitempty"`
	BlockHeight    int64   `json:"block_height,omitempty"`
	PublicKey      string  `json:"public_key,omitempty"`
	NodeId         int     `json:"node_id,omitempty"`
	NTotal         int64   `json:"n_total"`
	NMismatch      int64   `json:"n_mismatch"`
	MismatchNonces []int64 `json:"mismatch_nonces"`
	PValue         float64 `json:"p_value"`
	FraudDetected  bool    `json:"fraud_detected"`
}

// ToValidatedWeight returns NTotal (sample size) on success, -1 on fraud/failure.
// Chain only checks sign: >0 = valid vote, <=0 = invalid. Actual weight from PoCV2StoreCommit.Count.
// TODO: Should return committed count, not sample size.
func (v *ValidatedResultV2) ToValidatedWeight() int64 {
	if v.FraudDetected || v.NTotal <= 0 {
		return -1
	}
	return v.NTotal
}
