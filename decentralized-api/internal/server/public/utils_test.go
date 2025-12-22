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

// testKey wraps a secp256k1 private key for testing
type testKey struct {
	key *secp256k1.PrivKey
}

func newTestKey() *testKey {
	return &testKey{key: secp256k1.GenPrivKey()}
}

func (t *testKey) GetPubKeyBase64() string {
	return base64.StdEncoding.EncodeToString(t.key.PubKey().Bytes())
}

func (t *testKey) SignBytes(msg []byte) (string, error) {
	signature, err := t.key.Sign(msg)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func TestValidateTransferRequest_ValidSignature(t *testing.T) {
	devKey := newTestKey()
	timestamp := time.Now().UnixNano()
	transferAddress := "cosmos1transferaddress"
	body := `{"model":"test","messages":[{"role":"user","content":"hello"}]}`

	// Phase 3: Dev signs hash(original_prompt) + timestamp + ta_address
	originalPromptHash := utils.GenerateSHA256Hash(body)
	components := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: "", // Dev doesn't include executor
	}
	signature, err := calculations.Sign(devKey, components, calculations.Developer)
	require.NoError(t, err)

	request := &ChatRequest{
		Body:            []byte(body),
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		AuthKey:         signature,
	}

	err = validateTransferRequest(request, devKey.GetPubKeyBase64())
	require.NoError(t, err)
}

func TestValidateTransferRequest_InvalidSignature(t *testing.T) {
	devKey := newTestKey()
	wrongKey := newTestKey()
	timestamp := time.Now().UnixNano()
	transferAddress := "cosmos1transferaddress"
	body := `{"model":"test","messages":[{"role":"user","content":"hello"}]}`

	// Sign with wrong key
	originalPromptHash := utils.GenerateSHA256Hash(body)
	components := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: "",
	}
	signature, err := calculations.Sign(wrongKey, components, calculations.Developer)
	require.NoError(t, err)

	request := &ChatRequest{
		Body:            []byte(body),
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		AuthKey:         signature,
	}

	// Validate with dev's pubkey - should fail
	err = validateTransferRequest(request, devKey.GetPubKeyBase64())
	require.Error(t, err)
}

func TestValidateTransferRequest_WrongTimestamp(t *testing.T) {
	devKey := newTestKey()
	timestamp := time.Now().UnixNano()
	transferAddress := "cosmos1transferaddress"
	body := `{"model":"test","messages":[{"role":"user","content":"hello"}]}`

	// Sign with correct timestamp
	originalPromptHash := utils.GenerateSHA256Hash(body)
	components := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: "",
	}
	signature, err := calculations.Sign(devKey, components, calculations.Developer)
	require.NoError(t, err)

	// Request with wrong timestamp
	request := &ChatRequest{
		Body:            []byte(body),
		Timestamp:       timestamp + 1000, // Different timestamp
		TransferAddress: transferAddress,
		AuthKey:         signature,
	}

	err = validateTransferRequest(request, devKey.GetPubKeyBase64())
	require.Error(t, err)
}

func TestValidateExecuteRequestWithGrantees_ValidSignature(t *testing.T) {
	taKey := newTestKey()
	timestamp := time.Now().UnixNano()
	transferAddress := "cosmos1transferaddress"
	executorAddress := "cosmos1executoraddress"
	promptHash := "abc123def456"

	// Phase 3: TA signs prompt_hash + timestamp + ta_address + executor_address
	components := calculations.SignatureComponents{
		Payload:         promptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: executorAddress,
	}
	signature, err := calculations.Sign(taKey, components, calculations.TransferAgent)
	require.NoError(t, err)

	request := &ChatRequest{
		PromptHash:      promptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
	}

	err = validateExecuteRequestWithGrantees(request, []string{taKey.GetPubKeyBase64()}, executorAddress, signature)
	require.NoError(t, err)
}

func TestValidateExecuteRequestWithGrantees_InvalidSignature(t *testing.T) {
	taKey := newTestKey()
	wrongKey := newTestKey()
	timestamp := time.Now().UnixNano()
	transferAddress := "cosmos1transferaddress"
	executorAddress := "cosmos1executoraddress"
	promptHash := "abc123def456"

	// Sign with wrong key
	components := calculations.SignatureComponents{
		Payload:         promptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: executorAddress,
	}
	signature, err := calculations.Sign(wrongKey, components, calculations.TransferAgent)
	require.NoError(t, err)

	request := &ChatRequest{
		PromptHash:      promptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
	}

	// Validate with TA's pubkey - should fail
	err = validateExecuteRequestWithGrantees(request, []string{taKey.GetPubKeyBase64()}, executorAddress, signature)
	require.Error(t, err)
}

func TestValidateExecuteRequestWithGrantees_MultipleGrantees(t *testing.T) {
	taKey := newTestKey()
	grantee1 := newTestKey()
	grantee2 := newTestKey()
	timestamp := time.Now().UnixNano()
	transferAddress := "cosmos1transferaddress"
	executorAddress := "cosmos1executoraddress"
	promptHash := "abc123def456"

	// Sign with grantee2 (not the primary TA)
	components := calculations.SignatureComponents{
		Payload:         promptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: executorAddress,
	}
	signature, err := calculations.Sign(grantee2, components, calculations.TransferAgent)
	require.NoError(t, err)

	request := &ChatRequest{
		PromptHash:      promptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
	}

	// Should succeed when grantee2's pubkey is in the list
	pubkeys := []string{taKey.GetPubKeyBase64(), grantee1.GetPubKeyBase64(), grantee2.GetPubKeyBase64()}
	err = validateExecuteRequestWithGrantees(request, pubkeys, executorAddress, signature)
	require.NoError(t, err)
}

func TestValidateExecuteRequestWithGrantees_FallbackToHashedBody(t *testing.T) {
	taKey := newTestKey()
	timestamp := time.Now().UnixNano()
	transferAddress := "cosmos1transferaddress"
	executorAddress := "cosmos1executoraddress"
	body := `{"model":"test"}`

	// Phase 3 backward compatibility: When PromptHash is empty, compute hash(Body)
	bodyHash := utils.GenerateSHA256Hash(body)
	components := calculations.SignatureComponents{
		Payload:         bodyHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: executorAddress,
	}
	signature, err := calculations.Sign(taKey, components, calculations.TransferAgent)
	require.NoError(t, err)

	request := &ChatRequest{
		Body:            []byte(body),
		PromptHash:      "", // Empty - should fall back to hash(Body)
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
	}

	err = validateExecuteRequestWithGrantees(request, []string{taKey.GetPubKeyBase64()}, executorAddress, signature)
	require.NoError(t, err)
}

