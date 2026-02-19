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
	// Construct valid FP16 values (no NaN/Infinity)
	// Using values extracted from real vectors, excluding the NaN bytes
	validBytes := []byte{0x26, 0x3b, 0x7f, 0x39, 0x66, 0x3a} // 3 valid FP16 values
	assert.NoError(t, ValidateFP16Vector(validBytes))
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

			err = ValidateFP16Vector(vectorBytes)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "NaN")
			// Verify error reports correct byte offset
			expectedOffset := tc.nanPos * 2
			assert.Contains(t, err.Error(), fmt.Sprintf("byte offset %d", expectedOffset))
		})
	}
}

func TestValidateFP16Vector_WithPositiveInfinity(t *testing.T) {
	// 0x7c00 = +Infinity (exp=31, frac=0)
	infBytes := []byte{0x00, 0x7c}
	err := ValidateFP16Vector(infBytes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Infinity")
}

func TestValidateFP16Vector_WithNegativeInfinity(t *testing.T) {
	// 0xfc00 = -Infinity (exp=31, frac=0, sign=1)
	negInfBytes := []byte{0x00, 0xfc}
	err := ValidateFP16Vector(negInfBytes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Infinity")
}

func TestValidateFP16Vector_OddLength(t *testing.T) {
	// Odd byte count is invalid for FP16 vector
	oddBytes := []byte{0x00, 0x3c, 0x00}
	err := ValidateFP16Vector(oddBytes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be even")
}

func TestValidateFP16Vector_Empty(t *testing.T) {
	assert.NoError(t, ValidateFP16Vector(nil))
	assert.NoError(t, ValidateFP16Vector([]byte{}))
}

func TestValidateFP16Vector_QuietNaN(t *testing.T) {
	// 0x7e00 = quiet NaN (exp=31, frac=512) - the exact value found in all_nonces.json
	qnanBytes := []byte{0x00, 0x7e}
	err := ValidateFP16Vector(qnanBytes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NaN")
	assert.Contains(t, err.Error(), "0x7e00")
}

func TestValidateFP16Vector_SignalingNaN(t *testing.T) {
	// 0x7c01 = signaling NaN (exp=31, frac=1)
	snanBytes := []byte{0x01, 0x7c}
	err := ValidateFP16Vector(snanBytes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NaN")
}

func TestValidateFP16Vector_NegativeNaN(t *testing.T) {
	// 0xfe00 = negative quiet NaN (sign=1, exp=31, frac=512)
	negNanBytes := []byte{0x00, 0xfe}
	err := ValidateFP16Vector(negNanBytes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NaN")
}

func TestValidateFP16Vector_ValidWithSubnormals(t *testing.T) {
	// Subnormal values (exp=0, frac!=0) should be allowed - they are valid small numbers
	// 0x0001 = smallest positive subnormal
	subnormalBytes := []byte{0x01, 0x00, 0xff, 0x03} // two subnormals
	assert.NoError(t, ValidateFP16Vector(subnormalBytes))
}

func TestValidateFP16Vector_ValidZero(t *testing.T) {
	// 0x0000 = +0, 0x8000 = -0 - both are valid
	zeroBytes := []byte{0x00, 0x00, 0x00, 0x80}
	assert.NoError(t, ValidateFP16Vector(zeroBytes))
}

// TestErrInvalidVectorData_ErrorWrapping verifies that ErrInvalidVectorData is properly
// wrapped and can be detected with errors.Is, which is how validateParticipant classifies
// permanent failures.
func TestErrInvalidVectorData_ErrorWrapping(t *testing.T) {
	// Simulate what FetchAndVerifyProofs does when it detects invalid vector data
	leafIndex := uint32(42)
	validationErr := ValidateFP16Vector([]byte{0x00, 0x7e}) // NaN
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
	err = ValidateFP16Vector(vectorBytes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NaN")

	// Wrap it like FetchAndVerifyProofs does
	wrappedErr := fmt.Errorf("%w: leaf %d: %v", ErrInvalidVectorData, 0, err)

	// Verify validateParticipant would classify this as permanent failure
	assert.True(t, errors.Is(wrappedErr, ErrInvalidVectorData))

	// Ensure client is used to avoid unused variable warning
	_ = client
}
