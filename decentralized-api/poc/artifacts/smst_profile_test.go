package artifacts

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestSMSTBuildProfile measures ingest wall-time and resident heap for a live
// SMST built from N monotonic nonces (stride 100 = the worst porosity the chain
// allows, matching real PoC assignment). Leaf hashes are generated inline so
// only the tree stays resident, isolating the tree's own cost. Env-gated: set
// SMST_PROF_N to the leaf count to run a single scale, e.g.
//
//	SMST_PROF_N=1000000 SMST_DEFERRED_HASH=1 SMST_PARALLEL_HASH=1 \
//	  go test ./poc/artifacts/ -run TestSMSTBuildProfile -v -timeout 30m
//
// Run once per scale in its own process so heap is released between runs.
// Reports insert time vs GetRoot/ensureHashed time separately so deferred
// multicore fill is visible.
func TestSMSTBuildProfile(t *testing.T) {
	v := os.Getenv("SMST_PROF_N")
	if v == "" {
		t.Skip("set SMST_PROF_N to run the build profile")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		t.Fatalf("invalid SMST_PROF_N=%q", v)
	}
	const stride = 100

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)

	tree := NewSMST(0)
	tree.deferredHash = smstDeferredHashFromEnv()
	tree.parallelHash = smstParallelHashFromEnv()

	tIns := time.Now()
	for i := 0; i < n; i++ {
		leaf := smstHashLeaf(testVector(i))
		if _, err := tree.Insert(int32(i*stride), leaf); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	insElapsed := time.Since(tIns)

	tRoot := time.Now()
	root, count := tree.GetRoot()
	rootElapsed := time.Since(tRoot)
	elapsed := insElapsed + rootElapsed

	runtime.GC()
	runtime.ReadMemStats(&m1)
	runtime.KeepAlive(tree)

	heapBytes := int64(m1.HeapAlloc) - int64(m0.HeapAlloc)
	mb := float64(heapBytes) / (1024 * 1024)
	t.Logf("RESULT deferred=%v parallel=%v N=%-9d depth=%d  insert=%-12s  getroot=%-12s  total=%-12s  %6.0f ns/leaf  heap=%8.1f MB  %5.0f B/leaf  root=%x count=%d",
		tree.deferredHash, tree.parallelHash, n, tree.Depth(),
		insElapsed.Round(time.Millisecond), rootElapsed.Round(time.Microsecond), elapsed.Round(time.Millisecond),
		float64(elapsed.Nanoseconds())/float64(n),
		mb, float64(heapBytes)/float64(n), root[:6], count)
}

const profFlushCount = 30
const profEarlyFlush = 10 // 1/3 of 30

// TestSMSTStoreFlush30Profile is the realistic PoC ingest profile:
// N leaves across 30 equal flushes; at flush #10 (1/3) the early commit is
// snapshotted via PrebuildSnapshot. Then an early-count proof is timed.
//
// Deferred × parallel matrix (COW on so snap cost stays out of the way):
//
//	# deferred + multicore ensureHashed (production-like)
//	SMST_PROF_N=300000 SMST_DEFERRED_HASH=1 SMST_PARALLEL_HASH=1 SMST_COW=1 \
//	  go test ./poc/artifacts/ -run TestSMSTStoreFlush30Profile -v
//
//	# deferred + serial ensureHashed
//	SMST_PROF_N=300000 SMST_DEFERRED_HASH=1 SMST_PARALLEL_HASH=0 SMST_COW=1 \
//	  go test ./poc/artifacts/ -run TestSMSTStoreFlush30Profile -v
//
//	# eager path hash + parallel flag (parallel unused on per-insert path)
//	SMST_PROF_N=300000 SMST_DEFERRED_HASH=0 SMST_PARALLEL_HASH=1 SMST_COW=1 \
//	  go test ./poc/artifacts/ -run TestSMSTStoreFlush30Profile -v
//
//	# eager path hash, serial
//	SMST_PROF_N=300000 SMST_DEFERRED_HASH=0 SMST_PARALLEL_HASH=0 SMST_COW=1 \
//	  go test ./poc/artifacts/ -run TestSMSTStoreFlush30Profile -v
func TestSMSTStoreFlush30Profile(t *testing.T) {
	v := os.Getenv("SMST_PROF_N")
	if v == "" {
		t.Skip("set SMST_PROF_N to run the 30-flush profile")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < profFlushCount {
		t.Fatalf("invalid SMST_PROF_N=%q (need >= %d)", v, profFlushCount)
	}
	if n%profFlushCount != 0 {
		t.Fatalf("SMST_PROF_N=%d must be divisible by %d", n, profFlushCount)
	}
	batch := n / profFlushCount

	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	var (
		earlyCount  uint32
		snapElapsed time.Duration
		nonce       int32
	)

	tIngest := time.Now()
	for f := 1; f <= profFlushCount; f++ {
		for j := 0; j < batch; j++ {
			if err := store.AddWithNode(nonce, testVector(int(nonce)), "n"); err != nil {
				t.Fatalf("add %d: %v", nonce, err)
			}
			nonce++
		}
		if err := store.Flush(); err != nil {
			t.Fatalf("flush %d: %v", f, err)
		}
		if f == profEarlyFlush {
			earlyCount = store.Count()
			tSnap := time.Now()
			if err := store.PrebuildSnapshot(earlyCount); err != nil {
				t.Fatalf("PrebuildSnapshot(%d): %v", earlyCount, err)
			}
			snapElapsed = time.Since(tSnap)
		}
	}
	ingestElapsed := time.Since(tIngest)

	if earlyCount == 0 {
		t.Fatal("early count not recorded")
	}

	tProof := time.Now()
	entries, err := store.GetArtifactsAndProofs([]uint32{earlyCount / 2}, earlyCount)
	if err != nil {
		t.Fatalf("early proof: %v", err)
	}
	proofElapsed := time.Since(tProof)
	if len(entries) != 1 {
		t.Fatalf("expected 1 proof entry, got %d", len(entries))
	}

	globalSnapshotCache.mu.Lock()
	_, cacheHit := globalSnapshotCache.entries[snapshotCacheKey{store: store, count: earlyCount}]
	globalSnapshotCache.mu.Unlock()
	_, retainedHit := store.retained[earlyCount]

	mode := fmt.Sprintf("deferred=%v parallel=%v cow=%v", store.smst.deferredHash, store.smst.parallelHash, store.cowEnabled)
	t.Logf("RESULT mode=%s N=%d flushes=%d early_flush=%d early_count=%d ingest=%s snap=%s early_proof=%s ns/leaf=%.0f retained_hit=%v cache_hit=%v retained_n=%d gomaxprocs=%d",
		mode, n, profFlushCount, profEarlyFlush, earlyCount,
		ingestElapsed.Round(time.Millisecond),
		snapElapsed.Round(time.Microsecond),
		proofElapsed.Round(time.Microsecond),
		float64(ingestElapsed.Nanoseconds())/float64(n),
		retainedHit, cacheHit, len(store.retained), runtime.GOMAXPROCS(0))
}
