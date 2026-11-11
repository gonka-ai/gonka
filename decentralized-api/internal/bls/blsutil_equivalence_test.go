package bls

import (
	"bytes"
	"testing"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"

	"github.com/productscience/inference/x/bls/blsutil"
)

// referenceHashToG1 is the dAPI's previous inline implementation, kept here
// to pin that delegating to blsutil changed no bytes.
func referenceHashToG1(hash []byte) bls12381.G1Affine {
	var be [48]byte
	copy(be[48-32:], hash)
	var u fp.Element
	u.SetBytes(be[:])
	p := bls12381.MapToCurve1(&u)
	hash_to_curve.G1Isogeny(&p.X, &p.Y)
	var out bls12381.G1Affine
	out.ClearCofactor(&p)
	return out
}

// referencePartials is the dAPI's previous computePartialSignatureBlst body.
func referencePartials(t *testing.T, msg []byte, r *VerificationResult) []byte {
	t.Helper()
	g := referenceHashToG1(msg)
	gb := g.Bytes()
	m := new(blst.P1Affine).Uncompress(gb[:])
	require.NotNil(t, m)
	var out []byte
	for _, sk := range r.AggregatedShares {
		b := sk.Bytes()
		for i := 0; i < 16; i++ {
			b[i], b[31-i] = b[31-i], b[i]
		}
		sig := new(blst.P1)
		sig.FromAffine(m)
		sig.MultAssign(b[:], 255)
		out = append(out, sig.ToAffine().Compress()...)
	}
	return out
}

// Threshold signing and the block beacon share one primitive (x/bls/blsutil);
// the dAPI's partial signatures are byte-identical to what it produced before
// and to what a chain node produces for the same shares.
func TestSigningMatchesSharedPrimitive(t *testing.T) {
	fx := newExportFixture(t, 8, 2)
	r := fx.result(3, 2, 6)
	bm := NewBlsManager(createMockCosmosClient())
	for _, seed := range []byte{0, 1, 0xab} {
		msg := bytes.Repeat([]byte{seed}, 32)
		msg[0] ^= 0x5a

		g, err := bm.hashToG1(msg)
		require.NoError(t, err)
		require.Equal(t, referenceHashToG1(msg), g)

		got, err := bm.computePartialSignatureBlst(msg, r)
		require.NoError(t, err)
		require.Equal(t, referencePartials(t, msg, r), got)

		for i := range r.AggregatedShares {
			sig := got[i*blsutil.SignatureLen : (i+1)*blsutil.SignatureLen]
			require.True(t, blsutil.VerifyPartial(msg, sig, fx.slotPKs[2+i]), "slot %d", 2+i)
		}
	}
}
