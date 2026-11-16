package types

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateCompressedSecp256k1PubKey(t *testing.T) {
	// Fixed valid compressed keys with 0x02 and 0x03 prefixes
	valid02, err := hex.DecodeString("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	require.NoError(t, err)
	valid03, err := hex.DecodeString("031884e5018572688f308999f53092837489aeac31afe1389809281562794c171b")
	require.NoError(t, err)

	// Report vector: 02 || 31*00 || 05 (x = 5 is not a quadratic residue for x^3 + 7 mod p)
	offCurveX5 := make([]byte, 33)
	offCurveX5[0] = 0x02
	offCurveX5[32] = 0x05

	offCurveX5Odd := make([]byte, 33)
	offCurveX5Odd[0] = 0x03
	offCurveX5Odd[32] = 0x05

	// x = 0 (y^2 = 7 has no solution mod p)
	offCurveZeroX := make([]byte, 33)
	offCurveZeroX[0] = 0x02

	// x >= field prime p (all 0xFF)
	offCurveAllFF := make([]byte, 33)
	offCurveAllFF[0] = 0x02
	for i := 1; i < len(offCurveAllFF); i++ {
		offCurveAllFF[i] = 0xff
	}

	t.Run("valid_0x02_prefix", func(t *testing.T) {
		parsed, err := ParseCompressedSecp256k1PubKey(valid02)
		require.NoError(t, err)
		require.NotNil(t, parsed)
		require.Equal(t, valid02, parsed.SerializeCompressed())
		require.NoError(t, ValidateCompressedSecp256k1PubKey(valid02))
	})

	t.Run("valid_0x03_prefix", func(t *testing.T) {
		parsed, err := ParseCompressedSecp256k1PubKey(valid03)
		require.NoError(t, err)
		require.NotNil(t, parsed)
		require.Equal(t, valid03, parsed.SerializeCompressed())
		require.NoError(t, ValidateCompressedSecp256k1PubKey(valid03))
	})

	t.Run("empty_input", func(t *testing.T) {
		err := ValidateCompressedSecp256k1PubKey(nil)
		require.ErrorIs(t, err, ErrInvalidSecp256k1PubKeyLength)

		err = ValidateCompressedSecp256k1PubKey([]byte{})
		require.ErrorIs(t, err, ErrInvalidSecp256k1PubKeyLength)
	})

	t.Run("32_byte_input", func(t *testing.T) {
		err := ValidateCompressedSecp256k1PubKey(valid02[:32])
		require.ErrorIs(t, err, ErrInvalidSecp256k1PubKeyLength)
	})

	t.Run("34_byte_input", func(t *testing.T) {
		err := ValidateCompressedSecp256k1PubKey(append(append([]byte(nil), valid02...), 0x00))
		require.ErrorIs(t, err, ErrInvalidSecp256k1PubKeyLength)
	})

	t.Run("invalid_prefixes", func(t *testing.T) {
		for _, prefix := range []byte{0x00, 0x01, 0x04, 0x05, 0xff} {
			bad := append([]byte(nil), valid02...)
			bad[0] = prefix
			err := ValidateCompressedSecp256k1PubKey(bad)
			require.ErrorIs(t, err, ErrInvalidSecp256k1PubKeyPrefix)
		}
	})

	t.Run("off_curve_report_vector_x5", func(t *testing.T) {
		for _, vec := range [][]byte{offCurveX5, offCurveX5Odd, offCurveZeroX, offCurveAllFF} {
			parsed, err := ParseCompressedSecp256k1PubKey(vec)
			require.Nil(t, parsed)
			require.ErrorIs(t, err, ErrInvalidSecp256k1CurvePoint)
			// Ensure error message does not dump the full 32-byte coordinate
			require.NotContains(t, err.Error(), "0000000000000000000000000000000000000000000000000000000000000005")
		}
	})
}
