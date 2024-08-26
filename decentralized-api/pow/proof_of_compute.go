package pow

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/cometbft/cometbft/crypto"
)

type HashAndNonce struct {
	Hash  string
	Input []byte
	Nonce []byte
}

func proofOfWork(input []byte, nonce []byte) HashAndNonce {
	inputBytes := []byte(input)
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

func generateRandomBytes(length int) []byte {
	randomBytes := crypto.CRandBytes(length)
	return randomBytes
}

func incrementBytes(nonce []byte) {
	for i := len(nonce) - 1; i >= 0; i-- {
		nonce[i]++
		if nonce[i] != 0 {
			break // If no carry, we're done
		}
	}
}
