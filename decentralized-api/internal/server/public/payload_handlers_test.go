package public

import (
	"encoding/base64"
	"testing"
	"time"

	"decentralized-api/utils"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/stretchr/testify/require"
)

func TestValidatePayloadRequestSignature_ValidSignature(t *testing.T) {
	validatorKey := newTestKey()
	inferenceId := "aW5mZXJlbmNlLTEyMzQ1" // base64 encoded
	timestamp := time.Now().UnixNano()
	validatorAddress := "cosmos1validatoraddress"

	// Validator signs: inferenceId + timestamp + validatorAddress
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		Timestamp:       timestamp,
		TransferAddress: validatorAddress,
		ExecutorAddress: "",
	}
	signature, err := calculations.Sign(validatorKey, components, calculations.Developer)
	require.NoError(t, err)

	err = validatePayloadRequestSignature(inferenceId, timestamp, validatorAddress, []string{validatorKey.GetPubKeyBase64()}, signature)
	require.NoError(t, err)
}

func TestValidatePayloadRequestSignature_InvalidSignature(t *testing.T) {
	validatorKey := newTestKey()
	wrongKey := newTestKey()
	inferenceId := "aW5mZXJlbmNlLTEyMzQ1"
	timestamp := time.Now().UnixNano()
	validatorAddress := "cosmos1validatoraddress"

	// Sign with wrong key
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		Timestamp:       timestamp,
		TransferAddress: validatorAddress,
		ExecutorAddress: "",
	}
	signature, err := calculations.Sign(wrongKey, components, calculations.Developer)
	require.NoError(t, err)

	// Validate with validator's pubkey - should fail
	err = validatePayloadRequestSignature(inferenceId, timestamp, validatorAddress, []string{validatorKey.GetPubKeyBase64()}, signature)
	require.Error(t, err)
}

func TestValidatePayloadRequestSignature_WrongTimestamp(t *testing.T) {
	validatorKey := newTestKey()
	inferenceId := "aW5mZXJlbmNlLTEyMzQ1"
	timestamp := time.Now().UnixNano()
	validatorAddress := "cosmos1validatoraddress"

	// Sign with correct timestamp
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		Timestamp:       timestamp,
		TransferAddress: validatorAddress,
		ExecutorAddress: "",
	}
	signature, err := calculations.Sign(validatorKey, components, calculations.Developer)
	require.NoError(t, err)

	// Validate with different timestamp - should fail
	err = validatePayloadRequestSignature(inferenceId, timestamp+1000, validatorAddress, []string{validatorKey.GetPubKeyBase64()}, signature)
	require.Error(t, err)
}

func TestValidatePayloadRequestSignature_WrongInferenceId(t *testing.T) {
	validatorKey := newTestKey()
	inferenceId := "aW5mZXJlbmNlLTEyMzQ1"
	timestamp := time.Now().UnixNano()
	validatorAddress := "cosmos1validatoraddress"

	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		Timestamp:       timestamp,
		TransferAddress: validatorAddress,
		ExecutorAddress: "",
	}
	signature, err := calculations.Sign(validatorKey, components, calculations.Developer)
	require.NoError(t, err)

	// Validate with different inferenceId - should fail
	err = validatePayloadRequestSignature("different-inference-id", timestamp, validatorAddress, []string{validatorKey.GetPubKeyBase64()}, signature)
	require.Error(t, err)
}

func TestValidatePayloadRequestSignature_MultipleGrantees(t *testing.T) {
	validatorKey := newTestKey()
	grantee1 := newTestKey()
	grantee2 := newTestKey()
	inferenceId := "aW5mZXJlbmNlLTEyMzQ1"
	timestamp := time.Now().UnixNano()
	validatorAddress := "cosmos1validatoraddress"

	// Sign with grantee2 (warm key)
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		Timestamp:       timestamp,
		TransferAddress: validatorAddress,
		ExecutorAddress: "",
	}
	signature, err := calculations.Sign(grantee2, components, calculations.Developer)
	require.NoError(t, err)

	// Should succeed when grantee2's pubkey is in the list
	pubkeys := []string{validatorKey.GetPubKeyBase64(), grantee1.GetPubKeyBase64(), grantee2.GetPubKeyBase64()}
	err = validatePayloadRequestSignature(inferenceId, timestamp, validatorAddress, pubkeys, signature)
	require.NoError(t, err)
}

func TestValidatePayloadRequestTimestamp_Valid(t *testing.T) {
	s := &Server{}

	// Current timestamp - should be valid
	err := s.validatePayloadRequestTimestamp(time.Now().UnixNano())
	require.NoError(t, err)

	// 30 seconds ago - should be valid
	err = s.validatePayloadRequestTimestamp(time.Now().Add(-30 * time.Second).UnixNano())
	require.NoError(t, err)

	// 5 seconds in future - should be valid
	err = s.validatePayloadRequestTimestamp(time.Now().Add(5 * time.Second).UnixNano())
	require.NoError(t, err)
}

func TestValidatePayloadRequestTimestamp_TooOld(t *testing.T) {
	s := &Server{}

	// 61 seconds ago - should fail
	err := s.validatePayloadRequestTimestamp(time.Now().Add(-61 * time.Second).UnixNano())
	require.Error(t, err)
	require.Contains(t, err.Error(), "too old")
}

func TestValidatePayloadRequestTimestamp_TooFarInFuture(t *testing.T) {
	s := &Server{}

	// 15 seconds in future - should fail (>10s)
	err := s.validatePayloadRequestTimestamp(time.Now().Add(15 * time.Second).UnixNano())
	require.Error(t, err)
	require.Contains(t, err.Error(), "future")
}

func TestExecutorSignature_Format(t *testing.T) {
	// Test that executor signature format is consistent with verification
	executorKey := newTestKey()
	inferenceId := "aW5mZXJlbmNlLTEyMzQ1"
	promptPayload := `{"model":"test","seed":123}`
	responsePayload := `{"choices":[{"message":{"content":"hello"}}]}`
	executorAddress := "cosmos1executoraddress"

	// Compute hashes as executor does
	promptHash := utils.GenerateSHA256Hash(promptPayload)
	responseHash := utils.GenerateSHA256Hash(responsePayload)
	payload := inferenceId + promptHash + responseHash

	// Sign with timestamp=0 (non-repudiation signature)
	components := calculations.SignatureComponents{
		Payload:         payload,
		Timestamp:       0,
		TransferAddress: executorAddress,
		ExecutorAddress: "",
	}
	signature, err := calculations.Sign(executorKey, components, calculations.Developer)
	require.NoError(t, err)

	// Verify with same components
	err = calculations.ValidateSignature(components, calculations.Developer, executorKey.GetPubKeyBase64(), signature)
	require.NoError(t, err)
}

func TestExecutorSignature_HashMismatchDetection(t *testing.T) {
	executorKey := newTestKey()
	inferenceId := "aW5mZXJlbmNlLTEyMzQ1"
	promptPayload := `{"model":"test","seed":123}`
	responsePayload := `{"choices":[{"message":{"content":"hello"}}]}`
	executorAddress := "cosmos1executoraddress"

	// Sign correct payloads
	promptHash := utils.GenerateSHA256Hash(promptPayload)
	responseHash := utils.GenerateSHA256Hash(responsePayload)
	payload := inferenceId + promptHash + responseHash

	components := calculations.SignatureComponents{
		Payload:         payload,
		Timestamp:       0,
		TransferAddress: executorAddress,
		ExecutorAddress: "",
	}
	signature, err := calculations.Sign(executorKey, components, calculations.Developer)
	require.NoError(t, err)

	// Verify with tampered payload - should fail
	tamperedPayload := `{"choices":[{"message":{"content":"tampered"}}]}`
	tamperedResponseHash := utils.GenerateSHA256Hash(tamperedPayload)
	tamperedComponents := calculations.SignatureComponents{
		Payload:         inferenceId + promptHash + tamperedResponseHash,
		Timestamp:       0,
		TransferAddress: executorAddress,
		ExecutorAddress: "",
	}
	err = calculations.ValidateSignature(tamperedComponents, calculations.Developer, executorKey.GetPubKeyBase64(), signature)
	require.Error(t, err)
}

// Helper to generate test pubkey in base64
func pubKeyToBase64(key *secp256k1.PrivKey) string {
	return base64.StdEncoding.EncodeToString(key.PubKey().Bytes())
}


