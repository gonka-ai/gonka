package proof_of_compute

import (
	"testing"
	"time"
)

func TestNewPowOrchestrator(t *testing.T) {
	pubKey := "uCIGWUwW8jqyg7IhVqpWP8g2qfTjq0KMISt7reXqxr8="
	blockHash := "4A29D310402743E6587D219E1E975701ACA3EAE583AA88AA91B50FF3EF519167"
	orchestrator := NewPowOrchestrator(pubKey, 10)

	startTime := time.Now()
	for {
		proof := proofOfWork(blockHash, pubKey)
		if orchestrator.acceptProofOfCompute(&proof) {
			println("Found!")
			println(proof.Hash)
			println(proof.Nonce)
			break
		}

		println(proof.Hash)

		if time.Since(startTime) > 5*time.Second {
			t.Errorf("Proof of compute took too long to generate")
			break
		}
	}
}
