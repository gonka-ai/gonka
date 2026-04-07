package signing

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"fmt"

	"github.com/cosmos/btcutil/bech32"
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/ripemd160"
)

// Secp256k1Signer signs messages using a secp256k1 private key.
type Secp256k1Signer struct {
	key     *ecdsa.PrivateKey
	address string
}

func NewSecp256k1Signer(key *ecdsa.PrivateKey) (*Secp256k1Signer, error) {
	compressed := crypto.CompressPubkey(&key.PublicKey)
	addr, err := addressFromCompressed(compressed)
	if err != nil {
		return nil, fmt.Errorf("derive address: %w", err)
	}
	return &Secp256k1Signer{
		key:     key,
		address: addr,
	}, nil
}

func (s *Secp256k1Signer) Sign(message []byte) ([]byte, error) {
	hash := sha256.Sum256(message)
	return crypto.Sign(hash[:], s.key)
}

func (s *Secp256k1Signer) Address() string {
	return s.address
}

// Secp256k1Verifier recovers addresses from secp256k1 signatures.
type Secp256k1Verifier struct{}

func NewSecp256k1Verifier() *Secp256k1Verifier {
	return &Secp256k1Verifier{}
}

func (v *Secp256k1Verifier) RecoverAddress(message []byte, sig []byte) (string, error) {
	if len(sig) != 65 {
		return "", fmt.Errorf("invalid signature length: %d", len(sig))
	}
	hash := sha256.Sum256(message)
	pubkey, err := crypto.Ecrecover(hash[:], sig)
	if err != nil {
		return "", fmt.Errorf("ecrecover failed: %w", err)
	}
	return addressFromUncompressedPubkey(pubkey)
}

func (s *Secp256k1Signer) PublicKeyBytes() []byte {
	return crypto.FromECDSAPub(&s.key.PublicKey)
}

func GenerateKey() (*Secp256k1Signer, error) {
	key, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}
	return NewSecp256k1Signer(key)
}

// SignerFromHex creates a signer from a hex-encoded private key.
func SignerFromHex(hexKey string) (*Secp256k1Signer, error) {
	key, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		return nil, fmt.Errorf("parse hex key: %w", err)
	}
	return NewSecp256k1Signer(key)
}

// AddressFromPubKey derives a gonka bech32 address from a public key.
// Accepts both compressed (33-byte) and uncompressed (65-byte) keys.
func AddressFromPubKey(pubkey []byte) (string, error) {
	switch len(pubkey) {
	case 33:
		return addressFromCompressed(pubkey)
	case 65:
		return addressFromUncompressedPubkey(pubkey)
	default:
		return "", fmt.Errorf("invalid pubkey length: %d (expected 33 or 65)", len(pubkey))
	}
}

func addressFromUncompressedPubkey(pubkey []byte) (string, error) {
	pub, err := crypto.UnmarshalPubkey(pubkey)
	if err != nil {
		return "", fmt.Errorf("unmarshal pubkey: %w", err)
	}
	compressed := crypto.CompressPubkey(pub)
	return addressFromCompressed(compressed)
}

func addressFromCompressed(compressed []byte) (string, error) {
	sha := sha256.Sum256(compressed)
	rip := ripemd160.New() //nolint:gosec
	rip.Write(sha[:])
	addrBytes := rip.Sum(nil)
	conv, err := bech32.ConvertBits(addrBytes, 8, 5, true)
	if err != nil {
		return "", fmt.Errorf("convert bits: %w", err)
	}
	return bech32.Encode("gonka", conv)
}
