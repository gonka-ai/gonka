package utils

import (
	"encoding/base64"

	sdkerrors "cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types/errors"
	blstypes "github.com/productscience/inference/x/bls/types"
)

const (
	// maxBase64KeyDecodeLen bounds base64 input before allocation in pre-ante ValidateBasic.
	// A 33-byte compressed secp256k1 key is 44 base64 chars; 128 chars allows up to 96 decoded
	// bytes so 65-byte uncompressed keys still reach the exact byte-length check while rejecting
	// oversized payloads before decoding.
	maxBase64KeyDecodeLen = 128
)

// SafeCreateED25519ValidatorKey creates an ED25519 key and catches the panic that causes consensus failure
// This is the minimal fix for the bug report - let cosmos-sdk crypto do the validation
func SafeCreateED25519ValidatorKey(validatorKeyBase64 string) (pubKey cryptotypes.PubKey, err error) {
	if validatorKeyBase64 == "" {
		return nil, sdkerrors.Wrap(errors.ErrInvalidPubKey, "validator key cannot be empty")
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(validatorKeyBase64)
	if err != nil {
		return nil, sdkerrors.Wrapf(errors.ErrInvalidPubKey, "failed to decode validator key: %v", err)
	}

	// Check size - ED25519 keys must be exactly 32 bytes (this is the core issue)
	if len(pubKeyBytes) != 32 {
		return nil, sdkerrors.Wrapf(errors.ErrInvalidPubKey,
			"ED25519 validator key must be exactly 32 bytes, got %d bytes for ", len(pubKeyBytes))
	}

	pubKey = &ed25519.PubKey{Key: pubKeyBytes}

	// Test that the key works - catch any panics from Address() call
	defer func() {
		if r := recover(); r != nil {
			err = sdkerrors.Wrapf(errors.ErrInvalidPubKey, "invalid ED25519 key format: %v", r)
		}
	}()

	_ = pubKey.Address() // This is where the panic occurs with invalid keys

	return pubKey, err
}

// ValidateCompressedSECP256K1PubKey validates raw compressed secp256k1 public key bytes
// using the shared curve-point validator (33 bytes, 0x02/0x03 prefix, valid secp256k1 curve point).
func ValidateCompressedSECP256K1PubKey(pubKeyBytes []byte) error {
	if len(pubKeyBytes) != blstypes.CompressedSecp256k1PubKeyLen {
		return sdkerrors.Wrapf(errors.ErrInvalidPubKey,
			"SECP256K1 account key must be exactly 33 bytes, got %d bytes", len(pubKeyBytes))
	}

	if pubKeyBytes[0] != 0x02 && pubKeyBytes[0] != 0x03 {
		return sdkerrors.Wrapf(errors.ErrInvalidPubKey,
			"SECP256K1 key must be in compressed format (first byte should be 0x02 or 0x03)")
	}

	if err := blstypes.ValidateCompressedSecp256k1PubKey(pubKeyBytes); err != nil {
		return sdkerrors.Wrapf(errors.ErrInvalidPubKey,
			"SECP256K1 key is not a valid curve point: %v", err)
	}

	return nil
}

// SafeCreateSECP256K1AccountKey creates a SECP256K1 key with error handling
func SafeCreateSECP256K1AccountKey(accountKeyBase64 string) (pubKey cryptotypes.PubKey, err error) {
	if accountKeyBase64 == "" {
		return nil, sdkerrors.Wrap(errors.ErrInvalidPubKey, "account key cannot be empty")
	}
	if len(accountKeyBase64) > maxBase64KeyDecodeLen {
		return nil, sdkerrors.Wrapf(errors.ErrInvalidPubKey,
			"SECP256K1 account key base64 length %d exceeds maximum %d", len(accountKeyBase64), maxBase64KeyDecodeLen)
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(accountKeyBase64)
	if err != nil {
		return nil, sdkerrors.Wrapf(errors.ErrInvalidPubKey, "failed to decode account key: %v", err)
	}

	if err := ValidateCompressedSECP256K1PubKey(pubKeyBytes); err != nil {
		return nil, err
	}

	pubKey = &secp256k1.PubKey{Key: pubKeyBytes}

	// Test that the key works - catch any panics from Address() call
	defer func() {
		if r := recover(); r != nil {
			err = sdkerrors.Wrapf(errors.ErrInvalidPubKey, "invalid SECP256K1 key format: %v", r)
		}
	}()

	_ = pubKey.Address() // This triggers any validation panics

	return pubKey, err
}
