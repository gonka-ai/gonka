package bls

import (
	"fmt"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	blst "github.com/supranational/blst/bindings/go"
)

// DecompressG1To128Blst converts a 48-byte compressed G1 point into a 128-byte uncompressed format
// using blst. Format: (X, Y) each as 64-byte big-endian limb.
func DecompressG1To128Blst(signature []byte) ([]byte, error) {
	if len(signature) != 48 {
		return nil, fmt.Errorf("invalid signature length: expected 48 bytes, got %d", len(signature))
	}
	p := new(blst.P1Affine).Uncompress(signature)
	if p == nil {
		return nil, fmt.Errorf("failed to uncompress signature with blst")
	}
	// Full signature validation for untrusted inputs (subgroup check + optional infinity rejection).
	if !p.SigValidate(true) {
		return nil, fmt.Errorf("invalid signature: failed blst SigValidate")
	}

	// blst.Serialize() returns [X, Y] (big-endian 48-byte elements)
	raw := p.Serialize()

	uncompressed := make([]byte, 128)
	// Copy X to limb 0 (padded)
	copy(uncompressed[16:64], raw[0:48])
	// Copy Y to limb 1 (padded)
	copy(uncompressed[64+16:128], raw[48:96])

	return uncompressed, nil
}

// DecompressG2To256Blst converts a 96-byte compressed G2 point into a 256-byte uncompressed format
// using blst. Format: (X.c0, X.c1, Y.c0, Y.c1) each as 64-byte big-endian limb.
func DecompressG2To256Blst(groupPublicKey []byte) ([]byte, error) {
	if len(groupPublicKey) != 96 {
		return nil, fmt.Errorf("invalid group public key length: expected 96 bytes, got %d", len(groupPublicKey))
	}
	p := new(blst.P2Affine).Uncompress(groupPublicKey)
	if p == nil {
		return nil, fmt.Errorf("failed to uncompress G2 key with blst")
	}
	// Public key validation for untrusted inputs (subgroup check + non-identity policy).
	if !p.KeyValidate() {
		return nil, fmt.Errorf("invalid G2 public key: failed blst KeyValidate")
	}

	// blst.Serialize() returns [X.c1, X.c0, Y.c1, Y.c0] (IETF standard)
	// each as a 48-byte big-endian element.
	raw := p.Serialize()

	// We need [X.c0, X.c1, Y.c0, Y.c1] to match gnark-crypto
	// and pad each to 64 bytes.
	uncompressed := make([]byte, 256)

	// Copy X.c0 (from raw[48:96]) to limb 0
	copy(uncompressed[0*64+16:1*64], raw[48:96])
	// Copy X.c1 (from raw[0:48]) to limb 1
	copy(uncompressed[1*64+16:2*64], raw[0:48])
	// Copy Y.c0 (from raw[144:192]) to limb 2
	copy(uncompressed[2*64+16:3*64], raw[144:192])
	// Copy Y.c1 (from raw[96:144]) to limb 3
	copy(uncompressed[3*64+16:4*64], raw[96:144])

	return uncompressed, nil
}

// VerifyFinalSignature verifies a BLS threshold aggregate signature against the epoch
// group public key.  This is the dapi-side mirror of verifyFinalSignatureBlst from
// inference-chain/x/bls/keeper/bls_crypto.go — identical algorithm, same gnark/blst libs.
//
// signature     — 48-byte compressed G1 aggregate signature (ThresholdSigningRequest.FinalSignature)
// messageHash   — 32-byte keccak256 of the signed data (ThresholdSigningRequest.MessageHash)
// groupPubKey   — 96-byte compressed G2 group public key (EpochBLSData.GroupPublicKey)
//
// Returns true only when the pairing equation e(sig, G2_gen) == e(H(msg), groupPubKey) holds.
func VerifyFinalSignature(signature, messageHash, groupPubKey []byte) (bool, error) {
	if len(signature) != 48 {
		return false, fmt.Errorf("signature must be 48 bytes, got %d", len(signature))
	}
	if len(messageHash) != 32 {
		return false, fmt.Errorf("messageHash must be 32 bytes, got %d", len(messageHash))
	}
	if len(groupPubKey) != 96 {
		return false, fmt.Errorf("groupPubKey must be 96 bytes, got %d", len(groupPubKey))
	}

	g1Sig := new(blst.P1Affine).Uncompress(signature)
	if g1Sig == nil {
		return false, fmt.Errorf("failed to uncompress signature")
	}
	if !g1Sig.SigValidate(true) {
		return false, fmt.Errorf("signature failed SigValidate")
	}

	g2Key := new(blst.P2Affine).Uncompress(groupPubKey)
	if g2Key == nil {
		return false, fmt.Errorf("failed to uncompress group public key")
	}
	if !g2Key.KeyValidate() {
		return false, fmt.Errorf("group public key failed KeyValidate")
	}

	// Hash message to G1 — must match chain-side hashToG1 exactly.
	// Single-field SWU map + G1 isogeny + cofactor clear (EIP-2537 compatible).
	var be [48]byte
	copy(be[48-32:], messageHash)
	var u fp.Element
	u.SetBytes(be[:])
	p := bls12381.MapToCurve1(&u)
	hash_to_curve.G1Isogeny(&p.X, &p.Y)
	var msgG1Gnark bls12381.G1Affine
	msgG1Gnark.ClearCofactor(&p)

	msgBytes := msgG1Gnark.Bytes()
	msgG1 := new(blst.P1Affine).Uncompress(msgBytes[:])
	if msgG1 == nil {
		return false, fmt.Errorf("failed to uncompress message G1 point")
	}

	g2Gen := blst.P2Generator().ToAffine()
	negKey := new(blst.P2).Sub(g2Key).ToAffine()

	// e(sig, G2_gen) · e(H(msg), -groupPubKey) == 1
	ml := blst.Fp12MillerLoopN([]blst.P2Affine{*g2Gen, *negKey}, []blst.P1Affine{*g1Sig, *msgG1})
	ml.FinalExp()
	one := blst.Fp12One()
	return ml.Equals(&one), nil
}
