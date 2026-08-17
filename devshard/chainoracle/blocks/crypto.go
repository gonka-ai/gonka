package blocks

import (
	"crypto/sha256"
	"fmt"

	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/ripemd160" //nolint:staticcheck // cosmos address scheme
)

// AddressBytes returns the 20-byte address derived from a secp256k1 public
// key. Accepts both the 33-byte compressed and 65-byte uncompressed forms.
//
// The derivation matches devshard/signing: RIPEMD160(SHA256(compressed)).
// We duplicate that single-purpose helper here instead of importing signing
// so the blockoracle package stays self-contained.
func AddressBytes(pubkey []byte) ([]byte, error) {
	var compressed []byte
	switch len(pubkey) {
	case 33:
		compressed = pubkey
	case 65:
		pub, err := crypto.UnmarshalPubkey(pubkey)
		if err != nil {
			return nil, fmt.Errorf("unmarshal pubkey: %w", err)
		}
		compressed = crypto.CompressPubkey(pub)
	default:
		return nil, fmt.Errorf("invalid pubkey length: %d (expected 33 or 65)", len(pubkey))
	}
	sha := sha256.Sum256(compressed)
	rip := ripemd160.New() //nolint:gosec
	rip.Write(sha[:])
	return rip.Sum(nil), nil
}
