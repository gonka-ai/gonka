package proof_of_compute

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/cometbft/cometbft/crypto"
)

type ProofOfWork struct {
	Hash      string
	Nonce     []byte
	BlockHash string
}

func proofOfWork(blockHash string, pubKey string) ProofOfWork {
	// Step 1: Concatenate Hash and pubKey
	concat := blockHash + pubKey

	// Step 2: Generate random bit sequence of the same length as concatenated string
	randomBytes := generateRandomBytes(len(concat))

	// Step 3: XOR random bit sequence with the concatenated string
	concatBytes := []byte(concat)
	xorResult := xorBytes(concatBytes, randomBytes)

	// Step 4: Apply SHA-256 to the XOR result
	hashResult := sha256.Sum256(xorResult)

	// Return the Hash result as a hex string
	return ProofOfWork{
		Hash:      hex.EncodeToString(hashResult[:]),
		Nonce:     randomBytes,
		BlockHash: blockHash,
	}
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
