package artifacts

import (
	"testing"
)

// TestSMSTAttackerScenarios provides comprehensive security tests simulating
// various attack vectors to ensure the SMST implementation is robust.
//
// Vectors tested:
// 1. Proof Forgery: Modifying sibling counts to spoof dense index
// 2. Proof Forgery: Modifying sibling hashes to spoof root
// 3. Identity Theft: Claiming a proof for Nonce A belongs to Nonce B
// 4. Index Spoofing: Claiming a proof for Index X belongs to Index Y
// 5. Count Inflation: Claiming a root represents more items than it does
func TestSMSTAttackerScenarios(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	// Setup: Insert known artifacts
	// Use non-sequential nonces to make tree structure interesting
	nonces := []int32{10, 50, 20, 40, 30}
	for _, n := range nonces {
		if err := store.Add(n, []byte{byte(n)}); err != nil {
			t.Fatalf("Add(%d) failed: %v", n, err)
		}
	}
	store.Flush()

	count := store.Count()      // Should be 5
	rootHash := store.GetRoot() // Committed root

	// Helper to get valid proof components for a nonce
	getComponents := func(targetNonce int32) (uint32, []byte, []SMSTProofElement) {
		// Find index by iterating
		var targetIndex uint32
		var found bool
		for i := uint32(0); i < count; i++ {
			n, _, _, _ := store.GetArtifactAndProof(i, count)
			if n == targetNonce {
				targetIndex = i
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Setup error: nonce %d not found", targetNonce)
		}

		// Get valid proof
		_, vector, proofBytes, _ := store.GetArtifactAndProof(targetIndex, count)
		elements := DecodeProofElements(proofBytes)
		leafData := encodeLeaf(targetNonce, vector)

		return targetIndex, leafData, elements
	}

	// -------------------------------------------------------------------------
	// Attack 1: Modifying Sibling Counts (Inflation / Index Spoofing)
	// -------------------------------------------------------------------------
	// Attacker tries to change a sibling count in the proof to make the
	// verifier calculate a different dense index, hoping to match a target index.
	t.Run("Attack_ModifySiblingCount", func(t *testing.T) {
		targetNonce := int32(50) // Nonce 50 is at index 4 (sorted order: 10, 20, 30, 40, 50)
		realIndex, leafData, elements := getComponents(targetNonce)

		if realIndex != 4 {
			t.Fatalf("Setup assumptions wrong: expected index 4 for nonce 50, got %d", realIndex)
		}

		// Try to spoof index 3 by reducing a sibling count
		// We copy elements to avoid modifying original
		spoofedElements := make([]SMSTProofElement, len(elements))
		copy(spoofedElements, elements)

		// Modify the first non-zero count we find
		modified := false
		for i := range spoofedElements {
			if spoofedElements[i].SiblingCount > 0 {
				spoofedElements[i].SiblingCount-- // Decrease count
				modified = true
				break
			}
		}

		if !modified {
			t.Skip("Could not find suitable element to modify for this test case")
		}

		// Attempt verification
		// Expected result: Verification fails because changing count changes the node hash,
		// which propagates to a different root hash.
		if VerifySMSTProofWithCounts(rootHash, count, targetNonce, leafData, spoofedElements) {
			t.Error("Security Breach: Proof with modified sibling count was accepted!")
		} else {
			t.Log("Success: Modified sibling count rejected (root hash mismatch)")
		}
	})

	// -------------------------------------------------------------------------
	// Attack 2: Modifying Sibling Hashes
	// -------------------------------------------------------------------------
	// Attacker tries to change a sibling hash.
	t.Run("Attack_ModifySiblingHash", func(t *testing.T) {
		targetNonce := int32(10)
		_, leafData, elements := getComponents(targetNonce)

		spoofedElements := make([]SMSTProofElement, len(elements))
		copy(spoofedElements, elements)

		// Flip a bit in the first sibling hash
		if len(spoofedElements) > 0 {
			spoofedElements[0].SiblingHash[0] ^= 0xFF
		}

		if VerifySMSTProofWithCounts(rootHash, count, targetNonce, leafData, spoofedElements) {
			t.Error("Security Breach: Proof with modified sibling hash was accepted!")
		} else {
			t.Log("Success: Modified sibling hash rejected")
		}
	})

	// -------------------------------------------------------------------------
	// Attack 3: Identity Theft (Swapping Nonce)
	// -------------------------------------------------------------------------
	// Attacker takes a valid proof for Nonce A and claims it proves Nonce B.
	// This tests that the verification path is strictly bound to the nonce.
	t.Run("Attack_IdentityTheft", func(t *testing.T) {
		nonceA := int32(10)
		nonceB := int32(20)

		_, _, elementsA := getComponents(nonceA)

		// Attacker constructs leaf data for B but uses A's proof
		vectorB := []byte{byte(nonceB)}
		leafDataB := encodeLeaf(nonceB, vectorB)

		// Verify proof A against Nonce B should fail
		// The verifier will construct path(B). Since path(A) != path(B), 
		// the proof elements (siblings of path A) will be wrong for path B.
		if VerifySMSTProofWithCounts(rootHash, count, nonceB, leafDataB, elementsA) {
			t.Error("Security Breach: Proof for Nonce A accepted for Nonce B!")
		} else {
			t.Log("Success: Identity theft rejected (path mismatch)")
		}
	})

	// -------------------------------------------------------------------------
	// Attack 4: Index Spoofing (Claiming Wrong Index)
	// -------------------------------------------------------------------------
	// Attacker has a valid proof for Nonce A at Index X.
	// Attacker claims this proof satisfies a request for Index Y.
	t.Run("Attack_IndexSpoofing", func(t *testing.T) {
		nonce := int32(10)
		realIndex, leafData, elements := getComponents(nonce)

		targetIndex := realIndex + 1 // Claim it's at the next slot
		proofSlice := encodeTestProofForTransport(elements)

		if VerifySMSTProofWithDenseIndex(rootHash, count, targetIndex, nonce, leafData, proofSlice) {
			t.Errorf("Security Breach: Proof for index %d accepted for index %d!", realIndex, targetIndex)
		} else {
			t.Logf("Success: Index spoofing rejected (calculated index %d != claimed %d)", realIndex, targetIndex)
		}
	})

	// -------------------------------------------------------------------------
	// Attack 5: Count Inflation (Claiming Higher Total Count)
	// -------------------------------------------------------------------------
	// Attacker commits to Root R (which encodes count C).
	// Attacker claims the count is C+1.
	// Verifier uses C+1 in the check.
	t.Run("Attack_CountInflation", func(t *testing.T) {
		nonce := int32(10)
		_, leafData, elements := getComponents(nonce)

		inflatedCount := count + 1

		// VerifySMSTProofWithCounts checks: if currentCount != count { return false }
		// But currentCount is derived from the proof's sibling sums + 1.
		// If we use the original proof, currentCount will equal 'count'.
		// So 'count' != 'inflatedCount' will fail.

		if VerifySMSTProofWithCounts(rootHash, inflatedCount, nonce, leafData, elements) {
			t.Error("Security Breach: Proof accepted against inflated total count!")
		} else {
			t.Log("Success: Count inflation rejected")
		}
	})

	// -------------------------------------------------------------------------
	// Attack 6: Tree Substitution
	// -------------------------------------------------------------------------
	// Attacker generates a completely different tree (valid structure)
	// and tries to use proofs from it against the honest root.
	t.Run("Attack_TreeSubstitution", func(t *testing.T) {
		// Create a fake store with different data
		fakeDir := t.TempDir()
		fakeStore, _ := OpenSMST(fakeDir)
		defer fakeStore.Close()

		fakeStore.Add(999, []byte{0xFF})
		fakeStore.Flush()

		// Get valid proof from fake tree
		_, fakeVector, fakeProofBytes, _ := fakeStore.GetArtifactAndProof(0, 1)
		fakeLeafData := encodeLeaf(999, fakeVector)
		fakeElements := DecodeProofElements(fakeProofBytes)

		// Try to verify fake proof against HONEST root
		if VerifySMSTProofWithCounts(rootHash, count, 999, fakeLeafData, fakeElements) {
			t.Error("Security Breach: Proof from fake tree accepted against honest root!")
		} else {
			t.Log("Success: Tree substitution rejected")
		}
	})
}
