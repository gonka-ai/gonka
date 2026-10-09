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

	c, err := DealerConstantTermPoKChallenge(416, "gonka1dealerfixture", commitments, blst.P2Generator().ToAffine().Compress())
	require.NoError(t, err)
	got := c.Bytes()
	require.Equal(t, "54f11f55fa124ec20a553e1eb1a0aea4d94a1488df88335bed52997d9f126dc3", hex.EncodeToString(got[:]))
}
