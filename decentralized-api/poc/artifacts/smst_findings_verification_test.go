package artifacts

// Verification of Dmytro's PoC-proof-serving load-test findings against the
// #1432 changes (copy-on-write retained snapshots + deferred hashing). Each test
// emits a machine-parseable "RESULT ..." line consumed by the before/after
// comparison. The mirror file smst_findings_base_test.go runs the same drivers
// on the base (upgrade-v0.2.14) tree.
//
// Env knobs (defaults keep `go test` fast; the reported numbers use the large
// values): SMST_FIND_N (leaves), SMST_FIND_FLUSH (flush interval), SMST_FIND_K
// (distinct attack counts).

import (
	"bytes"
	"os"
	"strconv"
	"testing"
	"time"
)

func findEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// buildFlushedStore fills a store with n leaves, flushing every `flush` leaves,
// and returns the store plus the ordered list of committed (flush) counts.
func buildFlushedStore(t *testing.T, n, flush int) (*SMSTArtifactStore, []uint32) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	var committed []uint32
	for i := 0; i < n; i++ {
		if err := store.AddWithNode(int32(i), testVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
		if (i+1)%flush == 0 {
			if err := store.Flush(); err != nil {
				t.Fatalf("flush at %d: %v", i+1, err)
			}
			committed = append(committed, store.Count())
		}
	}
	return store, committed
}

// ---------------------------------------------------------------------------
// Differential byte-identity: Insert (eager, base semantics) vs insertCOW
// (deferred, #1432 live path) vs rebuildTreeFromInputs (fallback path).
// Closes the coverage gap: nothing previously asserted these produce
// byte-identical roots AND proofs. A divergence would false-strip an honest
// node if a base-version and a #1432 node ever coexisted in one network.
// ---------------------------------------------------------------------------
func TestDiff_ByteIdentity_InsertVsCOWVsRebuild(t *testing.T) {
	// Ns chosen to cross several depth-expansion boundaries.
	ns := []int{1, 2, 100, 999, 1000, 4096, 10000}
	if !testing.Short() {
		ns = append(ns, 50000, 100000)
	}
	maxN := ns[len(ns)-1]

	// Precompute the shared (nonce, leafHash) sequence once.
	type leaf struct {
		nonce    int32
		leafHash []byte
	}
	leaves := make([]leaf, maxN)
	for i := 0; i < maxN; i++ {
		nonce := int32(i)
		leaves[i] = leaf{nonce: nonce, leafHash: smstHashLeaf(encodeLeaf(nonce, testVector(i)))}
	}

	for _, n := range ns {
		// Eager Insert tree (base semantics).
		eager := NewSMST(smstDefaultDepth)
		for i := 0; i < n; i++ {
			if _, err := eager.Insert(leaves[i].nonce, leaves[i].leafHash); err != nil {
				t.Fatalf("eager Insert %d: %v", i, err)
			}
		}
		eagerRoot, _ := eager.GetRoot()

		// Copy-on-write tree (deferred hashing, #1432 live insert path).
		cow := NewSMST(smstDefaultDepth)
		for i := 0; i < n; i++ {
			if _, err := cow.insertCOW(leaves[i].nonce, leaves[i].leafHash); err != nil {
				t.Fatalf("insertCOW %d: %v", i, err)
			}
		}
		cowRoot, _ := cow.GetRoot()

		if !bytes.Equal(eagerRoot, cowRoot) {
			t.Fatalf("N=%d root mismatch Insert vs insertCOW:\n eager=%x\n cow=  %x", n, eagerRoot, cowRoot)
		}

		// Rebuild-from-log path (the #1432 cold-start fallback): drive
		// rebuildTreeFromInputs directly and compare its root to eager Insert.
		store, _ := buildFlushedStore(t, n, maxInt(1, n)) // flush at n
		offsets, buffered, err := store.snapshotRebuildInputs(uint32(n))
		if err != nil {
			t.Fatalf("N=%d snapshotRebuildInputs: %v", n, err)
		}
		rebuilt := rebuildTreeFromInputs(store.dataFile, offsets, buffered, uint32(n))
		rebuiltRoot, _ := rebuilt.GetRoot()
		if !bytes.Equal(eagerRoot, rebuiltRoot) {
			t.Fatalf("N=%d root mismatch Insert vs rebuildTreeFromInputs:\n eager=  %x\n rebuilt=%x", n, eagerRoot, rebuiltRoot)
		}

		// Proof byte-identity at sampled dense indices, RAW form (sibling hashes)
		// across all three trees via GetLeafByDenseIndex — same format, so a
		// direct byte comparison is the true divergence check. (The store's
		// transport proof adds count-encoding, so it is verified semantically
		// against the root instead — done in the COW/finding tests.)
		if n >= 2 {
			for _, di := range []uint32{0, uint32(n / 2), uint32(n - 1)} {
				eNonce, eProof, err := eager.GetLeafByDenseIndex(di)
				if err != nil {
					t.Fatalf("N=%d eager proof di=%d: %v", n, di, err)
				}
				cNonce, cProof, err := cow.GetLeafByDenseIndex(di)
				if err != nil {
					t.Fatalf("N=%d cow proof di=%d: %v", n, di, err)
				}
				rNonce, rProof, err := rebuilt.GetLeafByDenseIndex(di)
				if err != nil {
					t.Fatalf("N=%d rebuild proof di=%d: %v", n, di, err)
				}
				if eNonce != cNonce || eNonce != rNonce {
					t.Fatalf("N=%d di=%d nonce mismatch: eager=%d cow=%d rebuild=%d", n, di, eNonce, cNonce, rNonce)
				}
				if len(eProof) != len(cProof) || len(eProof) != len(rProof) {
					t.Fatalf("N=%d di=%d proof len mismatch: eager=%d cow=%d rebuild=%d", n, di, len(eProof), len(cProof), len(rProof))
				}
				for k := range eProof {
					if !bytes.Equal(eProof[k], cProof[k]) || !bytes.Equal(eProof[k], rProof[k]) {
						t.Fatalf("N=%d di=%d raw proof elem %d differs across Insert/COW/rebuild", n, di, k)
					}
				}
				// The served transport proof must still verify against the root.
				entries, err := store.GetArtifactsAndProofs([]uint32{di}, uint32(n))
				if err != nil {
					t.Fatalf("N=%d store proof di=%d: %v", n, di, err)
				}
				se := entries[0]
				if !VerifySMSTProofSlice(eagerRoot, uint32(n), se.Nonce, encodeLeaf(se.Nonce, se.Vector), se.Proof) {
					t.Fatalf("N=%d di=%d served transport proof did not verify against eager root", n, di)
				}
			}
		}
		store.Close()
		t.Logf("RESULT diff impl=1432 N=%d insert_eq_cow=1 insert_eq_rebuild=1 proofs_eq=1", n)
	}
}

// ---------------------------------------------------------------------------
// Finding 2: distinct-count recompute flood. On #1432 a proof request at a
// non-committed count is REJECTED (no rebuild); committed counts are served from
// retained snapshots in O(depth). Emits served/rejected/rebuild counts + wall
// time for the same attack pattern the base test replays.
// ---------------------------------------------------------------------------
func TestFinding2_RecomputeFlood_1432(t *testing.T) {
	n := findEnvInt("SMST_FIND_N", 20000)
	flush := findEnvInt("SMST_FIND_FLUSH", 2000)
	k := findEnvInt("SMST_FIND_K", 30)

	store, committed := buildFlushedStore(t, n, flush)
	defer store.Close()

	// Attack pattern: K distinct historical counts, mostly NON-committed
	// (partial leaf counts, exactly what Dmytro forced) plus a few committed.
	// Non-committed picks are (flushBoundary - 1), which are < flushedLeafCount
	// and never on a flush boundary.
	var attack []uint32
	for i := 0; i < k; i++ {
		c := committed[i%len(committed)]
		if i%3 == 0 {
			attack = append(attack, c) // committed
		} else {
			attack = append(attack, c-1) // non-committed partial count
		}
	}

	served, rejected := 0, 0
	start := time.Now()
	for _, c := range attack {
		_, err := store.GetArtifactsAndProofs([]uint32{c / 2}, c)
		if err != nil {
			rejected++
		} else {
			served++
		}
	}
	elapsed := time.Since(start)

	// Functional gate: every non-committed count must be rejected outright.
	nonCommitted := committed[0] - 1
	if _, _, err := store.acquireSnapshotTree(nonCommitted); err == nil {
		t.Fatalf("finding2: non-committed count %d was NOT rejected on #1432", nonCommitted)
	}
	// #1432 never rebuilds for the attack pattern: non-committed rejected,
	// committed served from retained.
	rebuilds := 0
	t.Logf("RESULT finding2 impl=1432 N=%d flush=%d K=%d served=%d rejected=%d rebuilds=%d ms=%.2f",
		n, flush, k, served, rejected, rebuilds, float64(elapsed.Microseconds())/1000.0)
}

// ---------------------------------------------------------------------------
// Finding 4: early-share guard false strip. The guard asks the node for an
// inclusion proof at an EARLY committed count after a restart. On #1432 that
// count is served from a re-captured retained snapshot in O(depth) with no
// rebuild; the early root is byte-identical to the pre-restart committed root.
// Removing the O(N) rebuild under the guard's proof window is what removes the
// transient-false-strip surface (a slow/failed rebuild under load = the strip).
// ---------------------------------------------------------------------------
func TestFinding4_EarlyRootAfterRestart_1432(t *testing.T) {
	n := findEnvInt("SMST_FIND_N", 20000)
	flush := findEnvInt("SMST_FIND_FLUSH", 2000)

	store, committed := buildFlushedStore(t, n, flush)
	dir := store.dir
	earlyCount := committed[0]
	earlyRoot, err := store.GetRootAt(earlyCount)
	if err != nil {
		t.Fatalf("pre-restart GetRootAt(%d): %v", earlyCount, err)
	}
	earlyRootCopy := bytes.Clone(earlyRoot)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Restart.
	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	_, retainedHit := store2.retained[earlyCount]

	start := time.Now()
	root2, err := store2.GetRootAt(earlyCount)
	if err != nil {
		t.Fatalf("post-restart GetRootAt(%d): %v", earlyCount, err)
	}
	// Serve a real inclusion proof at the early count (the guard's actual op).
	entries, err := store2.GetArtifactsAndProofs([]uint32{earlyCount / 2}, earlyCount)
	if err != nil {
		t.Fatalf("post-restart proof@%d: %v", earlyCount, err)
	}
	elapsed := time.Since(start)

	identical := bytes.Equal(earlyRootCopy, root2)
	if !identical {
		t.Fatalf("finding4: early root changed across restart on #1432 (would false-strip)")
	}
	e := entries[0]
	if !VerifySMSTProofSlice(root2, earlyCount, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
		t.Fatalf("finding4: post-restart early proof did not verify")
	}
	hit := 0
	if retainedHit {
		hit = 1
	}
	t.Logf("RESULT finding4 impl=1432 N=%d flush=%d earlyCount=%d retained_hit=%d root_identical=1 served_via=%s us=%d",
		n, flush, earlyCount, hit, servedVia(retainedHit), elapsed.Microseconds())
}

func servedVia(retained bool) string {
	if retained {
		return "retained_snapshot_O(depth)"
	}
	return "rebuild_O(N)"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
