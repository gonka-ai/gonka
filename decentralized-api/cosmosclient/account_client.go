package cosmosclient

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/cosmos/btcutil/bech32"
	"golang.org/x/crypto/ripemd160"
)

// PubKeyToAddress Public key bytes to Cosmos address
//
//	pubKeyHex := "A1B2C3..." // Replace with your public key hex string
func PubKeyToAddress(pubKeyHex string) {
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		fmt.Printf("Invalid public key hex: %v\n", err)
		return
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
		fmt.Printf("Failed to convert bits: %v\n", err)
		return
	}

	address, err := bech32.Encode(prefix, fiveBitData)
	if err != nil {
		fmt.Printf("Failed to encode address: %v\n", err)
		return
	}

	fmt.Printf("Address: %s\n", address)
}
