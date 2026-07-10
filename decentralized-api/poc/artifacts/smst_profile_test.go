package artifacts

import (
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
//	SMST_PROF_N=1000000 go test ./poc/artifacts/ -run TestSMSTBuildProfile -v -timeout 30m
//
// Run once per scale in its own process so heap is released between runs.
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

	t0 := time.Now()
	tree := NewSMST(0)
	for i := 0; i < n; i++ {
		leaf := smstHashLeaf(testVector(i))
		if _, err := tree.Insert(int32(i*stride), leaf); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	root, count := tree.GetRoot() // fills deferred hashes (no-op under eager build)
	elapsed := time.Since(t0)

	runtime.GC()
	runtime.ReadMemStats(&m1)
	runtime.KeepAlive(tree)

	heapBytes := int64(m1.HeapAlloc) - int64(m0.HeapAlloc)
	mb := float64(heapBytes) / (1024 * 1024)
	t.Logf("N=%-9d depth=%d  ingest=%-12s  %6.0f ns/leaf  heap=%8.1f MB  %5.0f B/leaf  root=%x count=%d",
		n, tree.Depth(), elapsed.Round(time.Millisecond), float64(elapsed.Nanoseconds())/float64(n),
		mb, float64(heapBytes)/float64(n), root[:6], count)
}
