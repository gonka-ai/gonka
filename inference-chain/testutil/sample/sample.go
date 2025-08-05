package sample

import (
	"encoding/base64"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// AccAddress returns a sample account address
func AccAddress() string {
	pk := ed25519.GenPrivKey().PubKey()
	addr := pk.Address()
	return sdk.AccAddress(addr).String()
	// TODO: Check if we should use secp256k1 instead
	// return sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address()).String()
}

// AccAddressAndValAddress returns a sample account address and its corresponding validator address
func AccAddressAndValAddress() (sdk.ValAddress, sdk.AccAddress) {
	addr := secp256k1.GenPrivKey().PubKey().Address()
	return sdk.ValAddress(addr), sdk.AccAddress(addr)
}

// ValidED25519ValidatorKey returns a valid ED25519 validator public key (base64 encoded)
func ValidED25519ValidatorKey() string {
	privKey := ed25519.GenPrivKey()
	pubKey := privKey.PubKey()
	return base64.StdEncoding.EncodeToString(pubKey.Bytes())
}

// ValidSECP256K1AccountKey returns a valid SECP256K1 account public key (base64 encoded)
func ValidSECP256K1AccountKey() string {
	privKey := secp256k1.GenPrivKey()
	pubKey := privKey.PubKey()
	return base64.StdEncoding.EncodeToString(pubKey.Bytes())
}

// InvalidED25519ValidatorKeys returns various invalid ED25519 validator keys for testing
func InvalidED25519ValidatorKeys() map[string]string {
	return map[string]string{
		// Invalid key from actual bug report that caused consensus failure
		"bug_report_key": "AggLJgjYij7iN/qmWohnV5mU7CdcYFGw9qd3NlsvZ28c",

		// Wrong sizes
		"too_short": base64.StdEncoding.EncodeToString([]byte("short")),
		"too_long":  base64.StdEncoding.EncodeToString(make([]byte, 64)), // 64 bytes instead of 32

		// Invalid base64 encodings
		"invalid_base64":    "invalid-base64-string!!!",
		"malformed_base64":  "AGF*&^%",
		"incomplete_base64": "AggLJgjYij7iN/qmWohnV5mU7CdcYFGw9qd3NlsvZ28", // Missing padding

		// Edge cases (these are technically valid but cryptographically weak)
		// "null_bytes":         base64.StdEncoding.EncodeToString(make([]byte, 32)), // All zeros - valid but weak
		// "max_bytes":          base64.StdEncoding.EncodeToString(bytes32(0xFF)),    // All 0xFF - valid but weak
		"single_byte": base64.StdEncoding.EncodeToString([]byte{0x01}),
		"31_bytes":    base64.StdEncoding.EncodeToString(make([]byte, 31)),
		"33_bytes":    base64.StdEncoding.EncodeToString(make([]byte, 33)),
		"huge_key":    base64.StdEncoding.EncodeToString(make([]byte, 1024)),

		// Special characters and edge cases
		"only_spaces":   "    ",
		"newline_chars": "\n\r\t",
		"unicode_chars": "测试🔑",
	}
}

// InvalidSECP256K1AccountKeys returns various invalid SECP256K1 account keys for testing
func InvalidSECP256K1AccountKeys() map[string]string {
	return map[string]string{
		// Wrong sizes
		"too_short": base64.StdEncoding.EncodeToString([]byte("short")),
		"too_long":  base64.StdEncoding.EncodeToString(make([]byte, 65)), // 65 bytes instead of 33

		// Wrong format (uncompressed keys start with 0x04 and are 65 bytes)
		"uncompressed_key": base64.StdEncoding.EncodeToString(append([]byte{0x04}, make([]byte, 64)...)),
		"wrong_prefix":     base64.StdEncoding.EncodeToString(append([]byte{0x01}, make([]byte, 32)...)),
		"no_prefix":        base64.StdEncoding.EncodeToString(make([]byte, 32)), // 32 bytes without proper prefix

		// Invalid base64 encodings
		"invalid_base64":    "invalid-base64-string!!!",
		"malformed_base64":  "AGF*&^%",
		"incomplete_base64": "Agg1JgjYij7iN/qmWohnV5mU7CdcYFGw9qd3NlsvZ2", // Missing padding

		// Edge cases (these are technically valid but cryptographically weak)
		// "null_bytes":         base64.StdEncoding.EncodeToString(make([]byte, 33)), // All zeros - valid but weak
		// "max_bytes":          base64.StdEncoding.EncodeToString(bytes33(0xFF)),    // All 0xFF - valid but weak
		"single_byte": base64.StdEncoding.EncodeToString([]byte{0x02}),     // Only prefix
		"32_bytes":    base64.StdEncoding.EncodeToString(make([]byte, 32)), // ED25519 size
		"34_bytes":    base64.StdEncoding.EncodeToString(make([]byte, 34)), // One byte too many
		"huge_key":    base64.StdEncoding.EncodeToString(make([]byte, 1024)),

		// Special characters and edge cases
		"only_spaces":   "    ",
		"newline_chars": "\n\r\t",
		"unicode_chars": "测试🔑",
	}
}

// ValidKeyPairs returns valid key pairs for testing successful operations
func ValidKeyPairs() map[string]map[string]string {
	return map[string]map[string]string{
		"pair_1": {
			"validator_key": ValidED25519ValidatorKey(),
			"account_key":   ValidSECP256K1AccountKey(),
		},
		"pair_2": {
			"validator_key": ValidED25519ValidatorKey(),
			"account_key":   ValidSECP256K1AccountKey(),
		},
		"pair_3": {
			"validator_key": ValidED25519ValidatorKey(),
			"account_key":   ValidSECP256K1AccountKey(),
		},
	}
}

// WeakButValidED25519Keys returns cryptographically weak but technically valid ED25519 keys
func WeakButValidED25519Keys() map[string]string {
	return map[string]string{
		"null_bytes": base64.StdEncoding.EncodeToString(make([]byte, 32)), // All zeros
		"max_bytes":  base64.StdEncoding.EncodeToString(bytes32(0xFF)),    // All 0xFF
	}
}

// WeakButValidSECP256K1Keys returns cryptographically weak but technically valid SECP256K1 keys
func WeakButValidSECP256K1Keys() map[string]string {
	nullBytes := make([]byte, 33)
	nullBytes[0] = 0x02 // Proper prefix for compressed key
	// Rest are zeros

	return map[string]string{
		"null_bytes": base64.StdEncoding.EncodeToString(nullBytes),     // All zeros with proper prefix
		"max_bytes":  base64.StdEncoding.EncodeToString(bytes33(0xFF)), // All 0xFF with proper prefix
	}
}

// bytes32 creates a 32-byte slice filled with the given value
func bytes32(value byte) []byte {
	result := make([]byte, 32)
	for i := range result {
		result[i] = value
	}
	return result
}

// bytes33 creates a 33-byte slice filled with the given value (with proper SECP256K1 prefix)
func bytes33(value byte) []byte {
	result := make([]byte, 33)
	result[0] = 0x02 // Proper compressed key prefix
	for i := 1; i < len(result); i++ {
		result[i] = value
	}
	return result
}
