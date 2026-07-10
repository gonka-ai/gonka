package artifacts

import "testing"

// TestCOWStoreProofPaths verifies proofs served through the copy-on-write store
// across the three paths: the live tip, a historical committed count served from
// a retained snapshot (O(depth), no rebuild), and the same historical count
// after a restart (retained is in-memory, so recovery re-captures only the tip
// and older counts fall back to the exact rebuild). All must verify against the
// count's root.
func TestCOWStoreProofPaths(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}

	// Fill and flush in blocks so several committed counts exist.
	const block = 2000
	const blocks = 4
	var earlyCount uint32
	for b := 0; b < blocks; b++ {
		for i := b * block; i < (b+1)*block; i++ {
			if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
				t.Fatalf("add %d: %v", i, err)
			}
		}
		if err := store.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		if b == 0 {
			earlyCount = store.Count() // first committed count
		}
	}

	if len(store.retained) == 0 {
		t.Fatalf("no retained snapshots captured")
	}

	verifyAt := func(tag string, count uint32) {
		root, err := store.GetRootAt(count)
		if err != nil {
			t.Fatalf("%s GetRootAt(%d): %v", tag, count, err)
		}
		entries, err := store.GetArtifactsAndProofs([]uint32{count / 2}, count)
		if err != nil {
			t.Fatalf("%s proof@%d: %v", tag, count, err)
		}
		e := entries[0]
		if !VerifySMSTProofSlice(root, count, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
			t.Fatalf("%s proof did not verify at count %d", tag, count)
		}
	}

	// Live tip and a historical committed count (retained snapshot path).
	verifyAt("live", store.Count())
	verifyAt("retained-historical", earlyCount)

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Restart: recovery rebuilds the live tree and re-captures the tip; an older
	// committed count falls back to the exact rebuild and must still verify.
	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	if store2.Count() != uint32(block*blocks) {
		t.Fatalf("recovered count %d, want %d", store2.Count(), block*blocks)
	}
	// Recovery must re-capture retained snapshots at every committed count, not
	// just the tip, so historical proofs are served in O(depth) after a restart.
	if len(store2.retained) < blocks {
		t.Fatalf("post-restart retained=%d, want >= %d committed counts", len(store2.retained), blocks)
	}
	if _, ok := store2.retained[earlyCount]; !ok {
		t.Fatalf("early committed count %d not re-captured after restart", earlyCount)
	}

	root, err := store2.GetRootAt(earlyCount)
	if err != nil {
		t.Fatalf("post-restart GetRootAt(%d): %v", earlyCount, err)
	}
	entries, err := store2.GetArtifactsAndProofs([]uint32{earlyCount / 2}, earlyCount)
	if err != nil {
		t.Fatalf("post-restart proof@%d: %v", earlyCount, err)
	}
	e := entries[0]
	if !VerifySMSTProofSlice(root, earlyCount, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
		t.Fatalf("post-restart historical proof did not verify")
	}
}
