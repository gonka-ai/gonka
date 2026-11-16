package bls

import (
	"encoding/hex"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/require"
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
		{"Off-curve x=5", append(append([]byte{0x02}, make([]byte, 31)...), 0x05)},
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

// TestPolynomialEvaluationEdgeCases verifies explicit behavior for small polynomial lengths.
func TestPolynomialEvaluationEdgeCases(t *testing.T) {
	t.Run("empty polynomial returns zero", func(t *testing.T) {
		result := evaluatePolynomial(nil, 7)
		if result == nil {
			t.Fatal("Polynomial evaluation returned nil for empty polynomial")
		}

		expected := new(fr.Element).SetZero()
		if !result.Equal(expected) {
			t.Fatalf("Expected zero element for empty polynomial")
		}
	})

	t.Run("constant polynomial returns constant", func(t *testing.T) {
		constant := new(fr.Element).SetUint64(17)
		polynomial := []*fr.Element{constant}
		result := evaluatePolynomial(polynomial, 99)
		if result == nil {
			t.Fatal("Polynomial evaluation returned nil for constant polynomial")
		}

		if !result.Equal(constant) {
			t.Fatalf("Expected constant polynomial evaluation to equal coefficient")
		}
	})

	t.Run("degree one polynomial evaluates correctly", func(t *testing.T) {
		// P(x) = a0 + a1*x where a0=3, a1=5, x=4 => 23.
		a0 := new(fr.Element).SetUint64(3)
		a1 := new(fr.Element).SetUint64(5)
		polynomial := []*fr.Element{a0, a1}

		result := evaluatePolynomial(polynomial, 4)
		if result == nil {
			t.Fatal("Polynomial evaluation returned nil for degree-one polynomial")
		}

		expected := new(fr.Element).SetUint64(23)
		if !result.Equal(expected) {
			t.Fatalf("Expected 23, got different field element")
		}
	})
}

func TestGenerateDealerPart_ValidatesSnapshotKeysUpfront(t *testing.T) {
	valid02, err := hex.DecodeString("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	require.NoError(t, err)
	valid03, err := hex.DecodeString("031884e5018572688f308999f53092837489aeac31afe1389809281562794c171b")
	require.NoError(t, err)

	// Report off-curve vector: 02 || 31*00 || 05
	offCurve := make([]byte, 33)
	offCurve[0] = 0x02
	offCurve[32] = 0x05

	t.Run("valid_primary_and_warm_keys", func(t *testing.T) {
		bm := NewBlsManager(createMockCosmosClient())
		participants := []ParticipantInfo{
			{
				Address:                    "gonka1alice",
				Secp256K1PublicKey:         valid02,
				AllowedSecp256K1PublicKeys: [][]byte{valid03},
				SlotStartIndex:             0,
				SlotEndIndex:               1,
			},
		}

		dealerPart, err := bm.generateDealerPart(7, 2, 1, participants)
		require.NoError(t, err)
		require.NotNil(t, dealerPart)
		require.Len(t, dealerPart.Commitments, 2)
		require.Len(t, dealerPart.EncryptedSharesForParticipants, 1)
		// 2 slots * (1 primary + 1 warm key) = 4 ciphertexts
		require.Len(t, dealerPart.EncryptedSharesForParticipants[0].EncryptedShares, 4)

		// Verify dealer opening records were stored for the valid epoch
		_, ok := bm.getDealerOpeningRecord(7, 0, 0)
		require.True(t, ok)
	})

	t.Run("rejects_off_curve_primary_key_before_work", func(t *testing.T) {
		bm := NewBlsManager(createMockCosmosClient())
		participants := []ParticipantInfo{
			{
				Address:            "gonka1alice",
				Secp256K1PublicKey: valid02,
				SlotStartIndex:     0,
				SlotEndIndex:       0,
			},
			{
				Address:            "gonka1offcurve",
				Secp256K1PublicKey: offCurve,
				SlotStartIndex:     1,
				SlotEndIndex:       1,
			},
		}

		dealerPart, err := bm.generateDealerPart(8, 2, 1, participants)
		require.Nil(t, dealerPart)
		require.Error(t, err)
		require.ErrorContains(t, err, "epoch 8")
		require.ErrorContains(t, err, "gonka1offcurve")
		require.ErrorContains(t, err, "not a valid curve point")
		require.NotContains(t, err.Error(), "0000000000000000000000000000000000000000000000000000000000000005")

		// No opening records should be stored for the first participant either
		_, ok := bm.getDealerOpeningRecord(8, 0, 0)
		require.False(t, ok)
	})

	t.Run("rejects_off_curve_additional_warm_key_before_work", func(t *testing.T) {
		bm := NewBlsManager(createMockCosmosClient())
		participants := []ParticipantInfo{
			{
				Address:                    "gonka1alice",
				Secp256K1PublicKey:         valid02,
				AllowedSecp256K1PublicKeys: [][]byte{valid03, offCurve},
				SlotStartIndex:             0,
				SlotEndIndex:               1,
			},
		}

		dealerPart, err := bm.generateDealerPart(9, 2, 1, participants)
		require.Nil(t, dealerPart)
		require.Error(t, err)
		require.ErrorContains(t, err, "epoch 9")
		require.ErrorContains(t, err, "gonka1alice")
		require.ErrorContains(t, err, "index 1")
		require.ErrorContains(t, err, "not a valid curve point")
		require.NotContains(t, err.Error(), "0000000000000000000000000000000000000000000000000000000000000005")

		_, ok := bm.getDealerOpeningRecord(9, 0, 0)
		require.False(t, ok)
	})
}

