package keeper

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/productscience/inference/x/bls/types"
	blst "github.com/supranational/blst/bindings/go"
)

func verifyDealerConstantTermPoK(epochID uint64, dealer string, commitments [][]byte, proof []byte) error {
	if len(commitments) == 0 {
		return fmt.Errorf("commitments must be non-empty")
	}
	if len(proof) != types.DealerConstantTermPoKLen {
		return fmt.Errorf("proof must be exactly %d bytes, got %d", types.DealerConstantTermPoKLen, len(proof))
	}

	var c, z fr.Element
	if err := c.SetBytesCanonical(proof[:fr.Bytes]); err != nil {
		return fmt.Errorf("challenge is not a canonical scalar: %w", err)
	}
	if err := z.SetBytesCanonical(proof[fr.Bytes:]); err != nil {
		return fmt.Errorf("response is not a canonical scalar: %w", err)
	}

	constantTerm := new(blst.P2Affine).Uncompress(commitments[0])
	if constantTerm == nil {
		return fmt.Errorf("failed to decompress commitments[0]")
	}
	if !constantTerm.KeyValidate() {
		return fmt.Errorf("commitments[0] is the identity or not in the G2 subgroup")
	}

	var negC fr.Element
	negC.Neg(&c)
	scalars := make([]byte, 0, 2*fr.Bytes)
	scalars = append(scalars, frToBlstScalar(&z)...)
	scalars = append(scalars, frToBlstScalar(&negC)...)
	nonceCommitment := blst.P2AffinesMult([]*blst.P2Affine{blst.P2Generator().ToAffine(), constantTerm}, scalars, 255).ToAffine()
	if nonceCommitment.Equals(new(blst.P2).ToAffine()) {
		return fmt.Errorf("nonce commitment is the identity")
	}

	expected, err := types.DealerConstantTermPoKChallenge(epochID, dealer, commitments, nonceCommitment.Compress())
	if err != nil {
		return err
	}
	if !expected.Equal(&c) {
		return fmt.Errorf("challenge mismatch")
	}
	return nil
}

func frToBlstScalar(e *fr.Element) []byte {
	b := e.Bytes()
	for i := 0; i < fr.Bytes/2; i++ {
		b[i], b[fr.Bytes-1-i] = b[fr.Bytes-1-i], b[i]
	}
	return b[:]
}
