package types

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	errorsmod "cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	secp256k1dcrd "github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// BridgeKeyLinkSignText is the exact UTF-8 text an Ethereum wallet must
// personal_sign. chainID is the Gonka chain id. ethPubKeyCanonical is the
// compressed secp256k1 key as 0x-prefixed lowercase hex. nonce is decimal,
// with no leading zeros, and must increase for each new authorization of
// that Ethereum key.
func BridgeKeyLinkSignText(chainID, gonkaAddress, ethPubKeyCanonical string, nonce uint64) string {
	return fmt.Sprintf(
		"Link Ethereum key to Gonka account\nchain-id: %s\ngonka-address: %s\neth-pubkey: %s\nnonce: %d",
		chainID,
		gonkaAddress,
		ethPubKeyCanonical,
		nonce,
	)
}

// CanonicalSecp256k1PubKeyHex returns the 0x-prefixed lowercase hex form of a
// compressed secp256k1 public key. The signed bridge-link text uses this form.
func CanonicalSecp256k1PubKeyHex(pub []byte) string {
	return "0x" + hex.EncodeToString(pub)
}

// ParseCompressedSecp256k1PubKey accepts a compressed secp256k1 public key as
// hex (optional 0x prefix) or standard base64. Uncompressed keys are rejected
// so the stored address matches the bridge feeder, which hashes the compressed key.
func ParseCompressedSecp256k1PubKey(s string) ([]byte, error) {
	raw, err := decodePubKey(strings.TrimSpace(s))
	if err != nil {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, err.Error())
	}
	if len(raw) == 64 || len(raw) == 65 {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "uncompressed public key is not accepted; submit the 33-byte compressed key")
	}
	if len(raw) != 33 || (raw[0] != 0x02 && raw[0] != 0x03) {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "public key must be compressed secp256k1 (33 bytes, prefix 0x02 or 0x03)")
	}
	if _, err := secp256k1dcrd.ParsePubKey(raw); err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey, "invalid secp256k1 public key: %v", err)
	}
	return raw, nil
}

// maxPubKeyEncodingLen is the longest encoding ParseCompressedSecp256k1PubKey
// will inspect: "0x" plus 130 hex characters of an uncompressed key. Longer
// input is rejected before hex or base64 decoding. ValidateBasic runs
// pre-ante, so this bound is the guard against a large decode.
const maxPubKeyEncodingLen = 132

func decodePubKey(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("public key is empty")
	}
	if len(s) > maxPubKeyEncodingLen {
		return nil, fmt.Errorf("public key encoding exceeds %d characters", maxPubKeyEncodingLen)
	}
	hexPart := s
	if strings.HasPrefix(strings.ToLower(hexPart), "0x") {
		hexPart = hexPart[2:]
	}
	if len(hexPart) == 66 && isHexString(hexPart) {
		return hex.DecodeString(hexPart)
	}
	if len(hexPart) == 130 && isHexString(hexPart) {
		return hex.DecodeString(hexPart)
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("public key must be compressed secp256k1 hex or base64")
	}
	return decoded, nil
}

func isHexString(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// AddressFromCompressedPubKey returns the bech32 account address derived from
// a compressed secp256k1 public key. This is the same derivation the bridge
// feeder uses for MsgBridgeExchange.ownerAddress.
func AddressFromCompressedPubKey(pub []byte) (sdk.AccAddress, error) {
	if len(pub) != 33 {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "compressed public key must be 33 bytes")
	}
	return sdk.AccAddress((&secp256k1.PubKey{Key: pub}).Address()), nil
}
