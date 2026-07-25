package poc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"common/logging"
	"common/utils"
	"decentralized-api/cosmosclient"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/types"
)

// Typed errors for explicit error handling
var (
	ErrProofVerificationFailed = errors.New("proof verification failed")
	ErrDuplicateNonces         = errors.New("duplicate nonces detected")
	ErrIncompleteCoverage      = errors.New("response does not cover all requested leaf indices")
	ErrNonceAbsent             = errors.New("response does not cover all requested nonces")
	ErrInvalidVectorData       = errors.New("invalid vector data detected")
)

// ProofClient fetches and verifies SMST proofs from participant APIs.
const DefaultKDim = 12

type ProofClient struct {
	httpClient *http.Client
	recorder   cosmosclient.CosmosMessageClient
}

// ProofRequest contains the parameters for requesting proofs.
type ProofRequest struct {
	PocStageStartBlockHeight int64
	ModelId                  string
	RootHash                 []byte
	Count                    uint32
	LeafIndices              []uint32
	ParticipantAddress       string // participant whose API we're calling
}

// ProofByNonceRequest contains parameters for requesting proofs by nonce.
type ProofByNonceRequest struct {
	PocStageStartBlockHeight int64
	ModelId                  string
	RootHash                 []byte
	Count                    uint32
	Nonces                   []int32
	ParticipantAddress       string // participant whose API we're calling
}

// ProofResponse is the response from the proof API.
type ProofResponse struct {
	Proofs []ProofItem `json:"proofs"`
}

// ProofItem is a single proof in the response.
type ProofItem struct {
	LeafIndex   uint32   `json:"leaf_index"`
	NonceValue  int32    `json:"nonce_value"`
	VectorBytes string   `json:"vector_bytes"` // base64-encoded
	Proof       []string `json:"proof"`        // base64-encoded hashes
}

// VerifiedArtifact represents an artifact with verified proof.
type VerifiedArtifact struct {
	LeafIndex uint32
	Nonce     int32
	VectorB64 string
}

// ProofClientConfig contains configuration for the proof client.
type ProofClientConfig struct {
	Timeout time.Duration
}

// DefaultProofClientConfig returns the default configuration.
func DefaultProofClientConfig() ProofClientConfig {
	return ProofClientConfig{
		Timeout: 30 * time.Second,
	}
}

// NewProofClient creates a new proof client.
func NewProofClient(recorder cosmosclient.CosmosMessageClient, config ProofClientConfig) *ProofClient {
	return &ProofClient{
		httpClient: utils.NewHttpClient(config.Timeout),
		recorder:   recorder,
	}
}

// FetchAndVerifyProofs fetches proofs from the participant's API and verifies them.
// Returns verified artifacts or error.
func (c *ProofClient) FetchAndVerifyProofs(
	ctx context.Context,
	participantUrl string,
	req ProofRequest,
) ([]VerifiedArtifact, error) {
	// Build request body
	timestamp := time.Now().UnixNano()
	validatorAddress := c.recorder.GetAccountAddress()
	signerAddress := c.recorder.GetSignerAddress()

	// Build signature payload
	signPayload := buildProofSignPayload(
		req.PocStageStartBlockHeight,
		req.ModelId,
		req.RootHash,
		req.Count,
		req.LeafIndices,
		timestamp,
		validatorAddress,
		signerAddress,
	)

	// Sign the payload
	signature, err := c.recorder.SignBytes(signPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to sign request: %w", err)
	}

	// Build JSON request body
	leafIndicesInt := make([]int64, len(req.LeafIndices))
	for i, idx := range req.LeafIndices {
		leafIndicesInt[i] = int64(idx)
	}

	requestBody := map[string]interface{}{
		"poc_stage_start_block_height": req.PocStageStartBlockHeight,
		"model_id":                     req.ModelId,
		"root_hash":                    base64.StdEncoding.EncodeToString(req.RootHash),
		"count":                        req.Count,
		"leaf_indices":                 leafIndicesInt,
		"validator_address":            validatorAddress,
		"validator_signer_address":     signerAddress,
		"timestamp":                    timestamp,
		"signature":                    base64.StdEncoding.EncodeToString(signature),
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build URL
	proofUrl, err := url.JoinPath(participantUrl, "v1/poc/proofs")
	if err != nil {
		return nil, fmt.Errorf("failed to build proof URL: %w", err)
	}

	// Make HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, proofUrl, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("proof request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var proofResp ProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&proofResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Validate coverage: response must contain exactly the requested leaf indices
	if err := validateLeafCoverage(req.LeafIndices, proofResp.Proofs); err != nil {
		return nil, err
	}

	verified := make([]VerifiedArtifact, 0, len(proofResp.Proofs))
	for _, item := range proofResp.Proofs {
		artifact, err := verifyProofItem(req.RootHash, req.Count, req.ParticipantAddress, item)
		if err != nil {
			return nil, err
		}
		verified = append(verified, artifact)
	}

	logging.Debug("Verified proofs from participant", types.PoC,
		"participant", req.ParticipantAddress, "count", len(verified))

	return verified, nil
}

// FetchAndVerifyProofsByNonce fetches proofs for concrete nonces from the
// participant's API and verifies their SMST index binding.
func (c *ProofClient) FetchAndVerifyProofsByNonce(
	ctx context.Context,
	participantUrl string,
	req ProofByNonceRequest,
) ([]VerifiedArtifact, error) {
	timestamp := time.Now().UnixNano()
	validatorAddress := c.recorder.GetAccountAddress()
	signerAddress := c.recorder.GetSignerAddress()

	signPayload := buildProofByNonceSignPayload(
		req.PocStageStartBlockHeight,
		req.ModelId,
		req.RootHash,
		req.Count,
		req.Nonces,
		timestamp,
		validatorAddress,
		signerAddress,
	)
	signature, err := c.recorder.SignBytes(signPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to sign request: %w", err)
	}

	noncesInt := make([]int64, len(req.Nonces))
	for i, nonce := range req.Nonces {
		noncesInt[i] = int64(nonce)
	}
	requestBody := map[string]interface{}{
		"poc_stage_start_block_height": req.PocStageStartBlockHeight,
		"model_id":                     req.ModelId,
		"root_hash":                    base64.StdEncoding.EncodeToString(req.RootHash),
		"count":                        req.Count,
		"nonces":                       noncesInt,
		"validator_address":            validatorAddress,
		"validator_signer_address":     signerAddress,
		"timestamp":                    timestamp,
		"signature":                    base64.StdEncoding.EncodeToString(signature),
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	proofUrl, err := url.JoinPath(participantUrl, "v1/poc/proofs/by-nonce")
	if err != nil {
		return nil, fmt.Errorf("failed to build proof URL: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, proofUrl, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("proof by nonce request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var proofResp ProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&proofResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if err := validateNonceCoverage(req.Nonces, proofResp.Proofs); err != nil {
		return nil, err
	}

	verified := make([]VerifiedArtifact, 0, len(proofResp.Proofs))
	for _, item := range proofResp.Proofs {
		artifact, err := verifyProofItem(req.RootHash, req.Count, req.ParticipantAddress, item)
		if err != nil {
			return nil, err
		}
		verified = append(verified, artifact)
	}

	logging.Debug("Verified by-nonce proofs from participant", types.PoC,
		"participant", req.ParticipantAddress, "count", len(verified))

	return verified, nil
}

// CheckDuplicateNonces checks if any artifacts have duplicate nonces.
// Returns ErrDuplicateNonces if duplicates found (fraud detected).
func CheckDuplicateNonces(artifacts []VerifiedArtifact) error {
	seen := make(map[int32]struct{}, len(artifacts))
	for _, a := range artifacts {
		if _, exists := seen[a.Nonce]; exists {
			return ErrDuplicateNonces
		}
		seen[a.Nonce] = struct{}{}
	}
	return nil
}

// validateLeafCoverage checks that the response covers exactly the requested leaf indices.
// Returns error if there are missing indices or duplicates.
func validateLeafCoverage(requested []uint32, proofs []ProofItem) error {
	if len(proofs) != len(requested) {
		return fmt.Errorf("%w: expected %d proofs, got %d", ErrIncompleteCoverage, len(requested), len(proofs))
	}
	if len(requested) == 0 {
		return nil
	}

	// Build set of requested indices
	requestedSet := make(map[uint32]struct{}, len(requested))
	for _, idx := range requested {
		requestedSet[idx] = struct{}{}
	}

	// Check each proof's leaf index
	seen := make(map[uint32]struct{}, len(proofs))
	for _, p := range proofs {
		if _, duplicate := seen[p.LeafIndex]; duplicate {
			return fmt.Errorf("%w: duplicate leaf index %d", ErrIncompleteCoverage, p.LeafIndex)
		}
		seen[p.LeafIndex] = struct{}{}

		if _, ok := requestedSet[p.LeafIndex]; !ok {
			return fmt.Errorf("%w: unexpected leaf index %d", ErrIncompleteCoverage, p.LeafIndex)
		}
	}

	return nil
}

func validateNonceCoverage(requested []int32, proofs []ProofItem) error {
	if len(requested) == 0 {
		if len(proofs) == 0 {
			return nil
		}
		return fmt.Errorf("%w: unexpected proof for nonce %d", ErrIncompleteCoverage, proofs[0].NonceValue)
	}

	missing := make(map[int32]struct{}, len(requested))
	for _, nonce := range requested {
		missing[nonce] = struct{}{}
	}

	seen := make(map[int32]struct{}, len(proofs))
	for _, p := range proofs {
		if _, duplicate := seen[p.NonceValue]; duplicate {
			return fmt.Errorf("%w: duplicate nonce %d", ErrIncompleteCoverage, p.NonceValue)
		}
		seen[p.NonceValue] = struct{}{}

		if _, ok := missing[p.NonceValue]; !ok {
			return fmt.Errorf("%w: unexpected nonce %d", ErrIncompleteCoverage, p.NonceValue)
		}
		delete(missing, p.NonceValue)
	}

	for nonce := range missing {
		return fmt.Errorf("%w: missing nonce %d", ErrNonceAbsent, nonce)
	}
	return nil
}

func verifyProofItem(rootHash []byte, count uint32, participantAddress string, item ProofItem) (VerifiedArtifact, error) {
	vectorBytes, err := base64.StdEncoding.DecodeString(item.VectorBytes)
	if err != nil {
		logging.Warn("Failed to decode vector bytes", types.PoC,
			"participant", participantAddress, "leafIndex", item.LeafIndex, "error", err)
		return VerifiedArtifact{}, fmt.Errorf("invalid vector_bytes encoding for leaf %d: %w", item.LeafIndex, err)
	}

	if err := ValidateFP16Vector(vectorBytes, DefaultKDim); err != nil {
		logging.Warn("Invalid FP16 vector data", types.PoC,
			"participant", participantAddress, "leafIndex", item.LeafIndex, "error", err)
		return VerifiedArtifact{}, fmt.Errorf("%w: leaf %d: %v", ErrInvalidVectorData, item.LeafIndex, err)
	}

	proofHashes := make([][]byte, len(item.Proof))
	for i, hashB64 := range item.Proof {
		hash, err := base64.StdEncoding.DecodeString(hashB64)
		if err != nil {
			return VerifiedArtifact{}, fmt.Errorf("invalid proof hash encoding for leaf %d: %w", item.LeafIndex, err)
		}
		proofHashes[i] = hash
	}

	leafData := buildLeafData(item.NonceValue, vectorBytes)
	if !artifacts.VerifySMSTProofWithDenseIndex(rootHash, count, item.LeafIndex, item.NonceValue, leafData, proofHashes) {
		logging.Warn("SMST proof verification failed", types.PoC,
			"participant", participantAddress, "leafIndex", item.LeafIndex, "nonce", item.NonceValue)
		return VerifiedArtifact{}, fmt.Errorf("%w: leaf %d", ErrProofVerificationFailed, item.LeafIndex)
	}

	return VerifiedArtifact{
		LeafIndex: item.LeafIndex,
		Nonce:     item.NonceValue,
		VectorB64: item.VectorBytes,
	}, nil
}

// buildProofSignPayload builds the binary payload for signature.
// Format: hex(SHA256(
//
//	poc_stage_start_block_height (LE64) ||
//	len(model_id) (LE32) || model_id ||
//	root_hash (32 bytes) ||
//	count (LE32) ||
//	num_leaf_indices (LE32) || leaf_indices (LE32 each) ||
//	timestamp (LE64) ||
//	len(validator_address) (LE32) || validator_address ||
//	len(signer_address) (LE32) || signer_address
//
// ))
//
// Every variable-length field is length-prefixed so distinct semantic
// tuples cannot map to identical bytes. This must stay in lockstep with
// buildPocProofsSignPayload on the server side.
func buildProofSignPayload(
	pocStageStartBlockHeight int64,
	modelID string,
	rootHash []byte,
	count uint32,
	leafIndices []uint32,
	timestamp int64,
	validatorAddress string,
	signerAddress string,
) []byte {
	buf := new(bytes.Buffer)

	binary.Write(buf, binary.LittleEndian, pocStageStartBlockHeight)
	writeLengthPrefixedString(buf, modelID)
	buf.Write(rootHash)
	binary.Write(buf, binary.LittleEndian, count)
	binary.Write(buf, binary.LittleEndian, uint32(len(leafIndices)))
	for _, idx := range leafIndices {
		binary.Write(buf, binary.LittleEndian, idx)
	}
	binary.Write(buf, binary.LittleEndian, timestamp)
	writeLengthPrefixedString(buf, validatorAddress)
	writeLengthPrefixedString(buf, signerAddress)

	hash := sha256.Sum256(buf.Bytes())
	// Return hex-encoded string as bytes (what the server expects to verify)
	return []byte(hex.EncodeToString(hash[:]))
}

// buildProofByNonceSignPayload mirrors the server's by-nonce request payload.
// The leading domain tag prevents leaf-index signatures from replaying here.
func buildProofByNonceSignPayload(
	pocStageStartBlockHeight int64,
	modelID string,
	rootHash []byte,
	count uint32,
	nonces []int32,
	timestamp int64,
	validatorAddress string,
	signerAddress string,
) []byte {
	buf := new(bytes.Buffer)

	writeLengthPrefixedString(buf, "poc-proofs-by-nonce-v1")
	binary.Write(buf, binary.LittleEndian, pocStageStartBlockHeight)
	writeLengthPrefixedString(buf, modelID)
	buf.Write(rootHash)
	binary.Write(buf, binary.LittleEndian, count)
	binary.Write(buf, binary.LittleEndian, uint32(len(nonces)))
	for _, nonce := range nonces {
		binary.Write(buf, binary.LittleEndian, nonce)
	}
	binary.Write(buf, binary.LittleEndian, timestamp)
	writeLengthPrefixedString(buf, validatorAddress)
	writeLengthPrefixedString(buf, signerAddress)

	hash := sha256.Sum256(buf.Bytes())
	return []byte(hex.EncodeToString(hash[:]))
}

// writeLengthPrefixedString writes len(s) as a LE uint32 followed by the
// raw string bytes. Mirrors the helper in poc_handler.go on the server.
func writeLengthPrefixedString(buf *bytes.Buffer, s string) {
	binary.Write(buf, binary.LittleEndian, uint32(len(s)))
	buf.WriteString(s)
}

// buildLeafData builds the leaf data format used in SMST proofs.
// Format: nonce(LE32) || vector
func buildLeafData(nonce int32, vector []byte) []byte {
	buf := make([]byte, 4+len(vector))
	binary.LittleEndian.PutUint32(buf[:4], uint32(nonce))
	copy(buf[4:], vector)
	return buf
}

// ValidateFP16Vector checks that the vector has exactly kDim FP16 values and all are valid finite numbers.
// Returns error if length doesn't match kDim*2 bytes, or any value is NaN or Infinity.
func ValidateFP16Vector(vectorBytes []byte, kDim int) error {
	expectedLen := kDim * 2
	if len(vectorBytes) != expectedLen {
		return fmt.Errorf("invalid vector length: got %d bytes, expected %d (kDim=%d)", len(vectorBytes), expectedLen, kDim)
	}

	for i := 0; i < len(vectorBytes); i += 2 {
		h := binary.LittleEndian.Uint16(vectorBytes[i : i+2])
		exp := (h >> 10) & 0x1f

		// Exponent 31 = special values (NaN or Infinity)
		if exp == 31 {
			frac := h & 0x3ff
			if frac != 0 {
				return fmt.Errorf("NaN detected at byte offset %d (value 0x%04x)", i, h)
			}
			return fmt.Errorf("Infinity detected at byte offset %d (value 0x%04x)", i, h)
		}
	}
	return nil
}
