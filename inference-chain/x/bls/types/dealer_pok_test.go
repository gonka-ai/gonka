package types

import (
	"encoding/hex"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"
)

func TestDealerConstantTermPoKChallenge_KnownAnswer(t *testing.T) {
	commitments := make([][]byte, 3)
	for i, coefficient := range []uint64{11, 22, 33} {
		var e fr.Element
		e.SetUint64(coefficient)
		b := e.Bytes()
		for j := 0; j < fr.Bytes/2; j++ {
			b[j], b[fr.Bytes-1-j] = b[fr.Bytes-1-j], b[j]
		}
		commitments[i] = blst.P2Generator().Mult(b[:], 255).ToAffine().Compress()
	}

	got := DealerConstantTermPoKChallenge(416, "gonka1dealerfixture", commitments, blst.P2Generator().ToAffine().Compress())
	require.Equal(t, "54f11f55fa124ec20a553e1eb1a0aea4d94a1488df88335bed52997d9f126dc3", hex.EncodeToString(got[:]))
}

func TestDealerConstantTermPoKChallenge_MatchesGnarkHashToField(t *testing.T) {
	require.Equal(t, 0, blsScalarFieldModulus.Cmp(fr.Modulus()))
	for i := 0; i < 64; i++ {
		commitments := [][]byte{make([]byte, 96), []byte{byte(i)}}
		commitments[0][0] = byte(i)
		dealer := "gonka1dealer" + string(rune('a'+i%26))
		nonce := []byte{byte(i), byte(i * 7)}

		got := DealerConstantTermPoKChallenge(uint64(i), dealer, commitments, nonce)

		msg := make([]byte, 0)
		msg = append(msg, make([]byte, 8)...)
		msg[7] = byte(i)
		msg = appendLengthPrefixed(msg, []byte(dealer))
		msg = append(msg, 0, 0, 0, 2)
		msg = appendLengthPrefixed(msg, commitments[0])
		msg = appendLengthPrefixed(msg, commitments[1])
		msg = appendLengthPrefixed(msg, nonce)
		want, err := fr.Hash(msg, []byte(DealerConstantTermPoKDomain), 1)
		require.NoError(t, err)
		wantBytes := want[0].Bytes()
		require.Equal(t, wantBytes[:], got[:])
	}
}
