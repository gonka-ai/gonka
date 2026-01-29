package cosmosclient

import (
	"crypto/sha256"
	"decentralized-api/logging"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/cosmos/btcutil/bech32"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/crypto/ripemd160"
)

// PubKeyBase64ToAddress converts a base64-encoded public key to a Cosmos bech32 address.
// This is used for chain queries and standard Cosmos format keys.
func PubKeyBase64ToAddress(pubKeyStr string) (string, error) {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKeyStr)
	if err != nil {
		logging.Error("Invalid base64 public key", types.Participants, "err", err)
		return "", err
	}

	return pubKeyBytesToAddress(pubKeyBytes)
}

// PubKeyHexToAddress converts a hex-encoded public key to a Cosmos bech32 address.
// This is used for PoC validation where keys are provided in hex format.
func PubKeyHexToAddress(pubKeyStr string) (string, error) {
	pubKeyBytes, err := hex.DecodeString(pubKeyStr)
	if err != nil {
		logging.Error("Invalid hex public key", types.Participants, "err", err)
		return "", err
	}

	return pubKeyBytesToAddress(pubKeyBytes)
}

// pubKeyBytesToAddress is the internal helper that performs the actual address derivation.
func pubKeyBytesToAddress(pubKeyBytes []byte) (string, error) {
	// Step 1: SHA-256 hash
	shaHash := sha256.Sum256(pubKeyBytes)

	// Step 2: RIPEMD-160 hash
	ripemdHasher := ripemd160.New()
	ripemdHasher.Write(shaHash[:])
	ripemdHash := ripemdHasher.Sum(nil)

	// Step 3: Bech32 encode
	prefix := "gonka"
	fiveBitData, err := bech32.ConvertBits(ripemdHash, 8, 5, true)
	if err != nil {
		logging.Error("Failed to convert bits", types.Participants, "err", err)
		return "", err
	}

	address, err := bech32.Encode(prefix, fiveBitData)
	if err != nil {
		logging.Error("Failed to encode address", types.Participants, "err", err)
		return "", err
	}

	return address, nil
}

// PubKeyToAddress converts a public key string to a Cosmos bech32 address.
// DEPRECATED: Accepts both base64-encoded (standard Cosmos format) and hex-encoded public keys.
// Use PubKeyBase64ToAddress or PubKeyHexToAddress for explicit control over encoding.
func PubKeyToAddress(pubKeyStr string) (string, error) {
	var pubKeyBytes []byte
	var err error

	// Try base64 first (standard Cosmos format from chain queries)
	pubKeyBytes, err = base64.StdEncoding.DecodeString(pubKeyStr)
	if err != nil {
		// Fallback to hex encoding (legacy format)
		pubKeyBytes, err = hex.DecodeString(pubKeyStr)
		if err != nil {
			logging.Error("Invalid public key (not base64 or hex)", types.Participants, "err", err, "error-type", fmt.Sprintf("%T", err))
			return "", err
		}
	}

	return pubKeyBytesToAddress(pubKeyBytes)
}

func PubKeyToString(pubKey cryptotypes.PubKey) string {
	return base64.StdEncoding.EncodeToString(pubKey.Bytes())
}
