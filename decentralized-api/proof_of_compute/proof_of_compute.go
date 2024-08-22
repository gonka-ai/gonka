package proof_of_compute

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/cometbft/cometbft/crypto"
)

func proofOfCompute(blockHash string, pubKey string) string {
	// Step 1: Concatenate hash and pubKey
	concat := blockHash + pubKey

	// Step 2: Generate random bit sequence of the same length as concatenated string
	randomBits := generateRandomBytes(len(concat))

	// Step 3: XOR random bit sequence with the concatenated string
	concatBytes := []byte(concat)
	xorResult := xorBytes(concatBytes, randomBits)

	// Step 4: Apply SHA-256 to the XOR result
	hashResult := sha256.Sum256(xorResult)

	// Return the hash result as a hex string
	return hex.EncodeToString(hashResult[:])
}

func xorBytes(a, b []byte) []byte {
	length := len(a)
	if len(b) < length {
		length = len(b)
	}

	result := make([]byte, length)
	for i := 0; i < length; i++ {
		result[i] = a[i] ^ b[i]
	}
	return result
}

func generateRandomBytes(length int) []byte {
	randomBytes := crypto.CRandBytes(length)
	return randomBytes
}
