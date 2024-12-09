package cosmosclient

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"

	"github.com/cosmos/btcutil/bech32"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"golang.org/x/crypto/ripemd160"
)

// PubKeyToAddress Public key bytes to Cosmos address
//
//	pubKeyHex := "A1B2C3..." // Replace with your public key hex string
func PubKeyToAddress(pubKeyHex string) (string, error) {
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		slog.Error("Invalid public key hex", "err", err)
		return "", err
	}

	// Step 1: SHA-256 hash
	shaHash := sha256.Sum256(pubKeyBytes)

	// Step 2: RIPEMD-160 hash
	ripemdHasher := ripemd160.New()
	ripemdHasher.Write(shaHash[:])
	ripemdHash := ripemdHasher.Sum(nil)

	// Step 3: Bech32 encode
	prefix := "cosmos"
	fiveBitData, err := bech32.ConvertBits(ripemdHash, 8, 5, true)
	if err != nil {
		slog.Error("Failed to convert bits", "err", err)
		return "", err
	}

	address, err := bech32.Encode(prefix, fiveBitData)
	if err != nil {
		slog.Error("Failed to encode address", "err", err)
		return "", err
	}

	return address, nil
}

func PubKeyToString(pubKey cryptotypes.PubKey) string {
	return base64.StdEncoding.EncodeToString(pubKey.Bytes())
}
