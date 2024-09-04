package poc

import (
	"github.com/productscience/inference/x/inference/proofofcompute"
	"testing"
	"time"
)

func TestNewPoCOrchestrator(t *testing.T) {
	pubKey := "uCIGWUwW8jqyg7IhVqpWP8g2qfTjq0KMISt7reXqxr8="
	blockHash := "4A29D310402743E6587D219E1E975701ACA3EAE583AA88AA91B50FF3EF519167"
	orchestrator := NewPoCOrchestrator(pubKey, 1)

	startTime := time.Now()
	input := proofofcompute.GetInput(blockHash, pubKey)
	nonce := make([]byte, len(input))
	for {
		hashAndNonce := proofofcompute.ProofOfCompute(input, nonce)

		if orchestrator.acceptHash(hashAndNonce.Hash) {
			elapsed := time.Since(startTime)
			println("Found! Elapsed time: ", elapsed)
			println(hashAndNonce.Hash)
			println(hashAndNonce.Nonce)
			break
		}

		incrementBytes(nonce)

		// println(hashAndNonce.Hash)

		if time.Since(startTime) > 10*time.Minute {
			t.Errorf("Proof of compute took too long to generate")
			break
		}
	}
}
