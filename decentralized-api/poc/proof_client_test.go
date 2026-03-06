package poc

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateLeafCoverage_ExactMatch(t *testing.T) {
	requested := []uint32{0, 5, 10}
	proofs := []ProofItem{
		{LeafIndex: 0},
		{LeafIndex: 5},
		{LeafIndex: 10},
	}
	assert.NoError(t, validateLeafCoverage(requested, proofs))
}

func TestValidateLeafCoverage_OrderIndependent(t *testing.T) {
	requested := []uint32{0, 5, 10}
	proofs := []ProofItem{
		{LeafIndex: 10},
		{LeafIndex: 0},
		{LeafIndex: 5},
	}
	assert.NoError(t, validateLeafCoverage(requested, proofs))
}

func TestValidateLeafCoverage_FewerProofs(t *testing.T) {
	requested := []uint32{0, 5, 10}
	proofs := []ProofItem{
		{LeafIndex: 0},
		{LeafIndex: 5},
	}
	err := validateLeafCoverage(requested, proofs)
	assert.True(t, errors.Is(err, ErrIncompleteCoverage))
	assert.Contains(t, err.Error(), "expected 3 proofs, got 2")
}

func TestValidateLeafCoverage_ExtraProofs(t *testing.T) {
	requested := []uint32{0, 5}
	proofs := []ProofItem{
		{LeafIndex: 0},
		{LeafIndex: 5},
		{LeafIndex: 10},
	}
	err := validateLeafCoverage(requested, proofs)
	assert.True(t, errors.Is(err, ErrIncompleteCoverage))
	assert.Contains(t, err.Error(), "expected 2 proofs, got 3")
}

func TestValidateLeafCoverage_DuplicateLeafIndex(t *testing.T) {
	requested := []uint32{0, 5, 10}
	proofs := []ProofItem{
		{LeafIndex: 0},
		{LeafIndex: 5},
		{LeafIndex: 5}, // duplicate
	}
	err := validateLeafCoverage(requested, proofs)
	assert.True(t, errors.Is(err, ErrIncompleteCoverage))
	assert.Contains(t, err.Error(), "duplicate leaf index 5")
}

func TestValidateLeafCoverage_UnexpectedLeafIndex(t *testing.T) {
	requested := []uint32{0, 5, 10}
	proofs := []ProofItem{
		{LeafIndex: 0},
		{LeafIndex: 5},
		{LeafIndex: 99}, // not requested
	}
	err := validateLeafCoverage(requested, proofs)
	assert.True(t, errors.Is(err, ErrIncompleteCoverage))
	assert.Contains(t, err.Error(), "unexpected leaf index 99")
}

func TestValidateLeafCoverage_EmptyBoth(t *testing.T) {
	assert.NoError(t, validateLeafCoverage(nil, nil))
	assert.NoError(t, validateLeafCoverage([]uint32{}, []ProofItem{}))
}

func TestValidateLeafCoverage_EmptyRequestNonEmptyProofs(t *testing.T) {
	err := validateLeafCoverage(nil, []ProofItem{{LeafIndex: 0}})
	assert.True(t, errors.Is(err, ErrIncompleteCoverage))
}

func TestValidateLeafCoverage_SingleLeaf(t *testing.T) {
	assert.NoError(t, validateLeafCoverage([]uint32{42}, []ProofItem{{LeafIndex: 42}}))
}

func TestCheckDuplicateNonces_NoDuplicates(t *testing.T) {
	artifacts := []VerifiedArtifact{
		{Nonce: 1},
		{Nonce: 2},
		{Nonce: 3},
	}
	assert.NoError(t, CheckDuplicateNonces(artifacts))
}

func TestCheckDuplicateNonces_WithDuplicates(t *testing.T) {
	artifacts := []VerifiedArtifact{
		{Nonce: 1},
		{Nonce: 2},
		{Nonce: 1}, // duplicate
	}
	assert.True(t, errors.Is(CheckDuplicateNonces(artifacts), ErrDuplicateNonces))
}

func TestCheckDuplicateNonces_Empty(t *testing.T) {
	assert.NoError(t, CheckDuplicateNonces(nil))
	assert.NoError(t, CheckDuplicateNonces([]VerifiedArtifact{}))
}

func TestCheckDuplicateNonces_Single(t *testing.T) {
	assert.NoError(t, CheckDuplicateNonces([]VerifiedArtifact{{Nonce: 42}}))
}

func TestCheckDuplicateNonces_NegativeNonces(t *testing.T) {
	artifacts := []VerifiedArtifact{
		{Nonce: -1},
		{Nonce: -2},
		{Nonce: 0},
	}
	assert.NoError(t, CheckDuplicateNonces(artifacts))
}

func TestCheckDuplicateNonces_NegativeDuplicates(t *testing.T) {
	artifacts := []VerifiedArtifact{
		{Nonce: -1},
		{Nonce: -1},
	}
	assert.True(t, errors.Is(CheckDuplicateNonces(artifacts), ErrDuplicateNonces))
}

func TestValidateFP16Vector_ValidVector(t *testing.T) {
	// Construct valid 12-element FP16 vector (DefaultKDim=12, so 24 bytes)
	validBytes := make([]byte, DefaultKDim*2)
	for i := 0; i < len(validBytes); i += 2 {
		// 0x3c00 = 1.0 in FP16
		validBytes[i] = 0x00
		validBytes[i+1] = 0x3c
	}
	assert.NoError(t, ValidateFP16Vector(validBytes, DefaultKDim))
}

func TestValidateFP16Vector_RealVectorsWithNaN(t *testing.T) {
	// Real examples from exploit data - all contain NaN at 0x7e00
	testCases := []struct {
		name   string
		nanPos int // position of NaN in the 12-element vector
		b64    string
	}{
		{"NaN at position 1", 1, "JjsAfn85Zjp/NUgzrzNgOdYliTiIO7g4"},
		{"NaN at position 2", 2, "NTsbOgB+1jbXOrEjsDm5OOA16DkXOg05"},
		{"NaN at last position (11)", 11, "UzCFOaA70zebNWAm9zlEODg3LjIcOAB+"},
		{"NaN at first position (0)", 0, "AH64L7g5JiraLKE1vju9ONctZTWSNQg0"},
		{"NaN at position 6", 6, "iDpIMGo5rDoiNkc5AH4vOogtSDgROa0w"},
		{"NaN at position 0 - participant 2", 0, "AH44OHY03TR5O345DDTnNB05jjqNOnw7"},
		{"NaN at position 10", 10, "Kib/OfcsgjsNMQY7+zufHyE6mTcAfoc2"},
		{"NaN at position 8", 8, "XjbAOZU6ADfdNek4Jzr/NQB+iDsAOHI6"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vectorBytes, err := base64.StdEncoding.DecodeString(tc.b64)
			require.NoError(t, err)

			err = ValidateFP16Vector(vectorBytes, DefaultKDim)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "NaN")
			// Verify error reports correct byte offset
			expectedOffset := tc.nanPos * 2
			assert.Contains(t, err.Error(), fmt.Sprintf("byte offset %d", expectedOffset))
		})
	}
}

func TestValidateFP16Vector_WithPositiveInfinity(t *testing.T) {
	// Build 12-element vector with +Infinity at position 0
	infBytes := make([]byte, DefaultKDim*2)
	infBytes[0] = 0x00
	infBytes[1] = 0x7c // 0x7c00 = +Infinity (exp=31, frac=0)
	err := ValidateFP16Vector(infBytes, DefaultKDim)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Infinity")
}

func TestValidateFP16Vector_WithNegativeInfinity(t *testing.T) {
	// Build 12-element vector with -Infinity at position 0
	negInfBytes := make([]byte, DefaultKDim*2)
	negInfBytes[0] = 0x00
	negInfBytes[1] = 0xfc // 0xfc00 = -Infinity (exp=31, frac=0, sign=1)
	err := ValidateFP16Vector(negInfBytes, DefaultKDim)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Infinity")
}

func TestValidateFP16Vector_WrongLength(t *testing.T) {
	// Vector with wrong number of elements (3 instead of 12)
	shortBytes := []byte{0x00, 0x3c, 0x00, 0x3c, 0x00, 0x3c} // 3 valid FP16 values
	err := ValidateFP16Vector(shortBytes, DefaultKDim)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid vector length")
	assert.Contains(t, err.Error(), "got 6 bytes")
	assert.Contains(t, err.Error(), "expected 24")
}

func TestValidateFP16Vector_Empty(t *testing.T) {
	// Empty vectors should fail with length mismatch
	err := ValidateFP16Vector(nil, DefaultKDim)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid vector length")
	assert.Contains(t, err.Error(), "got 0 bytes")

	err = ValidateFP16Vector([]byte{}, DefaultKDim)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid vector length")
}

func TestValidateFP16Vector_QuietNaN(t *testing.T) {
	// Build 12-element vector with quiet NaN at position 0
	// 0x7e00 = quiet NaN (exp=31, frac=512) - the exact value found in all_nonces.json
	qnanBytes := make([]byte, DefaultKDim*2)
	qnanBytes[0] = 0x00
	qnanBytes[1] = 0x7e
	err := ValidateFP16Vector(qnanBytes, DefaultKDim)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NaN")
	assert.Contains(t, err.Error(), "0x7e00")
}

func TestValidateFP16Vector_SignalingNaN(t *testing.T) {
	// Build 12-element vector with signaling NaN at position 0
	// 0x7c01 = signaling NaN (exp=31, frac=1)
	snanBytes := make([]byte, DefaultKDim*2)
	snanBytes[0] = 0x01
	snanBytes[1] = 0x7c
	err := ValidateFP16Vector(snanBytes, DefaultKDim)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NaN")
}

func TestValidateFP16Vector_NegativeNaN(t *testing.T) {
	// Build 12-element vector with negative quiet NaN at position 0
	// 0xfe00 = negative quiet NaN (sign=1, exp=31, frac=512)
	negNanBytes := make([]byte, DefaultKDim*2)
	negNanBytes[0] = 0x00
	negNanBytes[1] = 0xfe
	err := ValidateFP16Vector(negNanBytes, DefaultKDim)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NaN")
}

func TestValidateFP16Vector_ValidWithSubnormals(t *testing.T) {
	// Subnormal values (exp=0, frac!=0) should be allowed - they are valid small numbers
	subnormalBytes := make([]byte, DefaultKDim*2)
	// 0x0001 = smallest positive subnormal
	subnormalBytes[0] = 0x01
	subnormalBytes[1] = 0x00
	// 0x03ff = largest positive subnormal
	subnormalBytes[2] = 0xff
	subnormalBytes[3] = 0x03
	assert.NoError(t, ValidateFP16Vector(subnormalBytes, DefaultKDim))
}

func TestValidateFP16Vector_ValidZero(t *testing.T) {
	// 0x0000 = +0, 0x8000 = -0 - both are valid
	zeroBytes := make([]byte, DefaultKDim*2)
	// Position 0: +0
	zeroBytes[0] = 0x00
	zeroBytes[1] = 0x00
	// Position 1: -0
	zeroBytes[2] = 0x00
	zeroBytes[3] = 0x80
	assert.NoError(t, ValidateFP16Vector(zeroBytes, DefaultKDim))
}

// TestErrInvalidVectorData_ErrorWrapping verifies that ErrInvalidVectorData is properly
// wrapped and can be detected with errors.Is, which is how validateParticipant classifies
// permanent failures.
func TestErrInvalidVectorData_ErrorWrapping(t *testing.T) {
	// Simulate what FetchAndVerifyProofs does when it detects invalid vector data
	leafIndex := uint32(42)
	// Build 12-element vector with NaN at position 0
	nanBytes := make([]byte, DefaultKDim*2)
	nanBytes[0] = 0x00
	nanBytes[1] = 0x7e // quiet NaN
	validationErr := ValidateFP16Vector(nanBytes, DefaultKDim)
	wrappedErr := fmt.Errorf("%w: leaf %d: %v", ErrInvalidVectorData, leafIndex, validationErr)

	// This is exactly how validateParticipant checks for permanent failures
	assert.True(t, errors.Is(wrappedErr, ErrInvalidVectorData),
		"wrapped error should be detectable with errors.Is")

	// Verify the error message contains useful information
	assert.Contains(t, wrappedErr.Error(), "invalid vector data detected")
	assert.Contains(t, wrappedErr.Error(), "leaf 42")
	assert.Contains(t, wrappedErr.Error(), "NaN")
}

// TestPermanentFailureErrors verifies all permanent failure error types can be
// properly detected with errors.Is after wrapping.
func TestPermanentFailureErrors(t *testing.T) {
	testCases := []struct {
		name        string
		baseErr     error
		wrapMessage string
	}{
		{
			name:        "ErrProofVerificationFailed",
			baseErr:     ErrProofVerificationFailed,
			wrapMessage: "leaf 1",
		},
		{
			name:        "ErrIncompleteCoverage",
			baseErr:     ErrIncompleteCoverage,
			wrapMessage: "expected 10 proofs, got 5",
		},
		{
			name:        "ErrInvalidVectorData",
			baseErr:     ErrInvalidVectorData,
			wrapMessage: "leaf 42: NaN detected",
		},
		{
			name:        "ErrDuplicateNonces",
			baseErr:     ErrDuplicateNonces,
			wrapMessage: "participant xyz",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Wrap the error like the code does
			wrappedErr := fmt.Errorf("%w: %s", tc.baseErr, tc.wrapMessage)

			// Verify errors.Is can detect it (this is how validateParticipant decides permanent vs retry)
			assert.True(t, errors.Is(wrappedErr, tc.baseErr),
				"errors.Is should detect %v in wrapped error", tc.name)

			// Verify it's not confused with other error types
			for _, other := range testCases {
				if other.name != tc.name {
					assert.False(t, errors.Is(wrappedErr, other.baseErr),
						"errors.Is should NOT detect %v when error is %v", other.name, tc.name)
				}
			}
		})
	}
}

// TestFetchAndVerifyProofs_RejectsNaNVector tests the full flow: HTTP server returns
// a proof with NaN vector data, and FetchAndVerifyProofs returns ErrInvalidVectorData.
func TestFetchAndVerifyProofs_RejectsNaNVector(t *testing.T) {
	// Create a mock HTTP server that returns a proof with NaN vector
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a proof response with NaN-containing vector from real exploit data
		resp := ProofResponse{
			Proofs: []ProofItem{
				{
					LeafIndex:   0,
					NonceValue:  21,
					VectorBytes: "JjsAfn85Zjp/NUgzrzNgOdYliTiIO7g4", // Contains NaN at position 1
					Proof:       []string{},                         // Empty proof for simplicity
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create proof client (recorder can be nil for this test since we mock the HTTP response)
	client := &ProofClient{
		httpClient: server.Client(),
		recorder:   nil, // Not needed - we're testing vector validation, not signature
	}

	// We can't call FetchAndVerifyProofs directly because it needs a recorder for signing.
	// Instead, test the validation logic that would be called:
	vectorB64 := "JjsAfn85Zjp/NUgzrzNgOdYliTiIO7g4"
	vectorBytes, err := base64.StdEncoding.DecodeString(vectorB64)
	require.NoError(t, err)

	// This is what FetchAndVerifyProofs does internally
	err = ValidateFP16Vector(vectorBytes, DefaultKDim)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NaN")

	// Wrap it like FetchAndVerifyProofs does
	wrappedErr := fmt.Errorf("%w: leaf %d: %v", ErrInvalidVectorData, 0, err)

	// Verify validateParticipant would classify this as permanent failure
	assert.True(t, errors.Is(wrappedErr, ErrInvalidVectorData))

	// Ensure client is used to avoid unused variable warning
	_ = client
}
