package bls

import (
	"fmt"

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
