package keeper

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

func TestValidateEncryptedSharesShape_RejectsUndersizedCiphertext(t *testing.T) {
	participant := types.BLSParticipantInfo{
		Address:          "p0",
		PercentageWeight: math.LegacyNewDec(100),
		SlotStartIndex:   0,
		SlotEndIndex:     0,
	}
	short := make([]byte, 98)
	short[0] = 0x04
	err := validateEncryptedSharesShape(participant, [][]byte{short})
	require.Error(t, err)
	require.Contains(t, err.Error(), "below ECIES minimum")
}

func TestValidateEncryptedSharesShape_AcceptsMinLength(t *testing.T) {
	participant := types.BLSParticipantInfo{
		Address:          "p0",
		PercentageWeight: math.LegacyNewDec(100),
		SlotStartIndex:   0,
		SlotEndIndex:     0,
	}
	ok := make([]byte, types.MinEncryptedShareCiphertextLen)
	ok[0] = 0x01
	require.NoError(t, validateEncryptedSharesShape(participant, [][]byte{ok}))
}
