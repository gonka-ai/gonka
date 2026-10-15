package artifacts

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// TestSMSTStoreScaleMeasure measures disk, RSS, recover, and batched proofs
// for a production-shaped SMST store (24-byte k_dim=12 fp16 vectors, sequential
// nonces, COW+deferred+parallel defaults). Env:
//
//	SMST_PROF_N=1000000 SMST_PROF_DIR=/tmp/smst-scale \
//	  go test ./poc/artifacts/ -run TestSMSTStoreScaleMeasure -v -timeout 2h -count=1
//
// SMST_PROF_FLUSH (default 100000) is the ingest flush period.
// SMST_PROF_SKIP_INGEST=1 reopens an existing dir (load+proof only).
func TestSMSTStoreScaleMeasure(t *testing.T) {
	v := os.Getenv("SMST_PROF_N")
	if v == "" {
		t.Skip("set SMST_PROF_N to run the scale measure")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		t.Fatalf("invalid SMST_PROF_N=%q", v)
	}
	flushEvery := 100_000
	if s := os.Getenv("SMST_PROF_FLUSH"); s != "" {
		flushEvery, err = strconv.Atoi(s)
		if err != nil || flushEvery <= 0 {
			t.Fatalf("invalid SMST_PROF_FLUSH=%q", s)
		}
	}
	dir := os.Getenv("SMST_PROF_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	skipIngest := os.Getenv("SMST_PROF_SKIP_INGEST") == "1"
	skipLoad := os.Getenv("SMST_PROF_SKIP_LOAD") == "1"

	vec := make([]byte, 24)
	rss0 := procRSS()

	if !skipIngest {
		for _, name := range []string{"artifacts.data", "distributions.jsonl", "flushed_roots.jsonl"} {
			_ = os.Remove(filepath.Join(dir, name))
		}
		_ = os.RemoveAll(filepath.Join(dir, suffixDirName))
		store, err := OpenSMST(dir)
		if err != nil {
			t.Fatalf("OpenSMST ingest: %v", err)
		}
		tIngest := time.Now()
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint64(vec[0:8], uint64(i))
			binary.LittleEndian.PutUint64(vec[8:16], uint64(i)*2654435761)
			copied := append([]byte(nil), vec...)
			if err := store.AddWithNode(int32(i), copied, "n"); err != nil {
				t.Fatalf("add %d: %v", i, err)
			}
			if (i+1)%flushEvery == 0 {
				if err := store.Flush(); err != nil {
					t.Fatalf("flush at %d: %v", i+1, err)
				}
			}
		}
		if err := store.Flush(); err != nil {
			t.Fatalf("final flush: %v", err)
		}
		ingestElapsed := time.Since(tIngest)
		runtime.GC()
		rssLive := procRSS()
		t.Logf("INGEST N=%d flush_every=%d elapsed=%s ns/leaf=%.0f rss0=%s rss_live=%s delta=%s disk=%s",
			n, flushEvery, ingestElapsed.Round(time.Millisecond),
			float64(ingestElapsed.Nanoseconds())/float64(n),
			fmtBytes(rss0), fmtBytes(rssLive), fmtBytes(rssLive-rss0), fmtBytes(dirSize(dir)))
		if err := store.Close(); err != nil {
			t.Fatalf("close after ingest: %v", err)
		}
		store = nil
		runtime.GC()
	}
	if skipLoad {
		return
	}

	disk := dirSize(dir)
	runtime.GC()
	rssBeforeLoad := procRSS()
	tLoad := time.Now()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST recover: %v", err)
	}
	loadElapsed := time.Since(tLoad)
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	runtime.KeepAlive(store)
	rssAfterLoad := procRSS()
	count := store.Count()
	if int(count) != n {
		t.Fatalf("recovered count %d want %d", count, n)
	}
	t.Logf("LOAD N=%d elapsed=%s rate=%.0f/s rss_before=%s rss_after=%s delta=%s heap_inuse=%s heap_alloc=%s sys=%s disk=%s depth=%d retained=%d spilled=%v ram_limit=%d record_size=%d suffixes=%d",
		n, loadElapsed.Round(time.Millisecond), float64(n)/loadElapsed.Seconds(),
		fmtBytes(rssBeforeLoad), fmtBytes(rssAfterLoad), fmtBytes(rssAfterLoad-rssBeforeLoad),
		fmtBytes(int64(ms.HeapInuse)), fmtBytes(int64(ms.HeapAlloc)), fmtBytes(int64(ms.Sys)),
		fmtBytes(disk), store.smst.Depth(), len(store.retained),
		store.spilled, store.ramLeafLimit, store.pagedRecordSize, len(store.suffixes))

	for _, k := range []int{100, 1000, 100_000} {
		if k > n {
			t.Logf("PROOF k=%d skipped (N=%d)", k, n)
			continue
		}
		idx := make([]uint32, k)
		step := uint32(n / k)
		if step == 0 {
			step = 1
		}
		for i := 0; i < k; i++ {
			idx[i] = uint32(i) * step
			if idx[i] >= uint32(n) {
				idx[i] = uint32(n - 1)
			}
		}
		tProof := time.Now()
		entries, err := store.GetArtifactsAndProofs(idx, uint32(n))
		proofElapsed := time.Since(tProof)
		if err != nil {
			t.Fatalf("proof k=%d: %v", k, err)
		}
		if len(entries) != k {
			t.Fatalf("proof k=%d got %d entries", k, len(entries))
		}
		proofBytes := 0
		for _, e := range entries {
			for _, p := range e.Proof {
				proofBytes += len(p)
			}
		}
		prefixes := make(map[uint32]struct{}, k)
		for _, e := range entries {
			prefixes[suffixPrefix(e.Nonce)] = struct{}{}
		}
		rssProof := procRSS()
		t.Logf("PROOF k=%-6d elapsed=%s  %.1f us/proof  %.0f proofs/s  avg_proof_bytes=%.0f  prefixes=%d  rss=%s",
			k, proofElapsed, float64(proofElapsed.Microseconds())/float64(k),
			float64(k)/proofElapsed.Seconds(), float64(proofBytes)/float64(k), len(prefixes),
			fmtBytes(rssProof))
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestSMSTPagedWriteScale measures paged ingest rate and on-disk growth.
//
//	SMST_PAGED_WRITE_N=4000000 SMST_PAGED_WRITE_STREAMS=64 \
//	SMST_PAGED_WRITE_BATCH=4000 SMST_PAGED_WRITE_FLUSH=100000 \
//	SMST_RAM_LEAVES=200000 \
//	  go test ./poc/artifacts/ -run '^TestSMSTPagedWriteScale$' -v -timeout 30m
//
// STREAMS=0 (default) is sequential nonces 0..N-1. STREAMS>0 uses the MLNode
// interleave nonce = stream + x*streams, arriving in per-stream batches.
func TestSMSTPagedWriteScale(t *testing.T) {
	v := os.Getenv("SMST_PAGED_WRITE_N")
	if v == "" {
		t.Skip("set SMST_PAGED_WRITE_N to run the paged write scale")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		t.Fatalf("invalid SMST_PAGED_WRITE_N=%q", v)
	}
	streams := envInt("SMST_PAGED_WRITE_STREAMS", 0)
	batch := envInt("SMST_PAGED_WRITE_BATCH", 4000)
	flushEvery := envInt("SMST_PAGED_WRITE_FLUSH", 100000)
	if os.Getenv(envSMSTRAMLeaves) == "" {
		t.Setenv(envSMSTRAMLeaves, "200000")
	}

	dir := os.Getenv("SMST_PROF_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"artifacts.data", "distributions.jsonl", "flushed_roots.jsonl"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	_ = os.RemoveAll(filepath.Join(dir, suffixDirName))

	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	vec := make([]byte, 24)
	add := func(nonce int32, i int) {
		binary.LittleEndian.PutUint64(vec[0:8], uint64(i))
		binary.LittleEndian.PutUint64(vec[8:16], uint64(nonce)*2654435761)
		copied := append([]byte(nil), vec...)
		if err := store.AddWithNode(nonce, copied, "n"); err != nil {
			t.Fatalf("add nonce=%d i=%d: %v", nonce, i, err)
		}
		if (i+1)%flushEvery == 0 {
			if err := store.Flush(); err != nil {
				t.Fatalf("flush at %d: %v", i+1, err)
			}
		}
	}

	rss0 := procRSS()
	tIngest := time.Now()
	if streams <= 0 {
		for i := 0; i < n; i++ {
			add(int32(i), i)
		}
	} else {
		if batch <= 0 {
			batch = 4000
		}
		next := make([]int, streams)
		added := 0
		for added < n {
			for s := 0; s < streams && added < n; s++ {
				for b := 0; b < batch && added < n; b++ {
					nonce := int32(s + next[s]*streams)
					next[s]++
					add(nonce, added)
					added++
				}
			}
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("final flush: %v", err)
	}
	elapsed := time.Since(tIngest)
	rssLive := procRSS()
	disk := dirSize(dir)
	dataSz := fileSize(filepath.Join(dir, "artifacts.data"))
	suffixSz := dirSize(filepath.Join(dir, suffixDirName))
	span := 0
	if streams > 0 && batch > 0 {
		span = batch * streams / smstSuffixSlots
	}
	t.Logf("WRITE N=%d streams=%d batch=%d span_prefixes~%d flush_every=%d spilled=%v suffixes=%d elapsed=%s rate=%.0f/s ns/leaf=%.0f rss0=%s rss_live=%s disk=%s data=%s suffix=%s B/leaf=%.1f",
		n, streams, batch, span, flushEvery, store.spilled, len(store.suffixes),
		elapsed.Round(time.Millisecond), float64(n)/elapsed.Seconds(),
		float64(elapsed.Nanoseconds())/float64(n),
		fmtBytes(rss0), fmtBytes(rssLive), fmtBytes(disk), fmtBytes(dataSz), fmtBytes(suffixSz),
		float64(disk)/float64(n))
	if int(store.Count()) != n {
		t.Fatalf("count %d want %d", store.Count(), n)
	}
}

func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func procRSS() int64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0
			}
			kb, _ := strconv.ParseInt(fields[1], 10, 64)
			return kb * 1024
		}
	}
	return 0
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func fmtBytes(n int64) string {
	if n < 0 {
		return "-" + fmtBytes(-n)
	}
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
