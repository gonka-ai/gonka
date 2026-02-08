package validation

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"

	"decentralized-api/completionapi"
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// SequenceCheckResult contains the result of verifying reproducible sampling.
type SequenceCheckResult struct {
	// Valid indicates whether all tokens were correctly sampled from their top-k distributions.
	Valid bool
	// FailedPosition is the first position where sampling verification failed (-1 if valid).
	FailedPosition int64
	// Message provides details about the verification result.
	Message string
}

// Maximum limits for input validation
const (
	// maxTokensToVerify is the maximum number of tokens allowed in a single verification request
	maxTokensToVerify int64 = 100000
	// maxTopTokens is the maximum number of top tokens allowed per position
	maxTopTokens int64 = 1000
)

// VerifyReproducibleSampling performs the Stage 1 "Sequence Check" from the inference
// validation proposal. It verifies that each chosen token was correctly sampled from
// the top-k probability distribution using deterministic RNG seeded with run_seed.
//
// This check protects against speculative decoding attacks and pre-fill attacks by
// ensuring that the token sequence is cryptographically bound to:
// 1. The seed (derived from user_seed and inference_id)
// 2. The top-k probability distributions at each position
//
// Parameters:
//   - userSeed: The user-provided seed from the request
//   - inferenceId: The unique inference identifier
//   - enforcedTokens: The token sequence with top-k candidates at each position
//
// Returns:
//   - SequenceCheckResult indicating whether sampling is reproducible
func VerifyReproducibleSampling(userSeed int32, inferenceId string, enforcedTokens completionapi.EnforcedTokens) SequenceCheckResult {
	if len(enforcedTokens.Tokens) == 0 {
		return SequenceCheckResult{
			Valid:          false,
			FailedPosition: -1,
			Message:        "no tokens to verify",
		}
	}

	// Validate token count doesn't exceed maximum
	if int64(len(enforcedTokens.Tokens)) > maxTokensToVerify {
		return SequenceCheckResult{
			Valid:          false,
			FailedPosition: -1,
			Message:        fmt.Sprintf("token count %d exceeds maximum %d", len(enforcedTokens.Tokens), maxTokensToVerify),
		}
	}

	// Generate run_seed = SHA256(user_seed || inference_id)
	// This ensures deterministic but unique seed per inference
	runSeed := generateRunSeed(userSeed, inferenceId)

	// Initialize RNG with run_seed
	source := rand.NewSource(runSeed)
	rng := rand.New(source)

	// Verify each position
	for i := int64(0); i < int64(len(enforcedTokens.Tokens)); i++ {
		tokenInfo := enforcedTokens.Tokens[i]
		if len(tokenInfo.TopTokens) == 0 {
			logging.Warn("VerifyReproducibleSampling: position has no top tokens",
				types.Validation,
				"position", i,
				"inferenceId", inferenceId)
			continue
		}

		// Validate top tokens count doesn't exceed maximum
		k := int64(len(tokenInfo.TopTokens))
		if k > maxTopTokens {
			return SequenceCheckResult{
				Valid:          false,
				FailedPosition: i,
				Message:        fmt.Sprintf("top tokens count %d at position %d exceeds maximum %d", k, i, maxTopTokens),
			}
		}

		// Sample an index from [0, k) using the RNG
		sampledIndex := rng.Intn(int(k))

		// The chosen token must be the one at the sampled index
		expectedToken := tokenInfo.TopTokens[sampledIndex]
		actualToken := tokenInfo.Token

		if expectedToken != actualToken {
			logging.Warn("VerifyReproducibleSampling: token mismatch detected",
				types.Validation,
				"position", i,
				"inferenceId", inferenceId,
				"expectedToken", expectedToken,
				"actualToken", actualToken,
				"sampledIndex", sampledIndex,
				"topK", k)

			return SequenceCheckResult{
				Valid:          false,
				FailedPosition: i,
				Message: fmt.Sprintf("token mismatch at position %d: expected %q (index %d), got %q",
					i, expectedToken, sampledIndex, actualToken),
			}
		}
	}

	logging.Debug("VerifyReproducibleSampling: all tokens verified",
		types.Validation,
		"inferenceId", inferenceId,
		"tokenCount", len(enforcedTokens.Tokens))

	return SequenceCheckResult{
		Valid:          true,
		FailedPosition: -1,
		Message:        fmt.Sprintf("all %d tokens verified successfully", len(enforcedTokens.Tokens)),
	}
}

// generateRunSeed creates a deterministic seed from user_seed and inference_id.
// The seed is derived using: SHA256(user_seed || inference_id)
// This matches the proposal specification for reproducible sampling.
func generateRunSeed(userSeed int32, inferenceId string) int64 {
	// Create input: user_seed (as 4 bytes big-endian) || inference_id (as bytes)
	seedBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seedBytes, uint32(userSeed))

	input := append(seedBytes, []byte(inferenceId)...)

	// Compute SHA256 hash
	hash := sha256.Sum256(input)

	// Extract int64 from first 8 bytes (ensure positive by masking sign bit)
	runSeed := int64(binary.BigEndian.Uint64(hash[:8]) & ((1 << 63) - 1))

	// Avoid zero seed which could cause issues with some RNG implementations
	if runSeed == 0 {
		runSeed = 1
	}

	return runSeed
}

// ErrSequenceCheckFailed indicates that the sequence check failed.
var ErrSequenceCheckFailed = errors.New("sequence check failed: token sampling is not reproducible")

// CheckSequenceReproducibility is a convenience function that returns an error
// if the sequence check fails. This can be used in validation pipelines.
func CheckSequenceReproducibility(userSeed int32, inferenceId string, enforcedTokens completionapi.EnforcedTokens) error {
	result := VerifyReproducibleSampling(userSeed, inferenceId, enforcedTokens)
	if !result.Valid {
		logging.Error("Sequence check failed",
			types.Validation,
			"inferenceId", inferenceId,
			"failedPosition", result.FailedPosition,
			"message", result.Message)
		return fmt.Errorf("%w: %s", ErrSequenceCheckFailed, result.Message)
	}
	return nil
}
