package validation

import (
	"math/rand"
	"testing"

	"decentralized-api/completionapi"

	"github.com/stretchr/testify/require"
)

func TestGenerateRunSeed(t *testing.T) {
	// Test deterministic seed generation
	seed1 := generateRunSeed(42, "inference-123")
	seed2 := generateRunSeed(42, "inference-123")
	require.Equal(t, seed1, seed2, "same inputs should produce same seed")

	// Test different inputs produce different seeds
	seed3 := generateRunSeed(42, "inference-456")
	require.NotEqual(t, seed1, seed3, "different inference_id should produce different seed")

	seed4 := generateRunSeed(43, "inference-123")
	require.NotEqual(t, seed1, seed4, "different user_seed should produce different seed")

	// Test seed is positive
	for i := int32(-1000); i < 1000; i++ {
		seed := generateRunSeed(i, "test-inference")
		require.Greater(t, seed, int64(0), "seed should always be positive")
	}
}

func TestVerifyReproducibleSampling_Valid(t *testing.T) {
	userSeed := int32(42)
	inferenceId := "test-inference-123"

	// Generate a valid token sequence using the same RNG logic
	runSeed := generateRunSeed(userSeed, inferenceId)
	source := rand.NewSource(runSeed)
	rng := rand.New(source)

	// Create top-k tokens and sample from them
	tokens := make([]completionapi.EnforcedToken, 10)
	for i := 0; i < 10; i++ {
		topTokens := []string{"token_a", "token_b", "token_c", "token_d", "token_e"}
		sampledIndex := rng.Intn(len(topTokens))
		tokens[i] = completionapi.EnforcedToken{
			Token:     topTokens[sampledIndex],
			TopTokens: topTokens,
		}
	}

	enforcedTokens := completionapi.EnforcedTokens{Tokens: tokens}

	result := VerifyReproducibleSampling(userSeed, inferenceId, enforcedTokens)
	require.True(t, result.Valid, "valid sequence should pass: %s", result.Message)
	require.Equal(t, int64(-1), result.FailedPosition)
}

func TestVerifyReproducibleSampling_Invalid(t *testing.T) {
	userSeed := int32(42)
	inferenceId := "test-inference-123"

	// Create a token sequence that was NOT sampled correctly
	tokens := []completionapi.EnforcedToken{
		{
			Token:     "wrong_token", // This is not in the expected position
			TopTokens: []string{"token_a", "token_b", "token_c", "token_d", "token_e"},
		},
	}

	enforcedTokens := completionapi.EnforcedTokens{Tokens: tokens}

	result := VerifyReproducibleSampling(userSeed, inferenceId, enforcedTokens)
	require.False(t, result.Valid, "invalid sequence should fail")
	require.Equal(t, int64(0), result.FailedPosition)
}

func TestVerifyReproducibleSampling_EmptyTokens(t *testing.T) {
	result := VerifyReproducibleSampling(42, "test-inference", completionapi.EnforcedTokens{})
	require.False(t, result.Valid)
	require.Contains(t, result.Message, "no tokens")
}

func TestVerifyReproducibleSampling_PartialFailure(t *testing.T) {
	userSeed := int32(42)
	inferenceId := "test-inference-123"

	// Generate valid tokens for first 5 positions
	runSeed := generateRunSeed(userSeed, inferenceId)
	source := rand.NewSource(runSeed)
	rng := rand.New(source)

	tokens := make([]completionapi.EnforcedToken, 10)
	topTokens := []string{"token_a", "token_b", "token_c", "token_d", "token_e"}

	for i := 0; i < 10; i++ {
		sampledIndex := rng.Intn(len(topTokens))
		if i < 5 {
			// Valid token
			tokens[i] = completionapi.EnforcedToken{
				Token:     topTokens[sampledIndex],
				TopTokens: topTokens,
			}
		} else {
			// Invalid token - use wrong token deliberately
			wrongToken := "wrong_token"
			tokens[i] = completionapi.EnforcedToken{
				Token:     wrongToken,
				TopTokens: topTokens,
			}
		}
	}

	enforcedTokens := completionapi.EnforcedTokens{Tokens: tokens}

	result := VerifyReproducibleSampling(userSeed, inferenceId, enforcedTokens)
	require.False(t, result.Valid, "sequence with wrong token should fail")
	require.Equal(t, int64(5), result.FailedPosition, "should fail at position 5")
}

func TestCheckSequenceReproducibility(t *testing.T) {
	userSeed := int32(42)
	inferenceId := "test-inference-123"

	// Generate a valid token sequence
	runSeed := generateRunSeed(userSeed, inferenceId)
	source := rand.NewSource(runSeed)
	rng := rand.New(source)

	tokens := make([]completionapi.EnforcedToken, 5)
	topTokens := []string{"token_a", "token_b", "token_c"}
	for i := 0; i < 5; i++ {
		sampledIndex := rng.Intn(len(topTokens))
		tokens[i] = completionapi.EnforcedToken{
			Token:     topTokens[sampledIndex],
			TopTokens: topTokens,
		}
	}

	enforcedTokens := completionapi.EnforcedTokens{Tokens: tokens}

	// Valid sequence should return nil error
	err := CheckSequenceReproducibility(userSeed, inferenceId, enforcedTokens)
	require.NoError(t, err)

	// Invalid sequence should return error
	tokens[0].Token = "wrong_token"
	err = CheckSequenceReproducibility(userSeed, inferenceId, enforcedTokens)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSequenceCheckFailed)
}

func TestVerifyReproducibleSampling_DifferentSeeds(t *testing.T) {
	inferenceId := "test-inference-123"

	// Same token sequence verified with different seeds should fail
	topTokens := []string{"token_a", "token_b", "token_c", "token_d", "token_e"}

	// Generate sequence with seed 42
	seed1 := int32(42)
	runSeed1 := generateRunSeed(seed1, inferenceId)
	source1 := rand.NewSource(runSeed1)
	rng1 := rand.New(source1)

	tokens := make([]completionapi.EnforcedToken, 5)
	for i := 0; i < 5; i++ {
		sampledIndex := rng1.Intn(len(topTokens))
		tokens[i] = completionapi.EnforcedToken{
			Token:     topTokens[sampledIndex],
			TopTokens: topTokens,
		}
	}

	enforcedTokens := completionapi.EnforcedTokens{Tokens: tokens}

	// Verify with correct seed should pass
	result1 := VerifyReproducibleSampling(seed1, inferenceId, enforcedTokens)
	require.True(t, result1.Valid, "correct seed should pass")

	// Verify with different seed should fail (in most cases)
	// Note: There's a tiny probability of collision, but it's negligible
	seed2 := int32(43)
	result2 := VerifyReproducibleSampling(seed2, inferenceId, enforcedTokens)
	// This should typically fail, but we can't guarantee it fails at a specific position
	// due to the probabilistic nature. For a robust test, we just check it produces a result.
	t.Logf("Result with wrong seed: valid=%v, message=%s", result2.Valid, result2.Message)
	require.False(t, result2.Valid, "different seed should produce different sampling")
}
