package artifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func testVector(i int) []byte {
	v := make([]byte, 24)
	binary.LittleEndian.PutUint64(v[0:8], uint64(i))
	binary.LittleEndian.PutUint64(v[8:16], uint64(i*2654435761))
	return v
}

func perfN(def int) int {
	if v := os.Getenv("SMST_PERF_N"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// deferredDataset returns a deterministic nonce sequence that forces early depth
// expansion (nonces >= 2^25), includes negatives (path via uint32, depth 32),
// then a long sequential run — exercising every hashing path a flush touches.
func deferredDataset(n int) []int32 {
	out := make([]int32, 0, n+8)
	out = append(out, 1<<25, (1<<25)+1, 1<<28, -1, -2, -1000000)
	for i := 0; i < n; i++ {
		out = append(out, int32(i))
	}
	return out
}

// TestDeferredHashFingerprint builds a large store with periodic flushes,
// verifies every sampled proof against its flush-count root, and folds all
// roots plus sampled proofs into a single deterministic SHA-256 fingerprint.
// The fingerprint is stable across runs; each proof is verified independently
// against its root, so a matching fingerprint means deferred hashing reproduces
// the same roots and proofs the tree commits to.
func TestDeferredHashFingerprint(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	nonces := deferredDataset(200000)
	fp := sha256.New()
	const flushEvery = 20000
	added := 0

	for _, nonce := range nonces {
		if err := store.AddWithNode(nonce, testVector(int(nonce)), "n"); err != nil {
			t.Fatalf("add %d: %v", nonce, err)
		}
		added++
		if added%flushEvery != 0 {
			continue
		}
		if err := store.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		count := store.Count()
		root, err := store.GetRootAt(count)
		if err != nil {
			t.Fatalf("GetRootAt(%d): %v", count, err)
		}
		var cb [4]byte
		binary.LittleEndian.PutUint32(cb[:], count)
		fp.Write(cb[:])
		fp.Write(root)

		for _, idx := range []uint32{0, count / 3, count / 2, count - 1} {
			entries, err := store.GetArtifactsAndProofs([]uint32{idx}, count)
			if err != nil {
				t.Fatalf("proof idx=%d count=%d: %v", idx, count, err)
			}
			e := entries[0]
			leaf := encodeLeaf(e.Nonce, e.Vector)
			if !VerifySMSTProofSlice(root, count, e.Nonce, leaf, e.Proof) {
				t.Fatalf("proof failed idx=%d count=%d nonce=%d", idx, count, e.Nonce)
			}
			for _, p := range e.Proof {
				fp.Write(p)
			}
		}
	}

	t.Logf("SMST FINGERPRINT (200k + specials, flush 20k): %x", fp.Sum(nil))
}

// TestDeferredGetRootStableAndIdempotent checks the deferred-hash lifecycle in
// isolation: GetRoot on an empty tree returns the empty root, and after inserts
// two consecutive GetRoot calls (fill-then-reuse) return the identical hash.
func TestDeferredGetRootStableAndIdempotent(t *testing.T) {
	tree := NewSMST(0)
	empty, count := tree.GetRoot()
	if count != 0 || len(empty) == 0 {
		t.Fatalf("empty tree: count=%d rootLen=%d", count, len(empty))
	}

	for i := 0; i < 5000; i++ {
		if _, err := tree.Insert(int32(i), smstHashLeaf(testVector(i))); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	first, c1 := tree.GetRoot()
	second, c2 := tree.GetRoot() // no new inserts: ensureHashed must be a no-op
	if c1 != c2 || c1 != 5000 {
		t.Fatalf("count drift: %d vs %d", c1, c2)
	}
	if string(first) != string(second) {
		t.Fatalf("root not stable across GetRoot calls")
	}
}

// TestParallelHashMatchesSerial checks multicore ensureHashed produces the same
// root as serial fill (and as eager inserts).
func TestParallelHashMatchesSerial(t *testing.T) {
	const n = 20_000
	const stride = 100

	build := func(deferred, parallel bool) []byte {
		tree := NewSMST(0)
		tree.deferredHash = deferred
		tree.parallelHash = parallel
		for i := 0; i < n; i++ {
			if _, err := tree.Insert(int32(i*stride), smstHashLeaf(testVector(i))); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}
		root, count := tree.GetRoot()
		if count != n {
			t.Fatalf("count=%d want %d", count, n)
		}
		return root
	}

	serial := build(true, false)
	parallel := build(true, true)
	eager := build(false, false)
	eagerPar := build(false, true) // parallel unused on eager path fill; root must still match
	if !bytes.Equal(serial, parallel) {
		t.Fatalf("deferred serial vs parallel root mismatch:\n serial=%x\n paral=%x", serial, parallel)
	}
	if !bytes.Equal(serial, eager) {
		t.Fatalf("deferred vs eager root mismatch:\n def=%x\n eager=%x", serial, eager)
	}
	if !bytes.Equal(eager, eagerPar) {
		t.Fatalf("eager serial vs parallel-flag root mismatch:\n a=%x\n b=%x", eager, eagerPar)
	}
}

// TestDeferredLiveTipProofSlowThenFast exercises both live-tip proof paths in
// acquireSnapshotTree: a proof requested before any GetRoot must fill hashes
// under the write lock, then retry onto the RLock fast path; a later proof at
// the same count is served entirely under RLock. Both must verify.
func TestDeferredLiveTipProofSlowThenFast(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	const n = 3000
	for i := 0; i < n; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	count := store.Count()

	// Slow path: no GetRoot yet, so the live tip still carries deferred hashes.
	slow, err := store.GetArtifactsAndProofs([]uint32{count / 2}, count)
	if err != nil {
		t.Fatalf("slow-path proof: %v", err)
	}
	root, err := store.GetRootAt(count)
	if err != nil {
		t.Fatalf("GetRootAt: %v", err)
	}
	e := slow[0]
	if !VerifySMSTProofSlice(root, count, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
		t.Fatalf("slow-path proof did not verify")
	}

	// Fast path: hashes now filled, same live count served under the read lock.
	fast, err := store.GetArtifactsAndProofs([]uint32{count / 2}, count)
	if err != nil {
		t.Fatalf("fast-path proof: %v", err)
	}
	f := fast[0]
	if !VerifySMSTProofSlice(root, count, f.Nonce, encodeLeaf(f.Nonce, f.Vector), f.Proof) {
		t.Fatalf("fast-path proof did not verify")
	}
}

// TestDeferredLiveTipConcurrentProofsBeforeHashFill hammers the live tip with
// concurrent proofs before any GetRoot. Hash fill must run once under Lock;
// proof I/O must not hold the write lock, so readers finish without serializing
// the whole batch on Lock. Under -race this also covers the ensureHashed→retry
// handoff.
func TestDeferredLiveTipConcurrentProofsBeforeHashFill(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	const n = 4000
	for i := 0; i < n; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	count := store.Count()

	const readers = 16
	var wg sync.WaitGroup
	errCh := make(chan error, readers)
	for g := 0; g < readers; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			idx := uint32(gid) % count
			entries, err := store.GetArtifactsAndProofs([]uint32{idx}, count)
			if err != nil {
				errCh <- err
				return
			}
			root, err := store.GetRootAt(count)
			if err != nil {
				errCh <- err
				return
			}
			e := entries[0]
			if !VerifySMSTProofSlice(root, count, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
				errCh <- fmt.Errorf("proof verify failed for goroutine %d", gid)
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent tip proof: %v", err)
	}
}

// TestDeferredHistoricalRebuildProof forces the O(N) rebuild path: a proof at a
// past flush count is reconstructed from the log by rebuildTreeFromInputs, which
// must hash the fresh tree (ensureHashed) before serving. The proof must verify
// against the historical root.
func TestDeferredHistoricalRebuildProof(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	for i := 0; i < 4000; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	historical := store.Count()
	histRoot, err := store.GetRootAt(historical)
	if err != nil {
		t.Fatalf("GetRootAt(historical): %v", err)
	}

	// Advance past the flush so `historical` is no longer the live tip.
	for i := 4000; i < 6000; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}

	entries, err := store.GetArtifactsAndProofs([]uint32{historical / 2}, historical)
	if err != nil {
		t.Fatalf("historical proof: %v", err)
	}
	e := entries[0]
	if !VerifySMSTProofSlice(histRoot, historical, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
		t.Fatalf("historical (rebuilt) proof did not verify")
	}
}

// TestDeferredStoreEdgeCases covers the error and empty-tree branches of the
// deferred-hash read paths: an empty store roots to nil, out-of-range and
// zero counts are rejected, and a closed store returns ErrStoreClosed rather
// than touching the tree.
func TestDeferredStoreEdgeCases(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}

	if r := store.GetRoot(); r != nil {
		t.Fatalf("empty store root = %x, want nil", r)
	}
	if r, err := store.GetRootAt(0); err != nil || r != nil {
		t.Fatalf("GetRootAt(0) = (%x, %v), want (nil, nil)", r, err)
	}

	for i := 0; i < 500; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	count := store.Count()

	if _, err := store.GetRootAt(count + 1); err == nil {
		t.Fatalf("GetRootAt(count+1) should error")
	}
	// Proof beyond the live count reaches acquireSnapshotTree's range check.
	if _, err := store.GetArtifactsAndProofs([]uint32{0}, count+1); err == nil {
		t.Fatalf("proof at count+1 should error")
	}

	// Directly exercise acquireSnapshotTree's slow-path range check: an
	// over-count that is not the live tip falls through to the write-locked
	// branch and must report the mismatch.
	if _, _, err := store.acquireSnapshotTree(count + 2); err == nil {
		t.Fatalf("acquireSnapshotTree(count+2) should error")
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := store.GetRootAt(count); err != ErrStoreClosed {
		t.Fatalf("closed GetRootAt err = %v, want ErrStoreClosed", err)
	}
	if _, _, err := store.acquireSnapshotTree(count); err != ErrStoreClosed {
		t.Fatalf("closed acquireSnapshotTree err = %v, want ErrStoreClosed", err)
	}
}

// TestIngestPerfPortable times building an N-leaf tree via Insert plus one
// GetRoot (which fills deferred hashes). It reports the ingest cost that
// deferred hashing reduces by hashing shared upper nodes once at GetRoot rather
// than on every insert that passes through them.
func TestIngestPerfPortable(t *testing.T) {
	n := perfN(200000)
	nonces := make([]int32, n)
	leaves := make([][]byte, n)
	for i := 0; i < n; i++ {
		nonces[i] = int32(i)
		leaves[i] = smstHashLeaf(testVector(i))
	}

	t0 := time.Now()
	tree := NewSMST(0)
	for i := 0; i < n; i++ {
		if _, err := tree.Insert(nonces[i], leaves[i]); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	root, _ := tree.GetRoot()
	elapsed := time.Since(t0)

	t.Logf("INGEST N=%d: %v  root=%x", n, elapsed, root[:8])
}
