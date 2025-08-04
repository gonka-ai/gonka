package bls

import (
	"encoding/hex"
	"testing"
)

// TestBLSCryptographicFunctions tests the basic functionality of our BLS functions
func TestBLSCryptographicFunctions(t *testing.T) {
	// Test polynomial generation
	degree := uint32(3)
	polynomial, err := generateRandomPolynomial(degree)
	if err != nil {
		t.Fatalf("Failed to generate polynomial: %v", err)
	}

	if len(polynomial) != int(degree+1) {
		t.Fatalf("Expected polynomial length %d, got %d", degree+1, len(polynomial))
	}

	// Test G2 commitments
	commitments := computeG2Commitments(polynomial)
	if len(commitments) != len(polynomial) {
		t.Fatalf("Expected %d commitments, got %d", len(polynomial), len(commitments))
	}

	// Test polynomial evaluation
	x := uint32(5)
	result := evaluatePolynomial(polynomial, x)
	if result == nil {
		t.Fatal("Polynomial evaluation returned nil")
	}

	// Test ECIES encryption with a valid compressed secp256k1 public key
	// This is a valid compressed secp256k1 public key for testing (33 bytes)
	testPubKey := make([]byte, 33)
	testPubKey[0] = 0x02 // Compressed key prefix (even Y coordinate)
	// Fill with some test data that forms a valid secp256k1 point
	// Using a known valid compressed public key for testing
	validCompressedKey := []byte{
		0x02, 0x79, 0xbe, 0x66, 0x7e, 0xf9, 0xdc, 0xbb, 0xac, 0x55, 0xa0, 0x62, 0x95, 0xce, 0x87, 0x0b,
		0x07, 0x02, 0x9b, 0xfc, 0xdb, 0x2d, 0xce, 0x28, 0xd9, 0x59, 0xf2, 0x81, 0x5b, 0x16, 0xf8, 0x17, 0x98,
	}
	copy(testPubKey, validCompressedKey)

	testData := []byte("test data for encryption")
	encrypted, err := encryptForParticipant(testData, testPubKey)
	if err != nil {
		t.Fatalf("ECIES encryption failed: %v", err)
	}

	if len(encrypted) == 0 {
		t.Fatal("ECIES encryption returned empty result")
	}

	t.Logf("Successfully tested all BLS cryptographic functions:")
	t.Logf("- Generated polynomial with %d coefficients", len(polynomial))
	t.Logf("- Generated %d G2 commitments", len(commitments))
	t.Logf("- Evaluated polynomial at x=%d", x)
	t.Logf("- Encrypted %d bytes to %d bytes", len(testData), len(encrypted))
}

// TestPolynomialGeneration tests polynomial generation with different degrees
func TestPolynomialGeneration(t *testing.T) {
	testCases := []struct {
		name   string
		degree uint32
	}{
		{"Small degree", 1},
		{"Medium degree", 10},
		{"Large degree", 100},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			polynomial, err := generateRandomPolynomial(tc.degree)
			if err != nil {
				t.Fatalf("Failed to generate polynomial: %v", err)
			}

			expectedLength := int(tc.degree + 1)
			if len(polynomial) != expectedLength {
				t.Fatalf("Expected polynomial length %d, got %d", expectedLength, len(polynomial))
			}

			// Verify all coefficients are non-nil
			for i, coeff := range polynomial {
				if coeff == nil {
					t.Fatalf("Coefficient at index %d is nil", i)
				}
			}
		})
	}
}

// TestCommitmentCalculation tests G2 commitment computation
func TestCommitmentCalculation(t *testing.T) {
	// Generate a test polynomial
	degree := uint32(5)
	polynomial, err := generateRandomPolynomial(degree)
	if err != nil {
		t.Fatalf("Failed to generate polynomial: %v", err)
	}

	// Compute commitments
	commitments := computeG2Commitments(polynomial)

	// Verify commitment count matches polynomial length
	if len(commitments) != len(polynomial) {
		t.Fatalf("Expected %d commitments, got %d", len(polynomial), len(commitments))
	}

	// Verify commitments are valid G2 points (basic check)
	for i, commitment := range commitments {
		if len(commitment) == 0 {
			t.Fatalf("Commitment at index %d is empty", i)
		}
		// G2 points in compressed form are 96 bytes (48 bytes each for x and y coordinates)
		if len(commitment) != 96 {
			t.Fatalf("Expected commitment length 96 bytes (compressed), got %d at index %d", len(commitment), i)
		}
	}
}

// TestShareEncryption tests ECIES encryption of shares
func TestShareEncryption(t *testing.T) {
	// Test data
	testShares := [][]byte{
		[]byte("share_data_1"),
		[]byte("share_data_2"),
		[]byte("longer_share_data_for_testing"),
	}

	// Valid compressed secp256k1 public key
	validPubKey := []byte{
		0x02, 0x79, 0xbe, 0x66, 0x7e, 0xf9, 0xdc, 0xbb, 0xac, 0x55, 0xa0, 0x62, 0x95, 0xce, 0x87, 0x0b,
		0x07, 0x02, 0x9b, 0xfc, 0xdb, 0x2d, 0xce, 0x28, 0xd9, 0x59, 0xf2, 0x81, 0x5b, 0x16, 0xf8, 0x17, 0x98,
	}

	for i, share := range testShares {
		t.Run(hex.EncodeToString(share)[:16], func(t *testing.T) {
			encrypted, err := encryptForParticipant(share, validPubKey)
			if err != nil {
				t.Fatalf("Failed to encrypt share %d: %v", i, err)
			}

			if len(encrypted) == 0 {
				t.Fatalf("Encrypted share %d is empty", i)
			}

			// Encrypted data should be longer than original due to ECIES overhead
			if len(encrypted) <= len(share) {
				t.Fatalf("Encrypted data should be longer than original. Original: %d, Encrypted: %d", len(share), len(encrypted))
			}
		})
	}
}

// TestInvalidPublicKeyEncryption tests encryption with invalid public keys
func TestInvalidPublicKeyEncryption(t *testing.T) {
	testData := []byte("test data")

	invalidKeys := []struct {
		name string
		key  []byte
	}{
		{"Empty key", []byte{}},
		{"Too short", []byte{0x02, 0x79}},
		{"Too long", make([]byte, 65)}, // Uncompressed format
		{"Invalid prefix", append([]byte{0x05}, make([]byte, 32)...)},
	}

	for _, tc := range invalidKeys {
		t.Run(tc.name, func(t *testing.T) {
			_, err := encryptForParticipant(testData, tc.key)
			if err == nil {
				t.Fatalf("Expected encryption to fail with invalid key: %s", tc.name)
			}
		})
	}
}

// TestPolynomialEvaluation tests polynomial evaluation at different points
func TestPolynomialEvaluation(t *testing.T) {
	// Generate a test polynomial
	degree := uint32(3)
	polynomial, err := generateRandomPolynomial(degree)
	if err != nil {
		t.Fatalf("Failed to generate polynomial: %v", err)
	}

	// Test evaluation at different points
	testPoints := []uint32{0, 1, 5, 10, 100}

	for _, x := range testPoints {
		t.Run(hex.EncodeToString([]byte{byte(x)}), func(t *testing.T) {
			result := evaluatePolynomial(polynomial, x)
			if result == nil {
				t.Fatalf("Polynomial evaluation returned nil for x=%d", x)
			}
		})
	}
}

// TestDeterministicPolynomialEvaluation tests that polynomial evaluation is deterministic
func TestDeterministicPolynomialEvaluation(t *testing.T) {
	// Generate a test polynomial
	degree := uint32(2)
	polynomial, err := generateRandomPolynomial(degree)
	if err != nil {
		t.Fatalf("Failed to generate polynomial: %v", err)
	}

	x := uint32(42)

	// Evaluate multiple times
	result1 := evaluatePolynomial(polynomial, x)
	result2 := evaluatePolynomial(polynomial, x)

	if result1 == nil || result2 == nil {
		t.Fatal("Polynomial evaluation returned nil")
	}

	// Results should be identical (deterministic)
	if !result1.Equal(result2) {
		t.Fatal("Polynomial evaluation is not deterministic")
	}
}
