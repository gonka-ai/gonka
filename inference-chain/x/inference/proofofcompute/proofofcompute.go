package proofofcompute

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func GetInput(blockHash, pubKey string) []byte {
	return []byte(blockHash + pubKey)
}

type HashAndNonce struct {
	Hash  string
	Input []byte
	Nonce []byte
}

func ProofOfCompute(input []byte, nonce []byte) HashAndNonce {
	inputBytes := input
	xorResult := xorBytes(inputBytes, nonce)

	hashResult := sha256.Sum256(xorResult)

	return HashAndNonce{
		Hash:  hex.EncodeToString(hashResult[:]),
		Input: inputBytes,
		Nonce: nonce,
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

func AcceptHash(hash string, difficulty int) bool {
	prefix := strings.Repeat("0", difficulty)
	return strings.HasPrefix(hash, prefix)
}
