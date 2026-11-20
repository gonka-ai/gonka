package types

import (
	"errors"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

const CompressedSecp256k1PubKeyLen = 33

var (
	ErrInvalidSecp256k1PubKeyLength = errors.New("invalid compressed secp256k1 public key length")
	ErrInvalidSecp256k1PubKeyPrefix = errors.New("invalid compressed secp256k1 public key prefix")
	ErrInvalidSecp256k1CurvePoint   = errors.New("secp256k1 public key is not a valid curve point")
)

// ParseCompressedSecp256k1PubKey validates that pubKeyBytes is an exact 33-byte
// compressed secp256k1 public key (0x02 or 0x03 prefix) whose coordinates lie on
// the secp256k1 curve, using the same parser as DAPI ECIES encryption.
// Errors are bounded and do not echo the raw attacker-controlled key bytes.
func ParseCompressedSecp256k1PubKey(pubKeyBytes []byte) (*secp256k1.PublicKey, error) {
	if len(pubKeyBytes) != CompressedSecp256k1PubKeyLen {
		return nil, fmt.Errorf("%w: expected %d bytes, got %d bytes", ErrInvalidSecp256k1PubKeyLength, CompressedSecp256k1PubKeyLen, len(pubKeyBytes))
	}
	if pubKeyBytes[0] != 0x02 && pubKeyBytes[0] != 0x03 {
		return nil, fmt.Errorf("%w: expected 0x02 or 0x03, got 0x%02x", ErrInvalidSecp256k1PubKeyPrefix, pubKeyBytes[0])
	}
	pubKey, err := secp256k1.ParsePubKey(pubKeyBytes)
	if err != nil {
		return nil, ErrInvalidSecp256k1CurvePoint
	}
	return pubKey, nil
}

// ValidateCompressedSecp256k1PubKey checks that pubKeyBytes encodes a valid
// 33-byte compressed point on secp256k1.
func ValidateCompressedSecp256k1PubKey(pubKeyBytes []byte) error {
	_, err := ParseCompressedSecp256k1PubKey(pubKeyBytes)
	return err
}
