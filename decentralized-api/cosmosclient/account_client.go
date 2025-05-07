package cosmosclient

import (
	"crypto/sha256"
	"decentralized-api/logging"
	"encoding/base64"
	"encoding/hex"

	"github.com/cosmos/btcutil/bech32"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/crypto/ripemd160"
)

// PubKeyToAddress Public key bytes to Cosmos address
//
//	pubKeyHex := "A1B2C3..." // Replace with your public key hex string
func PubKeyToAddress(pubKeyHex string) (string, error) {
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		logging.Error("Invalid public key hex", types.Participants, "err", err)
		return "", err
	}

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

func PubKeyToString(pubKey cryptotypes.PubKey) string {
	return base64.StdEncoding.EncodeToString(pubKey.Bytes())
}
