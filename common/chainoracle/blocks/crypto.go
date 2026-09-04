package blocks

import (
	"crypto/sha256"
	"fmt"

	"golang.org/x/crypto/ripemd160" //nolint:gosec // RIPEMD-160 is fixed by the Cosmos address derivation.
)

// AddressBytes returns the 20-byte address derived from a secp256k1 public
// key. Accepts both the 33-byte compressed and 65-byte uncompressed forms.
//
// The derivation is RIPEMD160(SHA256(compressed)), matching host signing.
func AddressBytes(pubkey []byte) ([]byte, error) {
	compressed, err := compressedPubkey(pubkey)
	if err != nil {
		return nil, err
	}
	sha := sha256.Sum256(compressed)
	rip := ripemd160.New() //nolint:gosec
	rip.Write(sha[:])
	return rip.Sum(nil), nil
}

func compressedPubkey(pubkey []byte) ([]byte, error) {
	switch len(pubkey) {
	case 33:
		if pubkey[0] != 0x02 && pubkey[0] != 0x03 {
			return nil, fmt.Errorf("invalid compressed pubkey prefix: %#x", pubkey[0])
		}
		return pubkey, nil
	case 65:
		if pubkey[0] != 0x04 {
			return nil, fmt.Errorf("invalid uncompressed pubkey prefix: %#x", pubkey[0])
		}
		out := make([]byte, 33)
		if pubkey[64]%2 == 0 {
			out[0] = 0x02
		} else {
			out[0] = 0x03
		}
		copy(out[1:], pubkey[1:33])
		return out, nil
	default:
		return nil, fmt.Errorf("invalid pubkey length: %d (expected 33 or 65)", len(pubkey))
	}
}
