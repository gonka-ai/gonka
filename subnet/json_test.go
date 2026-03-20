package subnet

import (
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalizeJSON_SortsKeys(t *testing.T) {
	// Keys in different order must produce identical output.
	a := []byte(`{"b":2,"a":1}`)
	b := []byte(`{"a":1,"b":2}`)

	ca, err := CanonicalizeJSON(a)
	require.NoError(t, err)
	cb, err := CanonicalizeJSON(b)
	require.NoError(t, err)
	require.Equal(t, ca, cb)
}

func TestCanonicalizeJSON_NoTrailingNewline(t *testing.T) {
	data := []byte(`{"key":"value"}`)
	result, err := CanonicalizeJSON(data)
	require.NoError(t, err)
	require.NotEqual(t, byte('\n'), result[len(result)-1], "trailing newline must be trimmed")
}

func TestCanonicalizeJSON_Idempotent(t *testing.T) {
	data := []byte(`{"z":3,"a":1,"m":2}`)
	first, err := CanonicalizeJSON(data)
	require.NoError(t, err)
	second, err := CanonicalizeJSON(first)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestCanonicalizeJSON_InvalidJSON(t *testing.T) {
	_, err := CanonicalizeJSON([]byte("not json"))
	require.Error(t, err)
}

func TestCanonicalPromptHash_Deterministic(t *testing.T) {
	prompt := []byte(`{"model":"llama","messages":[{"role":"user","content":"hello"}]}`)

	h1, err := CanonicalPromptHash(prompt)
	require.NoError(t, err)
	h2, err := CanonicalPromptHash(prompt)
	require.NoError(t, err)
	require.Equal(t, h1, h2)
}

func TestCanonicalPromptHash_KeyOrderIndependent(t *testing.T) {
	// Same content, different key order.
	a := []byte(`{"model":"llama","messages":[{"role":"user","content":"hello"}]}`)
	b := []byte(`{"messages":[{"role":"user","content":"hello"}],"model":"llama"}`)

	ha, err := CanonicalPromptHash(a)
	require.NoError(t, err)
	hb, err := CanonicalPromptHash(b)
	require.NoError(t, err)
	require.Equal(t, ha, hb, "same content with different key order must produce same hash")
}

func TestCanonicalPromptHash_MatchesCanonicalThenSha256(t *testing.T) {
	// Verify the hash equals sha256(canonicalize(prompt)) -- the contract
	// that user SDK, host, engine storage, and validator all rely on.
	prompt := []byte(`{"model":"llama","messages":[{"role":"user","content":"test"}]}`)

	canonical, err := CanonicalizeJSON(prompt)
	require.NoError(t, err)
	expected := sha256.Sum256(canonical)

	hash, err := CanonicalPromptHash(prompt)
	require.NoError(t, err)
	require.Equal(t, expected[:], hash)
}

func TestCanonicalPromptHash_WhitespaceInsensitive(t *testing.T) {
	compact := []byte(`{"model":"llama","messages":[{"role":"user","content":"hi"}]}`)
	spaced := []byte(`{ "model" : "llama" , "messages" : [ { "role" : "user" , "content" : "hi" } ] }`)

	hc, err := CanonicalPromptHash(compact)
	require.NoError(t, err)
	hs, err := CanonicalPromptHash(spaced)
	require.NoError(t, err)
	require.Equal(t, hc, hs, "whitespace differences must not affect hash")
}

func TestCanonicalPromptHash_DiffersFromRawSha256(t *testing.T) {
	// Canonicalization sorts keys, so raw sha256 of unsorted JSON differs.
	unsorted := []byte(`{"b":2,"a":1}`)

	raw := sha256.Sum256(unsorted)
	canonical, err := CanonicalPromptHash(unsorted)
	require.NoError(t, err)
	require.NotEqual(t, raw[:], canonical, "raw sha256 of unsorted JSON must differ from canonical hash")
}

func TestCanonicalizeJSON_PreservesArrayOrder(t *testing.T) {
	data := []byte(`{"arr":[3,1,2]}`)
	result, err := CanonicalizeJSON(data)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &parsed))
	arr := parsed["arr"].([]interface{})
	require.Equal(t, float64(3), arr[0])
	require.Equal(t, float64(1), arr[1])
	require.Equal(t, float64(2), arr[2])
}

func TestCanonicalPromptHash_StoredPayloadMatchesDirectHash(t *testing.T) {
	// Simulates the full lifecycle:
	// 1. User computes CanonicalPromptHash(original) -> stored in MsgStartInference.PromptHash
	// 2. Engine stores CanonicalizeJSON(original) as prompt payload
	// 3. Validator fetches stored payload and computes sha256(payload)
	// 4. Validator compares sha256(payload) == PromptHash
	//
	// This must hold for hash verification to pass.
	original := []byte(`{"model":"llama","messages":[{"role":"user","content":"prompt"}]}`)

	// Step 1: user side
	promptHash, err := CanonicalPromptHash(original)
	require.NoError(t, err)

	// Step 2: engine stores canonical form
	stored, err := CanonicalizeJSON(original)
	require.NoError(t, err)

	// Step 3+4: validator computes sha256 of stored payload
	validatorHash := sha256.Sum256(stored)
	require.Equal(t, promptHash, validatorHash[:],
		"validator sha256(stored_canonical) must equal user CanonicalPromptHash(original)")
}
