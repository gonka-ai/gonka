package public

import (
	"bytes"
	"crypto/sha256"
	"decentralized-api/logging"
	"decentralized-api/poc"
	"decentralized-api/poc/artifacts"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

const (
	maxLeafIndicesPerRequest = 500
	pocProofsMsgTypeUrl      = "/inference.inference.MsgSubmitPocValidationsV2"
	timestampWindowNanos     = 5 * 60 * 1_000_000_000 // 5 minutes in nanoseconds
)

// StringInt64 unmarshals from both JSON number and string
type StringInt64 int64

func (s *StringInt64) UnmarshalJSON(data []byte) error {
	var num int64
	if err := json.Unmarshal(data, &num); err == nil {
		*s = StringInt64(num)
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	num, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return err
	}
	*s = StringInt64(num)
	return nil
}

// StringUint32 unmarshals from both JSON number and string
type StringUint32 uint32

func (s *StringUint32) UnmarshalJSON(data []byte) error {
	var num uint32
	if err := json.Unmarshal(data, &num); err == nil {
		*s = StringUint32(num)
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	num64, err := strconv.ParseUint(str, 10, 32)
	if err != nil {
		return err
	}
	*s = StringUint32(num64)
	return nil
}

// PocProofsRequest is the request body for POST /v1/poc/proofs
// Uses StringInt64/StringUint32 to accept both number and string JSON encoding
type PocProofsRequest struct {
	PocStageStartBlockHeight StringInt64    `json:"poc_stage_start_block_height"`
	RootHash                 string         `json:"root_hash"`    // base64-encoded 32 bytes
	Count                    StringUint32   `json:"count"`        // snapshot leaf count
	LeafIndices              []StringUint32 `json:"leaf_indices"` // 0-based indices

	ValidatorAddress       string      `json:"validator_address"`        // validator's cold key (for authz lookup)
	ValidatorSignerAddress string      `json:"validator_signer_address"` // actual signer (cold or warm key)
	Timestamp              StringInt64 `json:"timestamp"`                // unix nanoseconds
	Signature              string      `json:"signature"`                // base64-encoded signature
}

// PocProofItem is a single proof in the response
type PocProofItem struct {
	LeafIndex   uint32   `json:"leaf_index"`
	NonceValue  int32    `json:"nonce_value"`
	VectorBytes string   `json:"vector_bytes"` // base64-encoded
	Proof       []string `json:"proof"`        // base64-encoded hashes
}

// PocProofsResponse is the response body for POST /v1/poc/proofs
type PocProofsResponse struct {
	Proofs        []PocProofItem `json:"proofs"`
	SignerAddress string         `json:"signer_address,omitempty"` // participant's signer address
	Timestamp     int64          `json:"timestamp,omitempty"`      // response timestamp (nanos)
	Signature     string         `json:"signature,omitempty"`      // base64-encoded signature
}

// PocArtifactsStateResponse is the response for GET /v1/poc/artifacts/state
type PocArtifactsStateResponse struct {
	PocStageStartBlockHeight int64  `json:"poc_stage_start_block_height"`
	Count                    uint32 `json:"count"`
	RootHash                 string `json:"root_hash"` // base64-encoded 32 bytes, empty if count=0
}

// postPocProofs handles POST /v1/poc/proofs
func (s *Server) postPocProofs(ctx echo.Context) error {
	if s.artifactStore == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "artifact store not configured")
	}

	if s.phaseTracker != nil && !poc.ShouldUseV2FromEpochState(s.phaseTracker.GetCurrentEpochState()) {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "proof API requires V2 mode")
	}

	var req PocProofsRequest
	if err := ctx.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	// Validate required fields
	if req.RootHash == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "root_hash required")
	}
	if req.ValidatorAddress == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "validator_address required")
	}
	if req.ValidatorSignerAddress == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "validator_signer_address required")
	}
	if req.Signature == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "signature required")
	}
	if len(req.LeafIndices) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "leaf_indices required")
	}
	if len(req.LeafIndices) > maxLeafIndicesPerRequest {
		return echo.NewHTTPError(http.StatusBadRequest, "too many leaf_indices (max 500)")
	}

	// Decode root_hash
	rootHash, err := base64.StdEncoding.DecodeString(req.RootHash)
	if err != nil || len(rootHash) != 32 {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid root_hash (must be 32 bytes base64)")
	}

	// Validate timestamp is within acceptable window (+/-5 minutes)
	nowNanos := time.Now().UnixNano()
	reqTimestamp := int64(req.Timestamp)
	if reqTimestamp < nowNanos-timestampWindowNanos || reqTimestamp > nowNanos+timestampWindowNanos {
		logging.Warn("PoC proofs request timestamp out of range", types.Validation,
			"timestamp", reqTimestamp, "now", nowNanos)
		return echo.NewHTTPError(http.StatusBadRequest, "timestamp out of acceptable window")
	}

	// Get pubkey for the specific signer address (via authz cache)
	// validator_address = cold key for authz lookup
	// validator_signer_address = actual signer (must be authorized for validator_address)
	signerPubkey, err := s.authzCache.GetPubKeyForSigner(
		ctx.Request().Context(),
		req.ValidatorAddress,
		req.ValidatorSignerAddress,
		pocProofsMsgTypeUrl,
	)
	if err != nil {
		logging.Error("Failed to get signer pubkey", types.Validation,
			"validatorAddress", req.ValidatorAddress,
			"validatorSignerAddress", req.ValidatorSignerAddress,
			"error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "validator not found")
	}
	if signerPubkey == "" {
		logging.Warn("Signer not authorized for validator", types.Validation,
			"validatorAddress", req.ValidatorAddress,
			"validatorSignerAddress", req.ValidatorSignerAddress)
		return echo.NewHTTPError(http.StatusUnauthorized, "signer not authorized for validator")
	}

	// Verify signature against the specific signer's pubkey
	if err := verifyPocProofsSignatureWithPubkey(&req, rootHash, signerPubkey); err != nil {
		logging.Warn("Invalid PoC proofs signature", types.Validation,
			"validatorAddress", req.ValidatorAddress,
			"validatorSignerAddress", req.ValidatorSignerAddress, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid signature")
	}

	// Get stage-specific artifact store
	stageStore, err := s.artifactStore.GetStore(int64(req.PocStageStartBlockHeight))
	if err != nil {
		logging.Warn("Stage store not found", types.Validation,
			"pocStageStartBlockHeight", req.PocStageStartBlockHeight, "error", err)
		return echo.NewHTTPError(http.StatusNotFound, "not found for height (may be pruned or not yet created)")
	}

	// Snapshot binding validation: verify (root_hash, count) matches store state
	reqCount := uint32(req.Count)
	storeRoot, err := stageStore.GetRootAt(reqCount)
	if err != nil {
		logging.Warn("Snapshot count exceeds store", types.Validation,
			"pocStageStartBlockHeight", req.PocStageStartBlockHeight,
			"requestedCount", reqCount, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "count exceeds stored artifacts")
	}
	if !bytes.Equal(rootHash, storeRoot) {
		logging.Warn("Root hash mismatch", types.Validation,
			"pocStageStartBlockHeight", req.PocStageStartBlockHeight,
			"requestedCount", reqCount)
		return echo.NewHTTPError(http.StatusBadRequest, "root_hash does not match store state at count")
	}

	// Validate all leaf indices are within snapshot range
	for _, leafIndex := range req.LeafIndices {
		if uint32(leafIndex) >= reqCount {
			return echo.NewHTTPError(http.StatusBadRequest, "leaf_index out of snapshot range")
		}
	}

	// Generate proofs
	proofs := make([]PocProofItem, 0, len(req.LeafIndices))
	for _, leafIndex := range req.LeafIndices {
		leafIdx := uint32(leafIndex)
		nonce, vector, err := stageStore.GetArtifact(leafIdx)
		if err != nil {
			if err == artifacts.ErrLeafIndexOutOfRange {
				return echo.NewHTTPError(http.StatusBadRequest, "leaf_index out of range")
			}
			logging.Error("Failed to get artifact", types.Validation,
				"leafIndex", leafIdx, "error", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get artifact")
		}

		proof, err := stageStore.GetProof(leafIdx, reqCount)
		if err != nil {
			if err == artifacts.ErrLeafIndexOutOfRange {
				return echo.NewHTTPError(http.StatusBadRequest, "leaf_index out of range for proof")
			}
			logging.Error("Failed to get proof", types.Validation,
				"leafIndex", leafIdx, "count", reqCount, "error", err)
			return echo.NewHTTPError(http.StatusBadRequest, "failed to generate proof")
		}

		// Encode proof hashes as base64
		proofStrings := make([]string, len(proof))
		for i, hash := range proof {
			proofStrings[i] = base64.StdEncoding.EncodeToString(hash)
		}

		proofs = append(proofs, PocProofItem{
			LeafIndex:   leafIdx,
			NonceValue:  nonce,
			VectorBytes: base64.StdEncoding.EncodeToString(vector),
			Proof:       proofStrings,
		})
	}

	logging.Info("Serving PoC proofs", types.Validation,
		"validatorAddress", req.ValidatorAddress, "count", len(proofs))

	// Build and sign response
	respTimestamp := time.Now().UnixNano()
	signerAddress := s.recorder.GetSignerAddress()

	signPayload := buildPocProofsResponseSignPayload(
		int64(req.PocStageStartBlockHeight),
		rootHash,
		reqCount,
		proofs,
		respTimestamp,
		signerAddress,
	)

	signature, err := s.recorder.SignBytes(signPayload)
	if err != nil {
		logging.Error("Failed to sign response", types.Validation, "error", err)
		return ctx.JSON(http.StatusOK, PocProofsResponse{Proofs: proofs})
	}

	return ctx.JSON(http.StatusOK, PocProofsResponse{
		Proofs:        proofs,
		SignerAddress: signerAddress,
		Timestamp:     respTimestamp,
		Signature:     base64.StdEncoding.EncodeToString(signature),
	})
}

// getPocArtifactsState returns the current artifact store state for a given height.
// Used by validators/testermint to get real count and root_hash for proof requests.
func (s *Server) getPocArtifactsState(ctx echo.Context) error {
	if s.artifactStore == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "artifact store not configured")
	}

	if s.phaseTracker != nil && !poc.ShouldUseV2FromEpochState(s.phaseTracker.GetCurrentEpochState()) {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "artifact state API requires V2 mode")
	}

	heightParam := ctx.QueryParam("height")
	if heightParam == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "height query parameter required")
	}

	height, err := strconv.ParseInt(heightParam, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid height parameter")
	}

	store, err := s.artifactStore.GetStore(height)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found for height (may be pruned or not yet created)")
	}

	count, rootHash := store.GetFlushedRoot()

	var rootHashB64 string
	if rootHash != nil {
		rootHashB64 = base64.StdEncoding.EncodeToString(rootHash)
	}

	return ctx.JSON(http.StatusOK, PocArtifactsStateResponse{
		PocStageStartBlockHeight: height,
		Count:                    count,
		RootHash:                 rootHashB64,
	})
}

// buildPocProofsSignPayload builds the binary payload for signature verification.
// Format: hex(SHA256(poc_stage_start_block_height(LE64) || root_hash(32) || count(LE32) ||
//
//	leaf_indices(LE32 each) || timestamp(LE64) || validator_address || validator_signer_address))
//
// Returns the hex-encoded hash as bytes because Kotlin's signPayload takes a hex string.
func buildPocProofsSignPayload(req *PocProofsRequest, rootHash []byte) []byte {
	buf := new(bytes.Buffer)

	binary.Write(buf, binary.LittleEndian, int64(req.PocStageStartBlockHeight))
	buf.Write(rootHash)
	binary.Write(buf, binary.LittleEndian, uint32(req.Count))
	for _, idx := range req.LeafIndices {
		binary.Write(buf, binary.LittleEndian, uint32(idx))
	}
	binary.Write(buf, binary.LittleEndian, int64(req.Timestamp))
	buf.WriteString(req.ValidatorAddress)
	buf.WriteString(req.ValidatorSignerAddress)

	hash := sha256.Sum256(buf.Bytes())
	// Return hex-encoded string as bytes (what Kotlin signs)
	return []byte(hex.EncodeToString(hash[:]))
}

// verifyPocProofsSignatureWithPubkey verifies the signature against a specific pubkey.
// The pubkey is base64-encoded.
func verifyPocProofsSignatureWithPubkey(req *PocProofsRequest, rootHash []byte, pubkeyB64 string) error {
	payload := buildPocProofsSignPayload(req, rootHash)

	signatureBytes, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid signature encoding")
	}

	pubkeyBytes, err := base64.StdEncoding.DecodeString(pubkeyB64)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "invalid pubkey encoding")
	}

	pubkey := secp256k1.PubKey{Key: pubkeyBytes}
	if pubkey.VerifySignature(payload, signatureBytes) {
		return nil
	}

	return echo.NewHTTPError(http.StatusUnauthorized, "signature verification failed")
}

// buildPocProofsResponseSignPayload builds the binary payload for response signature.
// Format: hex(SHA256(poc_stage_start_block_height(LE64) || root_hash(32) || count(LE32) ||
//
//	proofs_hash(32) || timestamp(LE64) || signer_address))
//
// proofs_hash = SHA256(concatenated: leaf_index(LE32) || nonce_value(LE32) || vector_bytes || proof_hashes...)
func buildPocProofsResponseSignPayload(
	pocStageStartBlockHeight int64,
	rootHash []byte,
	count uint32,
	proofs []PocProofItem,
	timestamp int64,
	signerAddress string,
) []byte {
	// Build proofs hash
	proofsHashBuf := new(bytes.Buffer)
	for _, p := range proofs {
		binary.Write(proofsHashBuf, binary.LittleEndian, p.LeafIndex)
		binary.Write(proofsHashBuf, binary.LittleEndian, p.NonceValue)
		proofsHashBuf.WriteString(p.VectorBytes)
		for _, proofHash := range p.Proof {
			proofsHashBuf.WriteString(proofHash)
		}
	}
	proofsHash := sha256.Sum256(proofsHashBuf.Bytes())

	// Build final payload
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, pocStageStartBlockHeight)
	buf.Write(rootHash)
	binary.Write(buf, binary.LittleEndian, count)
	buf.Write(proofsHash[:])
	binary.Write(buf, binary.LittleEndian, timestamp)
	buf.WriteString(signerAddress)

	hash := sha256.Sum256(buf.Bytes())
	// Return hex-encoded string as bytes (consistent with request signing)
	return []byte(hex.EncodeToString(hash[:]))
}
