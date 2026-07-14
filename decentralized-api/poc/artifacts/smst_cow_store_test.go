package artifacts

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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

// TestCOWRetainedProofsDoNotHoldWriteLock checks that historical retained proofs
// release the write lock before artifact I/O: concurrent proof readers at an
// early committed count must not serialize ingest. Under -race this also guards
// the unlock→RLock handoff in acquireSnapshotTree.
func TestCOWRetainedProofsDoNotHoldWriteLock(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	const early = 2000
	for i := 0; i < early; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	earlyCount := store.Count()
	earlyRoot, err := store.GetRootAt(earlyCount)
	if err != nil {
		t.Fatalf("GetRootAt(%d): %v", earlyCount, err)
	}
	if _, ok := store.retained[earlyCount]; !ok {
		t.Fatalf("expected retained snapshot at %d", earlyCount)
	}

	var (
		wg           sync.WaitGroup
		proofOK      int64
		proofErr     int64
		writesOK     int64
		readerDone   = make(chan struct{})
		nextNonce    = int32(early)
	)

	const readers = 8
	for g := 0; g < readers; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			idx := uint32(gid) % earlyCount
			for {
				select {
				case <-readerDone:
					return
				default:
				}
				entries, err := store.GetArtifactsAndProofs([]uint32{idx}, earlyCount)
				if err != nil || len(entries) != 1 {
					atomic.AddInt64(&proofErr, 1)
					continue
				}
				e := entries[0]
				if !VerifySMSTProofSlice(earlyRoot, earlyCount, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
					atomic.AddInt64(&proofErr, 1)
					continue
				}
				atomic.AddInt64(&proofOK, 1)
			}
		}(g)
	}

	// Writer must make progress while retained proofs are in flight. If proofs
	// still held the write lock across I/O, Adds would stall behind every batch.
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case <-deadline:
			close(readerDone)
			wg.Wait()
			if atomic.LoadInt64(&proofOK) == 0 {
				t.Fatalf("no successful retained proofs")
			}
			if atomic.LoadInt64(&proofErr) != 0 {
				t.Fatalf("retained proof errors: %d", proofErr)
			}
			if atomic.LoadInt64(&writesOK) == 0 {
				t.Fatalf("ingest made no progress while retained proofs ran (write lock held across I/O?)")
			}
			return
		default:
			n := atomic.AddInt32(&nextNonce, 1) - 1
			if err := store.AddWithNode(n, testVector(int(n)), "n"); err != nil {
				t.Fatalf("concurrent add %d: %v", n, err)
			}
			if n%200 == 0 {
				if err := store.Flush(); err != nil {
					t.Fatalf("concurrent flush: %v", err)
				}
			}
			atomic.AddInt64(&writesOK, 1)
		}
	}
}

// TestFlushedRootsSurviveLostDistributionHistory checks that a flush count
// remains provable after restart even when distributions.jsonl lost that entry
// (the warn-only appendDistributionSnapshot failure mode). flushed_roots.jsonl
// is the durable commit journal used to re-capture retained snapshots.
func TestFlushedRootsSurviveLostDistributionHistory(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}

	const early = 500
	for i := 0; i < early; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush early: %v", err)
	}
	earlyCount := store.Count()
	earlyRoot, err := store.GetRootAt(earlyCount)
	if err != nil {
		t.Fatalf("GetRootAt(%d): %v", earlyCount, err)
	}
	earlyRoot = bytes.Clone(earlyRoot)

	for i := early; i < early*2; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush final: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Simulate distribution append failure / loss: wipe distributions.jsonl but
	// keep flushed_roots.jsonl (and artifacts.data).
	if err := os.Truncate(filepath.Join(dir, "distributions.jsonl"), 0); err != nil {
		t.Fatalf("truncate distributions: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "flushed_roots.jsonl")); err != nil {
		t.Fatalf("flushed_roots.jsonl missing after flush: %v", err)
	}

	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	if _, ok := store2.distributionHistory[earlyCount]; ok {
		t.Fatalf("expected distributionHistory to lack %d after truncate", earlyCount)
	}
	if _, ok := store2.retained[earlyCount]; !ok {
		t.Fatalf("expected retained snapshot at %d after restart without dist history", earlyCount)
	}
	root2, err := store2.GetRootAt(earlyCount)
	if err != nil {
		t.Fatalf("post-restart GetRootAt(%d): %v", earlyCount, err)
	}
	if !bytes.Equal(earlyRoot, root2) {
		t.Fatalf("early root changed across restart without dist history")
	}
	entries, err := store2.GetArtifactsAndProofs([]uint32{earlyCount / 2}, earlyCount)
	if err != nil {
		t.Fatalf("post-restart proof@%d: %v", earlyCount, err)
	}
	e := entries[0]
	if !VerifySMSTProofSlice(root2, earlyCount, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
		t.Fatalf("post-restart early proof did not verify")
	}
}

// TestSMSTCOWEnvDisabled uses in-place Insert (deferred hashing only): no
// retained snapshots, roots still match the COW path.
func TestSMSTCOWEnvDisabled(t *testing.T) {
	t.Setenv(envSMSTCOW, "0")
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()
	if store.cowEnabled {
		t.Fatalf("expected cowEnabled=false with %s=0", envSMSTCOW)
	}

	const n = 500
	for i := 0; i < n; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(store.retained) != 0 {
		t.Fatalf("retained=%d, want 0 when COW disabled", len(store.retained))
	}
	root := store.GetRoot()
	if root == nil {
		t.Fatal("nil root")
	}

	// Same workload with COW on must produce the same tip root.
	t.Setenv(envSMSTCOW, "1")
	dir2 := t.TempDir()
	store2, err := OpenSMST(dir2)
	if err != nil {
		t.Fatalf("OpenSMST cow: %v", err)
	}
	defer store2.Close()
	if !store2.cowEnabled {
		t.Fatal("expected cowEnabled=true")
	}
	for i := 0; i < n; i++ {
		if err := store2.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("cow add %d: %v", i, err)
		}
	}
	if err := store2.Flush(); err != nil {
		t.Fatalf("cow flush: %v", err)
	}
	root2 := store2.GetRoot()
	if !bytes.Equal(root, root2) {
		t.Fatalf("root mismatch COW off vs on:\n off=%x\n on= %x", root, root2)
	}
}

// TestSMSTCOWDisabledEarlySnapshotCache checks that with SMST_COW=0 the early
// flush count is pinned into the process snapshot cache (upgrade-v0.2.14 path)
// so proofs at that count stay available after the tip advances — without
// retained COW snapshots.
func TestSMSTCOWDisabledEarlySnapshotCache(t *testing.T) {
	t.Setenv(envSMSTCOW, "0")
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	const early = 300
	for i := 0; i < early; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush early: %v", err)
	}
	earlyCount := store.Count()
	earlyRoot, err := store.GetRootAt(earlyCount)
	if err != nil {
		t.Fatalf("GetRootAt(%d): %v", earlyCount, err)
	}
	earlyRoot = bytes.Clone(earlyRoot)

	// Commit-worker style: PrebuildSnapshot while tip still equals early count.
	if err := store.PrebuildSnapshot(earlyCount); err != nil {
		t.Fatalf("PrebuildSnapshot(%d): %v", earlyCount, err)
	}

	globalSnapshotCache.mu.Lock()
	entry, ok := globalSnapshotCache.entries[snapshotCacheKey{store: store, count: earlyCount}]
	globalSnapshotCache.mu.Unlock()
	if !ok || entry == nil || !entry.pinned {
		t.Fatalf("early count %d must be pinned in snapshot cache when COW disabled (ok=%v pinned=%v)",
			earlyCount, ok, ok && entry.pinned)
	}
	if len(store.retained) != 0 {
		t.Fatalf("retained must stay empty with COW off, got %d", len(store.retained))
	}

	// Advance tip past the early commit.
	for i := early; i < early*2; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush final: %v", err)
	}

	root2, err := store.GetRootAt(earlyCount)
	if err != nil {
		t.Fatalf("post-advance GetRootAt(%d): %v", earlyCount, err)
	}
	if !bytes.Equal(earlyRoot, root2) {
		t.Fatalf("early root changed after tip advanced")
	}
	entries, err := store.GetArtifactsAndProofs([]uint32{earlyCount / 2}, earlyCount)
	if err != nil {
		t.Fatalf("early proof after tip advanced: %v", err)
	}
	e := entries[0]
	if !VerifySMSTProofSlice(root2, earlyCount, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
		t.Fatalf("early proof did not verify after tip advanced")
	}
}

// TestSMSTDefaultsDeferredAndCOW ensures unset env keeps production defaults:
// deferred hashing on and COW on.
func TestSMSTDefaultsDeferredAndCOW(t *testing.T) {
	t.Setenv(envSMSTCOW, "")
	t.Setenv(envSMSTDeferredHash, "")
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()
	if !store.cowEnabled {
		t.Fatal("cowEnabled default want true")
	}
	if !store.smst.deferredHash {
		t.Fatal("deferredHash default want true")
	}
}
