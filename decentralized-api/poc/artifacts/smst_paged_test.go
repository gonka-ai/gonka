package artifacts

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const pagedTestVecLen = 24

func pagedTestVector(i int) []byte {
	v := make([]byte, pagedTestVecLen)
	v[0] = byte(i)
	v[1] = byte(i >> 8)
	v[2] = byte(i >> 16)
	return v
}

func openSMSTWithRAMLimit(t *testing.T, dir string, limit uint32) *SMSTArtifactStore {
	t.Helper()
	t.Setenv(envSMSTRAMLeaves, strconv.FormatUint(uint64(limit), 10))
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	return store
}

func fillStore(t *testing.T, store *SMSTArtifactStore, n int, flushEvery int) [][]byte {
	t.Helper()
	var roots [][]byte
	for i := 0; i < n; i++ {
		if err := store.AddWithNode(int32(i), pagedTestVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
		if flushEvery > 0 && (i+1)%flushEvery == 0 {
			if err := store.Flush(); err != nil {
				t.Fatalf("flush at %d: %v", i+1, err)
			}
			roots = append(roots, append([]byte(nil), store.GetRoot()...))
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("final flush: %v", err)
	}
	roots = append(roots, append([]byte(nil), store.GetRoot()...))
	return roots
}

func TestSMSTPagedRootAndProofIdentity(t *testing.T) {
	const n = 5000
	const flushEvery = 500
	const limit = 2000

	ramDir := t.TempDir()
	pagedDir := t.TempDir()

	ram := openSMSTWithRAMLimit(t, ramDir, 1_000_000_000)
	defer ram.Close()
	paged := openSMSTWithRAMLimit(t, pagedDir, limit)
	defer paged.Close()

	ramRoots := fillStore(t, ram, n, flushEvery)
	pagedRoots := fillStore(t, paged, n, flushEvery)

	if !paged.spilled {
		t.Fatal("expected paged store to spill")
	}
	if ram.spilled {
		t.Fatal("control store should stay in RAM")
	}
	if len(ramRoots) != len(pagedRoots) {
		t.Fatalf("root snapshot count ram=%d paged=%d", len(ramRoots), len(pagedRoots))
	}
	for i := range ramRoots {
		if !bytes.Equal(ramRoots[i], pagedRoots[i]) {
			t.Fatalf("root mismatch after flush %d", i+1)
		}
	}
	if !bytes.Equal(ram.GetRoot(), paged.GetRoot()) {
		t.Fatal("live root mismatch")
	}

	compareProofs(t, ram, paged, n, []uint32{0, 1, 17, 4095, 4096, 4097, uint32(n - 1)})
	compareProofsByNonce(t, ram, paged, n, []int32{0, 100, 4095, 4096, 4999})
}

func TestSMSTPagedRecoverIdentity(t *testing.T) {
	const n = 5000
	const limit = 2000

	dir := t.TempDir()
	store := openSMSTWithRAMLimit(t, dir, limit)
	fillStore(t, store, n, 1000)
	root := append([]byte(nil), store.GetRoot()...)
	if !store.spilled {
		t.Fatal("expected spill before close")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, suffixDirName, suffixMarkerName)); err != nil {
		t.Fatalf("missing spill marker: %v", err)
	}

	reopened := openSMSTWithRAMLimit(t, dir, limit)
	defer reopened.Close()
	if !reopened.spilled {
		t.Fatal("reopen should load paged form")
	}
	if reopened.Count() != n {
		t.Fatalf("recovered count %d want %d", reopened.Count(), n)
	}
	if !bytes.Equal(root, reopened.GetRoot()) {
		t.Fatal("recovered root mismatch")
	}

	controlDir := t.TempDir()
	control := openSMSTWithRAMLimit(t, controlDir, 1_000_000_000)
	defer control.Close()
	fillStore(t, control, n, 1000)
	compareProofs(t, control, reopened, n, []uint32{0, 200, 4095, 4096, uint32(n - 1)})

	hist := uint32(2000)
	histRoot, err := reopened.GetRootAt(hist)
	if err != nil {
		t.Fatalf("GetRootAt(%d) after recover: %v", hist, err)
	}
	ctrlHist, err := control.GetRootAt(hist)
	if err != nil {
		t.Fatalf("control GetRootAt(%d): %v", hist, err)
	}
	if !bytes.Equal(histRoot, ctrlHist) {
		t.Fatal("historical root mismatch after paged recover")
	}
	idxs := []uint32{0, 1999}
	got, err := reopened.GetArtifactsAndProofs(idxs, hist)
	if err != nil {
		t.Fatalf("historical proofs after recover: %v", err)
	}
	want, err := control.GetArtifactsAndProofs(idxs, hist)
	if err != nil {
		t.Fatalf("control historical proofs: %v", err)
	}
	if !proofsEqual(want[0].Proof, got[0].Proof) || !proofsEqual(want[1].Proof, got[1].Proof) {
		t.Fatal("historical proof mismatch after paged recover")
	}
}

func TestSMSTPagedHistoricalProofs(t *testing.T) {
	const limit = 2000
	dir := t.TempDir()
	store := openSMSTWithRAMLimit(t, dir, limit)
	defer store.Close()

	for i := 0; i < 2000; i++ {
		if err := store.AddWithNode(int32(i), pagedTestVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if !store.spilled {
		t.Fatal("expected spill at 2000")
	}
	early := store.Count()
	earlyRoot := append([]byte(nil), store.GetRoot()...)

	for i := 2000; i < 4500; i++ {
		if err := store.AddWithNode(int32(i), pagedTestVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetRootAt(early)
	if err != nil {
		t.Fatalf("GetRootAt(%d): %v", early, err)
	}
	if !bytes.Equal(earlyRoot, got) {
		t.Fatal("historical root changed after later inserts")
	}

	controlDir := t.TempDir()
	control := openSMSTWithRAMLimit(t, controlDir, 1_000_000_000)
	defer control.Close()
	for i := 0; i < 2000; i++ {
		if err := control.AddWithNode(int32(i), pagedTestVector(i), "n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := control.Flush(); err != nil {
		t.Fatal(err)
	}

	idxs := []uint32{0, 1, 500, 1999}
	pagedEntries, err := store.GetArtifactsAndProofs(idxs, early)
	if err != nil {
		t.Fatalf("paged historical proofs: %v", err)
	}
	ramEntries, err := control.GetArtifactsAndProofs(idxs, early)
	if err != nil {
		t.Fatalf("ram historical proofs: %v", err)
	}
	if len(pagedEntries) != len(ramEntries) {
		t.Fatalf("entry count %d vs %d", len(pagedEntries), len(ramEntries))
	}
	for i := range ramEntries {
		if ramEntries[i].Nonce != pagedEntries[i].Nonce {
			t.Fatalf("nonce mismatch at %d: %d vs %d", i, ramEntries[i].Nonce, pagedEntries[i].Nonce)
		}
		if !proofsEqual(ramEntries[i].Proof, pagedEntries[i].Proof) {
			t.Fatalf("historical proof mismatch at dense %d nonce %d", idxs[i], ramEntries[i].Nonce)
		}
		if !VerifySMSTProofSlice(earlyRoot, early, pagedEntries[i].Nonce, encodeLeaf(pagedEntries[i].Nonce, pagedEntries[i].Vector), pagedEntries[i].Proof) {
			t.Fatalf("paged historical proof failed verify at %d", idxs[i])
		}
	}
}

func TestSMSTPagedDuplicateRejected(t *testing.T) {
	store := openSMSTWithRAMLimit(t, t.TempDir(), 64)
	defer store.Close()
	for i := 0; i < 64; i++ {
		if err := store.AddWithNode(int32(i), pagedTestVector(i), "n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if !store.spilled {
		t.Fatal("expected spill")
	}
	if err := store.AddWithNode(3, pagedTestVector(3), "n"); err != ErrDuplicateNonce {
		t.Fatalf("expected ErrDuplicateNonce, got %v", err)
	}
	if err := store.AddWithNode(64, pagedTestVector(64), "n"); err != nil {
		t.Fatalf("new nonce after spill: %v", err)
	}
}

func TestSMSTPagedEarlyGuardNonSequential(t *testing.T) {
	const (
		limit      = 2000
		earlyN     = 2500
		totalN     = 8000
		flushEvery = 500
	)
	nonces := make([]int32, totalN)
	for i := range nonces {
		nonces[i] = int32(i)
	}
	// Shuffle so later inserts land in suffixes that already have early leaves.
	for i := len(nonces) - 1; i > 0; i-- {
		j := int(uint32(i*1103515245+12345) % uint32(i+1))
		nonces[i], nonces[j] = nonces[j], nonces[i]
	}

	addN := func(t *testing.T, store *SMSTArtifactStore, from, to int) {
		t.Helper()
		for i := from; i < to; i++ {
			n := nonces[i]
			if err := store.AddWithNode(n, pagedTestVector(int(n)), "n"); err != nil {
				t.Fatalf("add nonce %d at seq %d: %v", n, i, err)
			}
			if (i+1)%flushEvery == 0 {
				if err := store.Flush(); err != nil {
					t.Fatalf("flush at %d: %v", i+1, err)
				}
			}
		}
		if err := store.Flush(); err != nil {
			t.Fatal(err)
		}
	}

	ram := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000_000)
	defer ram.Close()
	pagedDir := t.TempDir()
	paged := openSMSTWithRAMLimit(t, pagedDir, limit)
	addN(t, ram, 0, earlyN)
	addN(t, paged, 0, earlyN)
	if !paged.spilled {
		t.Fatal("expected spill before early commit")
	}
	early := paged.Count()
	if early != earlyN {
		t.Fatalf("early count %d want %d", early, earlyN)
	}
	earlyRoot := append([]byte(nil), paged.GetRoot()...)
	if !bytes.Equal(earlyRoot, ram.GetRoot()) {
		t.Fatal("early root mismatch vs RAM control")
	}

	addN(t, ram, earlyN, totalN)
	addN(t, paged, earlyN, totalN)
	if paged.Count() != totalN || ram.Count() != totalN {
		t.Fatalf("tip count paged=%d ram=%d want %d", paged.Count(), ram.Count(), totalN)
	}

	gotEarly, err := paged.GetRootAt(early)
	if err != nil {
		t.Fatalf("GetRootAt(%d): %v", early, err)
	}
	if !bytes.Equal(earlyRoot, gotEarly) {
		t.Fatal("early root changed after later non-sequential inserts")
	}

	idxs := []uint32{0, 1, early / 2, early - 1}
	compareProofsAt(t, ram, paged, early, earlyRoot, idxs)

	earlyNonce := nonces[0]
	lateNonce := nonces[earlyN]
	compareProofsByNonceAt(t, ram, paged, early, []int32{earlyNonce, nonces[17], nonces[early-1]})
	lateHits, err := paged.GetArtifactsAndProofsByNonce([]int32{lateNonce}, early)
	if err != nil {
		t.Fatalf("late by-nonce at early count: %v", err)
	}
	if len(lateHits) != 0 {
		t.Fatalf("late nonce %d must not be included at early count %d", lateNonce, early)
	}

	if err := paged.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := openSMSTWithRAMLimit(t, pagedDir, limit)
	defer reopened.Close()
	root2, err := reopened.GetRootAt(early)
	if err != nil {
		t.Fatalf("post-restart GetRootAt(%d): %v", early, err)
	}
	if !bytes.Equal(earlyRoot, root2) {
		t.Fatal("early root changed across restart")
	}
	entries, err := reopened.GetArtifactsAndProofs([]uint32{early / 2}, early)
	if err != nil {
		t.Fatalf("post-restart early proof: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("post-restart early proof entries %d", len(entries))
	}
	e := entries[0]
	if !VerifySMSTProofSlice(root2, early, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
		t.Fatal("post-restart early proof did not verify")
	}
	compareProofsAt(t, ram, reopened, early, earlyRoot, idxs)
	lateHits, err = reopened.GetArtifactsAndProofsByNonce([]int32{lateNonce}, early)
	if err != nil {
		t.Fatal(err)
	}
	if len(lateHits) != 0 {
		t.Fatalf("post-restart late nonce %d included at early count", lateNonce)
	}
}

func compareProofsAt(t *testing.T, ram, paged *SMSTArtifactStore, count uint32, root []byte, idxs []uint32) {
	t.Helper()
	ramEntries, err := ram.GetArtifactsAndProofs(idxs, count)
	if err != nil {
		t.Fatalf("ram proofs@%d: %v", count, err)
	}
	pagedEntries, err := paged.GetArtifactsAndProofs(idxs, count)
	if err != nil {
		t.Fatalf("paged proofs@%d: %v", count, err)
	}
	if len(ramEntries) != len(pagedEntries) {
		t.Fatalf("proof count ram=%d paged=%d", len(ramEntries), len(pagedEntries))
	}
	for i := range ramEntries {
		if ramEntries[i].Nonce != pagedEntries[i].Nonce || ramEntries[i].DenseIndex != pagedEntries[i].DenseIndex {
			t.Fatalf("index/nonce mismatch at %d: ram (%d,%d) paged (%d,%d)",
				i, ramEntries[i].DenseIndex, ramEntries[i].Nonce, pagedEntries[i].DenseIndex, pagedEntries[i].Nonce)
		}
		if !bytes.Equal(ramEntries[i].Vector, pagedEntries[i].Vector) || !proofsEqual(ramEntries[i].Proof, pagedEntries[i].Proof) {
			t.Fatalf("early proof mismatch nonce %d dense %d", ramEntries[i].Nonce, idxs[i])
		}
		if !VerifySMSTProofSlice(root, count, pagedEntries[i].Nonce, encodeLeaf(pagedEntries[i].Nonce, pagedEntries[i].Vector), pagedEntries[i].Proof) {
			t.Fatalf("early proof failed verify nonce %d", pagedEntries[i].Nonce)
		}
	}
}

func compareProofsByNonceAt(t *testing.T, ram, paged *SMSTArtifactStore, count uint32, nonces []int32) {
	t.Helper()
	ramEntries, err := ram.GetArtifactsAndProofsByNonce(nonces, count)
	if err != nil {
		t.Fatalf("ram by-nonce@%d: %v", count, err)
	}
	pagedEntries, err := paged.GetArtifactsAndProofsByNonce(nonces, count)
	if err != nil {
		t.Fatalf("paged by-nonce@%d: %v", count, err)
	}
	if len(ramEntries) != len(pagedEntries) {
		t.Fatalf("by-nonce count ram=%d paged=%d", len(ramEntries), len(pagedEntries))
	}
	ramByNonce := map[int32]ProofEntry{}
	for _, e := range ramEntries {
		ramByNonce[e.Nonce] = e
	}
	for _, e := range pagedEntries {
		re, ok := ramByNonce[e.Nonce]
		if !ok {
			t.Fatalf("paged extra nonce %d at count %d", e.Nonce, count)
		}
		if re.DenseIndex != e.DenseIndex || !proofsEqual(re.Proof, e.Proof) {
			t.Fatalf("by-nonce mismatch nonce %d at count %d", e.Nonce, count)
		}
	}
}

func TestSMSTPagedUncommittedSnapshotRejected(t *testing.T) {
	store := openSMSTWithRAMLimit(t, t.TempDir(), 32)
	defer store.Close()
	for i := 0; i < 32; i++ {
		if err := store.AddWithNode(int32(i), pagedTestVector(i), "n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWithNode(32, pagedTestVector(32), "n"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetArtifactsAndProofs([]uint32{0}, 16); err == nil {
		t.Fatal("expected reject of uncommitted snapshot count")
	}
}

func compareProofs(t *testing.T, ram, paged *SMSTArtifactStore, n int, idxs []uint32) {
	t.Helper()
	count := uint32(n)
	root := ram.GetRoot()
	ramEntries, err := ram.GetArtifactsAndProofs(idxs, count)
	if err != nil {
		t.Fatalf("ram proofs: %v", err)
	}
	pagedEntries, err := paged.GetArtifactsAndProofs(idxs, count)
	if err != nil {
		t.Fatalf("paged proofs: %v", err)
	}
	if len(ramEntries) != len(pagedEntries) {
		t.Fatalf("proof count ram=%d paged=%d", len(ramEntries), len(pagedEntries))
	}
	for i := range ramEntries {
		if ramEntries[i].DenseIndex != pagedEntries[i].DenseIndex || ramEntries[i].Nonce != pagedEntries[i].Nonce {
			t.Fatalf("index/nonce mismatch at %d: ram (%d,%d) paged (%d,%d)",
				i, ramEntries[i].DenseIndex, ramEntries[i].Nonce, pagedEntries[i].DenseIndex, pagedEntries[i].Nonce)
		}
		if !bytes.Equal(ramEntries[i].Vector, pagedEntries[i].Vector) {
			t.Fatalf("vector mismatch nonce %d", ramEntries[i].Nonce)
		}
		if !proofsEqual(ramEntries[i].Proof, pagedEntries[i].Proof) {
			t.Fatalf("proof bytes mismatch nonce %d dense %d (ram len %d paged len %d)",
				ramEntries[i].Nonce, ramEntries[i].DenseIndex, len(ramEntries[i].Proof), len(pagedEntries[i].Proof))
		}
		if !VerifySMSTProofSlice(root, count, pagedEntries[i].Nonce, encodeLeaf(pagedEntries[i].Nonce, pagedEntries[i].Vector), pagedEntries[i].Proof) {
			t.Fatalf("paged proof failed verify nonce %d", pagedEntries[i].Nonce)
		}
	}
}

func compareProofsByNonce(t *testing.T, ram, paged *SMSTArtifactStore, n int, nonces []int32) {
	t.Helper()
	count := uint32(n)
	ramEntries, err := ram.GetArtifactsAndProofsByNonce(nonces, count)
	if err != nil {
		t.Fatalf("ram by-nonce: %v", err)
	}
	pagedEntries, err := paged.GetArtifactsAndProofsByNonce(nonces, count)
	if err != nil {
		t.Fatalf("paged by-nonce: %v", err)
	}
	if len(ramEntries) != len(pagedEntries) {
		t.Fatalf("by-nonce count ram=%d paged=%d", len(ramEntries), len(pagedEntries))
	}
	ramByNonce := map[int32]ProofEntry{}
	for _, e := range ramEntries {
		ramByNonce[e.Nonce] = e
	}
	for _, e := range pagedEntries {
		re, ok := ramByNonce[e.Nonce]
		if !ok {
			t.Fatalf("paged extra nonce %d", e.Nonce)
		}
		if re.DenseIndex != e.DenseIndex {
			t.Fatalf("dense index mismatch nonce %d: ram %d paged %d", e.Nonce, re.DenseIndex, e.DenseIndex)
		}
		if !proofsEqual(re.Proof, e.Proof) {
			t.Fatalf("by-nonce proof mismatch nonce %d", e.Nonce)
		}
	}
}

func proofsEqual(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

