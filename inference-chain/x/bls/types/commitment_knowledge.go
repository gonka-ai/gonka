package types

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	blst "github.com/supranational/blst/bindings/go"
)

const commitmentKnowledgeDomain = "gonka-dkg-commitment-pok-v1"

// BuildCommitmentKnowledgeProof returns the compressed G2 commitment a·G and a
// Schnorr proof that the caller knows a. The nonce is a fresh nonzero scalar.
func BuildCommitmentKnowledgeProof(epochID uint64, creator string, index uint32, coefficient *fr.Element) ([]byte, CommitmentKnowledgeProof, error) {
	if coefficient == nil {
		return nil, CommitmentKnowledgeProof{}, fmt.Errorf("coefficient is nil")
	}

	nonce := new(fr.Element)
	for {
		if _, err := nonce.SetRandom(); err != nil {
			return nil, CommitmentKnowledgeProof{}, fmt.Errorf("failed to sample nonce: %w", err)
		}
		if !nonce.IsZero() {
			break
		}
	}

	commitment := scalarMultG2(coefficient)
	noncePoint := scalarMultG2(nonce)
	challenge := commitmentKnowledgeChallenge(epochID, creator, index, commitment, noncePoint)

	response := new(fr.Element).Mul(challenge, coefficient)
	response.Add(response, nonce)

	return commitment, CommitmentKnowledgeProof{
		NoncePoint: noncePoint,
		Response:   response.Marshal(),
	}, nil
}

// VerifyCommitmentKnowledgeProofs checks one proof per commitment. A proof
// verifies only when z·G = R + c·C for the scalar the dealer claims to know.
func VerifyCommitmentKnowledgeProofs(epochID uint64, creator string, commitments [][]byte, proofs []CommitmentKnowledgeProof) error {
	if len(proofs) != len(commitments) {
		return fmt.Errorf("commitment_proofs count %d does not match commitments count %d", len(proofs), len(commitments))
	}
	for i := range commitments {
		if err := verifyCommitmentKnowledgeProof(epochID, creator, uint32(i), commitments[i], proofs[i]); err != nil {
			return fmt.Errorf("commitment_proofs[%d]: %w", i, err)
		}
	}
	return nil
}

func verifyCommitmentKnowledgeProof(epochID uint64, creator string, index uint32, commitment []byte, proof CommitmentKnowledgeProof) error {
	if len(commitment) != commitmentCompressedG2Len {
		return fmt.Errorf("commitment must be %d bytes", commitmentCompressedG2Len)
	}
	if len(proof.NoncePoint) != commitmentCompressedG2Len {
		return fmt.Errorf("nonce_point must be %d bytes", commitmentCompressedG2Len)
	}
	if len(proof.Response) != commitmentKnowledgeResponseLen {
		return fmt.Errorf("response must be %d bytes", commitmentKnowledgeResponseLen)
	}

	commitmentPoint, err := uncompressG2(commitment)
	if err != nil {
		return fmt.Errorf("commitment: %w", err)
	}
	noncePoint, err := uncompressG2(proof.NoncePoint)
	if err != nil {
		return fmt.Errorf("nonce_point: %w", err)
	}
	// uncompressG2 already checked subgroup membership; KeyValidate adds the
	// non-identity check.
	if !noncePoint.KeyValidate() {
		return fmt.Errorf("nonce_point must not be the identity")
	}

	response, err := canonicalScalar(proof.Response)
	if err != nil {
		return err
	}

	challenge := commitmentKnowledgeChallenge(epochID, creator, index, commitment, proof.NoncePoint)
	if !commitmentProofHolds(response, noncePoint, challenge, commitmentPoint) {
		return fmt.Errorf("verification failed")
	}
	return nil
}

func commitmentProofHolds(response *fr.Element, noncePoint *blst.P2Affine, challenge *fr.Element, commitment *blst.P2Affine) bool {
	left := blst.P2Generator().Mult(scalarToBlst(response), 255).ToAffine()

	commitmentTerm := new(blst.P2)
	commitmentTerm.FromAffine(commitment)
	commitmentTerm.MultAssign(scalarToBlst(challenge), 255)

	right := new(blst.P2)
	right.FromAffine(noncePoint)
	right.AddAssign(commitmentTerm)
	return left.Equals(right.ToAffine())
}

// commitmentKnowledgeChallenge hashes the statement to a scalar. Two
// domain-separated SHA-256 outputs (512 bits) are reduced mod r so the result
// is statistically close to uniform; one 256-bit output would be biased.
func commitmentKnowledgeChallenge(epochID uint64, creator string, index uint32, commitment, noncePoint []byte) *fr.Element {
	var input []byte
	input = append(input, commitmentKnowledgeDomain...)
	input = binary.BigEndian.AppendUint64(input, epochID)
	input = binary.BigEndian.AppendUint32(input, uint32(len(creator)))
	input = append(input, creator...)
	input = binary.BigEndian.AppendUint32(input, index)
	input = append(input, commitment...)
	input = append(input, noncePoint...)

	wide := make([]byte, 0, 2*sha256.Size)
	for _, label := range []byte{0, 1} {
		sum := sha256.Sum256(append([]byte{label}, input...))
		wide = append(wide, sum[:]...)
	}

	challenge := new(fr.Element)
	challenge.SetBytes(wide) // big-endian, reduced mod r
	return challenge
}

func scalarMultG2(scalar *fr.Element) []byte {
	return blst.P2Generator().Mult(scalarToBlst(scalar), 255).ToAffine().Compress()
}

func scalarToBlst(scalar *fr.Element) []byte {
	encoded := scalar.Bytes()
	out := make([]byte, len(encoded))
	for i := range encoded {
		out[i] = encoded[len(encoded)-1-i]
	}
	return out
}

func canonicalScalar(encoded []byte) (*fr.Element, error) {
	if len(encoded) != fr.Bytes {
		return nil, fmt.Errorf("response must be %d bytes", fr.Bytes)
	}
	var buf [fr.Bytes]byte
	copy(buf[:], encoded)
	scalar, err := fr.BigEndian.Element(&buf)
	if err != nil {
		return nil, fmt.Errorf("response is not a canonical scalar")
	}
	return &scalar, nil
}

func uncompressG2(encoded []byte) (*blst.P2Affine, error) {
	point := new(blst.P2Affine).Uncompress(encoded)
	if point == nil {
		return nil, fmt.Errorf("failed to uncompress G2 point")
	}
	if !point.InG2() {
		return nil, fmt.Errorf("point is not in G2")
	}
	return point, nil
}
