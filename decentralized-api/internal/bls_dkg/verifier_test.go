package bls_dkg

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/assert"
)

func TestNewVerifier(t *testing.T) {
	// Test with nil client for basic construction
	verifier := NewVerifier(nil)

	assert.NotNil(t, verifier)
	assert.NotNil(t, verifier.slotShares)
}

func TestGetSlotShares(t *testing.T) {
	verifier := NewVerifier(nil)

	// Add some test slot shares
	share1 := &fr.Element{}
	share1.SetUint64(123)
	share2 := &fr.Element{}
	share2.SetUint64(456)

	verifier.slotShares[0] = share1
	verifier.slotShares[1] = share2

	// Get copies of slot shares
	shares := verifier.GetSlotShares()

	assert.Len(t, shares, 2)
	assert.Equal(t, share1.String(), shares[0].String())
	assert.Equal(t, share2.String(), shares[1].String())

	// Verify they are copies, not references
	shares[0].SetUint64(999)
	assert.NotEqual(t, shares[0].String(), verifier.slotShares[0].String())
}

func TestCountTrueValues(t *testing.T) {
	tests := []struct {
		name     string
		input    []bool
		expected int
	}{
		{"empty slice", []bool{}, 0},
		{"all false", []bool{false, false, false}, 0},
		{"all true", []bool{true, true, true}, 3},
		{"mixed", []bool{true, false, true, false, true}, 3},
		{"single true", []bool{true}, 1},
		{"single false", []bool{false}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := countTrueValues(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
