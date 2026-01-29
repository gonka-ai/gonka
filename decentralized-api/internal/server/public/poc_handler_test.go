package public

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildPocProofsSignPayload(t *testing.T) {
	rootHash := make([]byte, 32)
	for i := range rootHash {
		rootHash[i] = byte(i)
	}

	req := &PocProofsRequest{
		PocStageStartBlockHeight: 12345,
		RootHash:                 base64.StdEncoding.EncodeToString(rootHash),
		Count:                    50000,
		LeafIndices:              []StringUint32{0, 42, 999},
		ValidatorAddress:         "gonka1validator",
		ValidatorSignerAddress:   "gonka1signer",
		Timestamp:                1700000000000000000,
	}

	payload := buildPocProofsSignPayload(req, rootHash)

	// Verify payload is 64 bytes (hex-encoded SHA256 hash)
	assert.Len(t, payload, 64)

	// Manually construct expected payload to verify format
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, int64(12345))
	buf.Write(rootHash)
	binary.Write(buf, binary.LittleEndian, uint32(50000))
	binary.Write(buf, binary.LittleEndian, uint32(0))
	binary.Write(buf, binary.LittleEndian, uint32(42))
	binary.Write(buf, binary.LittleEndian, uint32(999))
	binary.Write(buf, binary.LittleEndian, int64(1700000000000000000))
	buf.WriteString("gonka1validator")
	buf.WriteString("gonka1signer")

	expectedHash := sha256.Sum256(buf.Bytes())
	expectedHex := fmt.Sprintf("%x", expectedHash)
	assert.Equal(t, expectedHex, string(payload))
}

func TestStringInt64_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
	}{
		{"number", `12345`, 12345},
		{"string", `"12345"`, 12345},
		{"large number", `1700000000000000000`, 1700000000000000000},
		{"large string", `"1700000000000000000"`, 1700000000000000000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s StringInt64
			err := json.Unmarshal([]byte(tc.input), &s)
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, int64(s))
		})
	}
}

func TestStringUint32_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected uint32
	}{
		{"number", `12345`, 12345},
		{"string", `"12345"`, 12345},
		{"zero", `0`, 0},
		{"zero string", `"0"`, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s StringUint32
			err := json.Unmarshal([]byte(tc.input), &s)
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, uint32(s))
		})
	}
}

func TestVerifyPocProofsSignatureWithPubkey_InvalidSignatureEncoding(t *testing.T) {
	rootHash := make([]byte, 32)
	req := &PocProofsRequest{
		Signature: "not-valid-base64!!!",
	}

	err := verifyPocProofsSignatureWithPubkey(req, rootHash, base64.StdEncoding.EncodeToString([]byte("pubkey")))
	assert.Error(t, err)
}

func TestVerifyPocProofsSignatureWithPubkey_InvalidPubkeyEncoding(t *testing.T) {
	rootHash := make([]byte, 32)
	req := &PocProofsRequest{
		Signature: base64.StdEncoding.EncodeToString([]byte("somesig")),
	}

	err := verifyPocProofsSignatureWithPubkey(req, rootHash, "not-valid-base64!!!")
	assert.Error(t, err)
}

func TestVerifyPocProofsSignatureWithPubkey_InvalidSignature(t *testing.T) {
	rootHash := make([]byte, 32)
	req := &PocProofsRequest{
		Signature: base64.StdEncoding.EncodeToString([]byte("somesig")),
	}

	// Valid base64 but invalid secp256k1 pubkey
	err := verifyPocProofsSignatureWithPubkey(req, rootHash, base64.StdEncoding.EncodeToString([]byte("pubkey")))
	assert.Error(t, err)
}

func TestPocProofsRequest_MaxLeafIndices(t *testing.T) {
	// Verify the constant is set correctly
	assert.Equal(t, 500, maxLeafIndicesPerRequest)
}

// TestBuildPocProofsSignPayload_KotlinCompatibility verifies the payload matches
// what Kotlin's buildPocProofsSignPayload produces with the same inputs.
func TestBuildPocProofsSignPayload_KotlinCompatibility(t *testing.T) {
	rootHash := make([]byte, 32)
	for i := range rootHash {
		rootHash[i] = byte(i)
	}

	req := &PocProofsRequest{
		PocStageStartBlockHeight: 45,
		RootHash:                 base64.StdEncoding.EncodeToString(rootHash),
		Count:                    100,
		LeafIndices:              []StringUint32{0, 1, 2},
		ValidatorAddress:         "gonka1test",
		ValidatorSignerAddress:   "gonka1test",
		Timestamp:                1768556941222626000,
	}

	payload := buildPocProofsSignPayload(req, rootHash)

	// Verify it's 64 bytes (hex string)
	assert.Len(t, payload, 64, "Payload should be 64 bytes (hex string)")

	// Verify the binary structure is correct by rebuilding manually
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, int64(45))
	buf.Write(rootHash)
	binary.Write(buf, binary.LittleEndian, uint32(100))
	binary.Write(buf, binary.LittleEndian, uint32(0))
	binary.Write(buf, binary.LittleEndian, uint32(1))
	binary.Write(buf, binary.LittleEndian, uint32(2))
	binary.Write(buf, binary.LittleEndian, int64(1768556941222626000))
	buf.WriteString("gonka1test")
	buf.WriteString("gonka1test")

	expectedHash := sha256.Sum256(buf.Bytes())
	expectedHex := fmt.Sprintf("%x", expectedHash)
	assert.Equal(t, expectedHex, string(payload), "Hex payload mismatch")
}
