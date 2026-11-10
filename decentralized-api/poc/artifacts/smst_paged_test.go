package artifacts

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

func TestSMSTPagedNegativeNonceIdentity(t *testing.T) {
	nonces := []int32{-1, -2, -1000000, 1 << 25, (1 << 25) + 1, 1 << 28}
	for i := 0; i < 40; i++ {
		nonces = append(nonces, int32(i))
	}
	ram := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000_000)
	defer ram.Close()
	paged := openSMSTWithRAMLimit(t, t.TempDir(), 16)
	defer paged.Close()
	for i, nonce := range nonces {
		if err := ram.AddWithNode(nonce, pagedTestVector(int(nonce)), "n"); err != nil {
			t.Fatalf("ram add %d: %v", nonce, err)
		}
		if err := paged.AddWithNode(nonce, pagedTestVector(int(nonce)), "n"); err != nil {
			t.Fatalf("paged add %d: %v", nonce, err)
		}
		if (i+1)%16 == 0 {
			if err := ram.Flush(); err != nil {
				t.Fatal(err)
			}
			if err := paged.Flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !paged.spilled {
		t.Fatal("expected spill")
	}
	if !bytes.Equal(ram.GetRoot(), paged.GetRoot()) {
		t.Fatal("root mismatch with negative/expanded nonces")
	}
	compareProofsByNonce(t, ram, paged, len(nonces), []int32{-1, -2, 1 << 25, 0, 39})
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
	for i := range ramEntries {
		if ramEntries[i].Nonce != pagedEntries[i].Nonce || ramEntries[i].DenseIndex != pagedEntries[i].DenseIndex {
			t.Fatalf("by-nonce order/index mismatch at %d count %d", i, count)
		}
		if !proofsEqual(ramEntries[i].Proof, pagedEntries[i].Proof) {
			t.Fatalf("by-nonce mismatch nonce %d at count %d", ramEntries[i].Nonce, count)
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
	for i := range ramEntries {
		if ramEntries[i].Nonce != pagedEntries[i].Nonce || ramEntries[i].DenseIndex != pagedEntries[i].DenseIndex {
			t.Fatalf("by-nonce order/index mismatch at %d: ram (%d,%d) paged (%d,%d)",
				i, ramEntries[i].DenseIndex, ramEntries[i].Nonce, pagedEntries[i].DenseIndex, pagedEntries[i].Nonce)
		}
		if !proofsEqual(ramEntries[i].Proof, pagedEntries[i].Proof) {
			t.Fatalf("by-nonce proof mismatch nonce %d", ramEntries[i].Nonce)
		}
	}
}

func TestSMSTPagedRecoverMissingSeal(t *testing.T) {
	dir := t.TempDir()
	store := openSMSTWithRAMLimit(t, dir, 64)
	fillStore(t, store, 128, 64)
	root := append([]byte(nil), store.GetRoot()...)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, suffixDirName))
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".seal" {
			if err := os.Remove(filepath.Join(dir, suffixDirName, e.Name())); err != nil {
				t.Fatal(err)
			}
			removed = true
			break
		}
	}
	if !removed {
		t.Fatal("no seal file to remove")
	}
	reopened := openSMSTWithRAMLimit(t, dir, 64)
	defer reopened.Close()
	if !bytes.Equal(root, reopened.GetRoot()) {
		t.Fatal("root changed after missing-seal recover")
	}
	if _, _, _, err := reopened.GetArtifactAndProof(0, reopened.Count()); err != nil {
		t.Fatalf("proof after missing-seal recover: %v", err)
	}
}

func TestSMSTPagedRecoverOrphanData(t *testing.T) {
	dir := t.TempDir()
	store := openSMSTWithRAMLimit(t, dir, 64)
	fillStore(t, store, 128, 64)
	root := append([]byte(nil), store.GetRoot()...)
	count := store.Count()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "artifacts.data"), os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0xFF}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openSMSTWithRAMLimit(t, dir, 64)
	defer reopened.Close()
	if reopened.Count() != count {
		t.Fatalf("count %d want %d after orphan recover", reopened.Count(), count)
	}
	if !bytes.Equal(root, reopened.GetRoot()) {
		t.Fatal("root changed after orphan recover")
	}
	entries, err := reopened.GetArtifactsAndProofs([]uint32{0, count - 1}, count)
	if err != nil {
		t.Fatalf("proofs after orphan recover: %v", err)
	}
	for _, e := range entries {
		if !VerifySMSTProofSlice(root, count, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
			t.Fatalf("proof failed after orphan recover nonce %d", e.Nonce)
		}
	}
}

func TestSMSTPagedRespillWithoutMarker(t *testing.T) {
	dir := t.TempDir()
	store := openSMSTWithRAMLimit(t, dir, 64)
	fillStore(t, store, 128, 64)
	if !store.spilled {
		t.Fatal("expected spill")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, suffixDirName, suffixMarkerName)); err != nil {
		t.Fatal(err)
	}
	reopened := openSMSTWithRAMLimit(t, dir, 64)
	defer reopened.Close()
	if !reopened.spilled {
		t.Fatal("expected re-spill after missing marker")
	}
	if reopened.Count() != 128 {
		t.Fatalf("count %d want 128", reopened.Count())
	}
	if _, _, _, err := reopened.GetArtifactAndProof(10, 128); err != nil {
		t.Fatalf("proof after re-spill: %v", err)
	}
}

func TestSMSTPagedByNoncePreservesRequestOrder(t *testing.T) {
	store := openSMSTWithRAMLimit(t, t.TempDir(), 32)
	defer store.Close()
	fillStore(t, store, 64, 32)
	nonces := []int32{50, 3, 40, 3, 20}
	entries, err := store.GetArtifactsAndProofsByNonce(nonces, store.Count())
	if err != nil {
		t.Fatal(err)
	}
	want := []int32{50, 3, 40, 3, 20}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries want %d", len(entries), len(want))
	}
	for i, e := range entries {
		if e.Nonce != want[i] {
			t.Fatalf("order[%d]=%d want %d", i, e.Nonce, want[i])
		}
	}
}

func TestSMSTPagedConcurrentIngestAndProof(t *testing.T) {
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
	early := store.Count()
	root := append([]byte(nil), store.GetRoot()...)

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				idx := uint32((id*11 + i) % int(early))
				entries, err := store.GetArtifactsAndProofs([]uint32{idx}, early)
				if err != nil {
					errCh <- err
					return
				}
				e := entries[0]
				if !VerifySMSTProofSlice(root, early, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
					errCh <- errors.New("historical proof failed verify")
					return
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 64; i < 192; i++ {
			if err := store.AddWithNode(int32(i), pagedTestVector(i), "n"); err != nil {
				errCh <- err
				return
			}
			if (i+1)%32 == 0 {
				if err := store.Flush(); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSMSTPagedHasNonceConsultsSuffix(t *testing.T) {
	store := openSMSTWithRAMLimit(t, t.TempDir(), 32)
	defer store.Close()
	fillStore(t, store, 64, 32)
	if !store.spilled {
		t.Fatal("expected spill")
	}
	if !store.smst.HasNonce(0) || !store.smst.HasNonce(63) {
		t.Fatal("live spilled tree must see committed nonces")
	}
	if store.smst.HasNonce(64) || store.smst.HasNonce(-1) {
		t.Fatal("live spilled tree reported a missing nonce")
	}

	for i := 64; i < 80; i++ {
		if err := store.AddWithNode(int32(i), pagedTestVector(i), "n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	store.mu.RLock()
	view, ok := store.retainedSnapshotViewLocked(64)
	if !ok {
		store.mu.RUnlock()
		t.Fatal("missing retained snapshot at 64")
	}
	if !view.HasNonce(0) || !view.HasNonce(63) {
		store.mu.RUnlock()
		t.Fatal("historical view missed a nonce present at count 64")
	}
	if view.HasNonce(70) {
		store.mu.RUnlock()
		t.Fatal("historical view saw a nonce added after count 64")
	}
	idx, err := store.smst.denseIndexForNonce(17)
	if err != nil || idx != 17 {
		store.mu.RUnlock()
		t.Fatalf("live denseIndex(17)=%d err=%v", idx, err)
	}
	hist, err := view.denseIndexForNonce(17)
	if err != nil || hist != 17 {
		store.mu.RUnlock()
		t.Fatalf("snapshot denseIndex(17)=%d err=%v", hist, err)
	}
	if _, err := view.denseIndexForNonce(70); err == nil {
		store.mu.RUnlock()
		t.Fatal("snapshot denseIndex saw a nonce added after count 64")
	}
	store.mu.RUnlock()
}

func TestSMSTPagedHotMissReloadsSuffix(t *testing.T) {
	ram := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000_000)
	defer ram.Close()
	paged := openSMSTWithRAMLimit(t, t.TempDir(), 8)
	defer paged.Close()

	// Fill prefix 0 past the RAM cap so the store spills, then Flush drops the
	// hot suffix. The next inserts into prefix 0 must reload from the log.
	for i := 0; i < 16; i++ {
		nonce := int32(i)
		if err := ram.AddWithNode(nonce, pagedTestVector(int(nonce)), "n"); err != nil {
			t.Fatal(err)
		}
		if err := paged.AddWithNode(nonce, pagedTestVector(int(nonce)), "n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !paged.spilled {
		t.Fatal("expected spill")
	}
	for i := 16; i < 24; i++ {
		nonce := int32(i)
		if err := ram.AddWithNode(nonce, pagedTestVector(int(nonce)), "n"); err != nil {
			t.Fatal(err)
		}
		if err := paged.AddWithNode(nonce, pagedTestVector(int(nonce)), "n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ram.GetRoot(), paged.GetRoot()) {
		t.Fatal("root mismatch after hot reload")
	}
	compareProofsByNonce(t, ram, paged, 24, []int32{0, 8, 16, 23})
}

// TestSMSTPagedInterleavedIngest walks 40 suffixes round-robin, which is more
// than the old FIFO cap of 32. Roots must stay identical to the RAM store.
func TestSMSTPagedInterleavedIngest(t *testing.T) {
	const (
		prefixes   = 40
		rounds     = 80
		limit      = 64
		flushEvery = 200
	)
	ram := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000_000)
	defer ram.Close()
	paged := openSMSTWithRAMLimit(t, t.TempDir(), limit)
	defer paged.Close()

	n := 0
	for round := 0; round < rounds; round++ {
		for p := 0; p < prefixes; p++ {
			nonce := int32(p<<smstSuffixHeight) + int32(round)
			if err := ram.AddWithNode(nonce, pagedTestVector(int(nonce)), "n"); err != nil {
				t.Fatal(err)
			}
			if err := paged.AddWithNode(nonce, pagedTestVector(int(nonce)), "n"); err != nil {
				t.Fatal(err)
			}
			n++
			if n%flushEvery == 0 {
				if err := ram.Flush(); err != nil {
					t.Fatal(err)
				}
				if err := paged.Flush(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !paged.spilled {
		t.Fatal("expected spill")
	}
	if !bytes.Equal(ram.GetRoot(), paged.GetRoot()) {
		t.Fatal("root mismatch after interleaved ingest")
	}
	compareProofs(t, ram, paged, n, []uint32{0, 1, uint32(n / 2), uint32(n - 1)})
}

func TestSMSTPagedReopenProofsFromLog(t *testing.T) {
	dir := t.TempDir()
	store := openSMSTWithRAMLimit(t, dir, 32)
	fillStore(t, store, 64, 32)
	root := append([]byte(nil), store.GetRoot()...)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openSMSTWithRAMLimit(t, dir, 32)
	defer reopened.Close()
	if !bytes.Equal(root, reopened.GetRoot()) {
		t.Fatal("root mismatch after reopen without tree blobs")
	}
	entries2, err := reopened.GetArtifactsAndProofs([]uint32{0, 32, 63}, 64)
	if err != nil {
		t.Fatalf("cold proofs from suffix log: %v", err)
	}
	for _, e := range entries2 {
		if !VerifySMSTProofSlice(root, 64, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
			t.Fatalf("blob proof failed nonce %d", e.Nonce)
		}
	}
}

func TestSMSTPagedLeftoverLogRebuild(t *testing.T) {
	dir := t.TempDir()
	store := openSMSTWithRAMLimit(t, dir, 32)
	fillStore(t, store, 64, 32)
	root := append([]byte(nil), store.GetRoot()...)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	leftover := filepath.Join(dir, suffixDirName, "00ffffff.log")
	var rec [8]byte
	binary.LittleEndian.PutUint32(rec[0:4], 1)
	binary.LittleEndian.PutUint32(rec[4:8], 0)
	if err := os.WriteFile(leftover, rec[:], 0644); err != nil {
		t.Fatal(err)
	}

	reopened := openSMSTWithRAMLimit(t, dir, 32)
	defer reopened.Close()
	if !bytes.Equal(root, reopened.GetRoot()) {
		t.Fatal("root changed after leftover-log recover")
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatal("leftover suffix log should be removed after rebuild")
	}
}

func TestPagedDeferredHashFingerprint(t *testing.T) {
	nonces := []int32{1 << 25, (1 << 25) + 1, 1 << 28, -1, -2, -1000000}
	for i := 0; i < 80; i++ {
		nonces = append(nonces, int32(i))
	}

	ram := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000_000)
	defer ram.Close()
	paged := openSMSTWithRAMLimit(t, t.TempDir(), 20)
	defer paged.Close()

	const flushEvery = 20
	for i, nonce := range nonces {
		if err := ram.AddWithNode(nonce, testVector(int(nonce)), "n"); err != nil {
			t.Fatalf("ram add %d: %v", nonce, err)
		}
		if err := paged.AddWithNode(nonce, testVector(int(nonce)), "n"); err != nil {
			t.Fatalf("paged add %d: %v", nonce, err)
		}
		if (i+1)%flushEvery != 0 {
			continue
		}
		if err := ram.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := paged.Flush(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ram.GetRoot(), paged.GetRoot()) {
			t.Fatalf("root mismatch after %d inserts", i+1)
		}
	}
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !paged.spilled {
		t.Fatal("expected paged store to spill")
	}
	if ram.spilled {
		t.Fatal("RAM control should not spill")
	}
	if !bytes.Equal(ram.GetRoot(), paged.GetRoot()) {
		t.Fatal("final root mismatch")
	}

	n := len(nonces)
	compareProofs(t, ram, paged, n, []uint32{0, 1, uint32(n / 2), uint32(n - 1)})
	compareProofsByNonce(t, ram, paged, n, []int32{-1, -2, 1 << 25, 0, 79})
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

func addPrefixed(t *testing.T, store *SMSTArtifactStore, prefixes int, perPrefix int, fromLocal int) {
	t.Helper()
	for local := fromLocal; local < fromLocal+perPrefix; local++ {
		for p := 0; p < prefixes; p++ {
			nonce := int32(p<<smstSuffixHeight) + int32(local)
			if err := store.AddWithNode(nonce, pagedTestVector(int(nonce)), "n"); err != nil {
				t.Fatalf("add nonce %d: %v", nonce, err)
			}
		}
	}
}

func dropLastJournalLine(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, suffixDirName, suffixHistoryName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := bytes.TrimRight(data, "\n")
	idx := bytes.LastIndexByte(trimmed, '\n')
	if idx < 0 {
		t.Fatal("journal has fewer than two lines")
	}
	if err := os.WriteFile(path, trimmed[:idx+1], 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSMSTPagedJournalGapRecover(t *testing.T) {
	const (
		limit    = 8
		prefixes = 4
	)
	ram := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000_000)
	defer ram.Close()
	pagedDir := t.TempDir()
	paged := openSMSTWithRAMLimit(t, pagedDir, limit)

	addPrefixed(t, ram, prefixes, 1, 0)
	addPrefixed(t, paged, prefixes, 1, 0)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	early := paged.Count()
	earlyRoot := append([]byte(nil), paged.GetRoot()...)

	addPrefixed(t, ram, prefixes, 1, 1)
	addPrefixed(t, paged, prefixes, 1, 1)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !paged.spilled {
		t.Fatal("expected spill")
	}

	addPrefixed(t, ram, prefixes, 1, 2)
	addPrefixed(t, paged, prefixes, 1, 2)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	tip := paged.Count()
	tipRoot := append([]byte(nil), paged.GetRoot()...)
	if err := paged.Close(); err != nil {
		t.Fatal(err)
	}

	dropLastJournalLine(t, pagedDir)
	reopened := openSMSTWithRAMLimit(t, pagedDir, limit)
	defer reopened.Close()
	if reopened.Count() != tip {
		t.Fatalf("recovered count %d want %d", reopened.Count(), tip)
	}
	if !bytes.Equal(tipRoot, reopened.GetRoot()) {
		t.Fatal("tip root mismatch after journal-gap recover")
	}
	gotEarly, err := reopened.GetRootAt(early)
	if err != nil {
		t.Fatalf("GetRootAt(%d): %v", early, err)
	}
	if !bytes.Equal(earlyRoot, gotEarly) {
		t.Fatal("early root mismatch after journal-gap recover")
	}
	compareProofsAt(t, ram, reopened, early, earlyRoot, []uint32{0, early - 1})
	compareProofsAt(t, ram, reopened, tip, tipRoot, []uint32{0, tip - 1})
}

func flipJournalHashNibble(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, suffixDirName, suffixHistoryName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(data, []byte(`"h":"`))
	if idx < 0 {
		t.Fatal("journal has no hash field")
	}
	pos := idx + len(`"h":"`)
	if pos >= len(data) {
		t.Fatal("journal hash field is empty")
	}
	if data[pos] == '0' {
		data[pos] = '1'
	} else {
		data[pos] = '0'
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSMSTPagedJournalRootMismatchRecover(t *testing.T) {
	const (
		limit    = 8
		prefixes = 4
	)
	ram := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000_000)
	defer ram.Close()
	pagedDir := t.TempDir()
	paged := openSMSTWithRAMLimit(t, pagedDir, limit)

	addPrefixed(t, ram, prefixes, 1, 0)
	addPrefixed(t, paged, prefixes, 1, 0)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	early := paged.Count()
	earlyRoot := append([]byte(nil), paged.GetRoot()...)

	addPrefixed(t, ram, prefixes, 1, 1)
	addPrefixed(t, paged, prefixes, 1, 1)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !paged.spilled {
		t.Fatal("expected spill")
	}
	tip := paged.Count()
	tipRoot := append([]byte(nil), paged.GetRoot()...)
	if err := paged.Close(); err != nil {
		t.Fatal(err)
	}

	flipJournalHashNibble(t, pagedDir)
	reopened := openSMSTWithRAMLimit(t, pagedDir, limit)
	defer reopened.Close()
	if !bytes.Equal(tipRoot, reopened.GetRoot()) {
		t.Fatal("tip root mismatch after journal hash flip")
	}
	gotEarly, err := reopened.GetRootAt(early)
	if err != nil {
		t.Fatalf("GetRootAt(%d): %v", early, err)
	}
	if !bytes.Equal(earlyRoot, gotEarly) {
		t.Fatal("early root mismatch after journal hash flip")
	}
	compareProofsAt(t, ram, reopened, early, earlyRoot, []uint32{0, early - 1})
	compareProofsAt(t, ram, reopened, tip, tipRoot, []uint32{0, tip - 1})
}

func TestSMSTPagedFlushPersistRetry(t *testing.T) {
	const limit = 8
	ram := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000_000)
	defer ram.Close()
	pagedDir := t.TempDir()
	paged := openSMSTWithRAMLimit(t, pagedDir, limit)
	defer paged.Close()

	addPrefixed(t, ram, 4, 2, 0)
	addPrefixed(t, paged, 4, 2, 0)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !paged.spilled {
		t.Fatal("expected spill")
	}

	addPrefixed(t, ram, 4, 1, 2)
	addPrefixed(t, paged, 4, 1, 2)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	blocked := blockSuffixSeals(t, pagedDir)
	err := paged.Flush()
	unblockSuffixSeals(t, blocked)
	if err == nil {
		t.Fatal("expected suffix persist error")
	}
	flushed, _ := paged.GetFlushedRoot()
	if flushed != paged.Count() {
		t.Fatalf("flushed count %d want %d after persist error", flushed, paged.Count())
	}

	addPrefixed(t, ram, 4, 1, 3)
	addPrefixed(t, paged, 4, 1, 3)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ram.GetRoot(), paged.GetRoot()) {
		t.Fatal("root mismatch after persist retry")
	}
	tip := paged.Count()
	tipRoot := append([]byte(nil), paged.GetRoot()...)
	if err := paged.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openSMSTWithRAMLimit(t, pagedDir, limit)
	defer reopened.Close()
	if !bytes.Equal(tipRoot, reopened.GetRoot()) {
		t.Fatal("reopen root mismatch after persist retry")
	}
	entries, err := reopened.GetArtifactsAndProofs([]uint32{0, tip - 1}, tip)
	if err != nil {
		t.Fatalf("reopen proofs: %v", err)
	}
	for _, e := range entries {
		if !VerifySMSTProofSlice(tipRoot, tip, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
			t.Fatalf("reopen proof failed nonce %d", e.Nonce)
		}
	}
}

func TestSMSTPagedSpillPersistRetry(t *testing.T) {
	const limit = 8
	ram := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000_000)
	defer ram.Close()
	pagedDir := t.TempDir()
	paged := openSMSTWithRAMLimit(t, pagedDir, limit)
	defer paged.Close()

	if err := os.MkdirAll(filepath.Join(pagedDir, suffixDirName), 0755); err != nil {
		t.Fatal(err)
	}
	addPrefixed(t, ram, 4, 2, 0)
	addPrefixed(t, paged, 4, 2, 0)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(pagedDir, suffixDirName), 0555); err != nil {
		t.Fatal(err)
	}
	err := paged.Flush()
	if err := os.Chmod(filepath.Join(pagedDir, suffixDirName), 0755); err != nil {
		t.Fatal(err)
	}
	if err == nil {
		t.Fatal("expected spill persist error")
	}
	if paged.spilled {
		t.Fatal("spill must not flip in-memory state before the marker")
	}

	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !paged.spilled {
		t.Fatal("expected spill on retry")
	}
	if !bytes.Equal(ram.GetRoot(), paged.GetRoot()) {
		t.Fatal("root mismatch after spill retry")
	}
}

func TestSMSTPagedCloseWithBuffer(t *testing.T) {
	dir := t.TempDir()
	store := openSMSTWithRAMLimit(t, dir, 8)
	addPrefixed(t, store, 4, 2, 0)
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if !store.spilled {
		t.Fatal("expected spill")
	}
	addPrefixed(t, store, 4, 1, 2)
	root := append([]byte(nil), store.GetRoot()...)
	count := store.Count()
	if err := store.Close(); err != nil {
		t.Fatalf("close with buffer: %v", err)
	}
	reopened := openSMSTWithRAMLimit(t, dir, 8)
	defer reopened.Close()
	if reopened.Count() != count {
		t.Fatalf("reopen count %d want %d", reopened.Count(), count)
	}
	if !bytes.Equal(root, reopened.GetRoot()) {
		t.Fatal("reopen root mismatch after close with buffer")
	}
}

func TestSMSTPagedLogFlushRetry(t *testing.T) {
	const (
		limit    = 8
		prefixes = 4
	)
	ram := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000_000)
	defer ram.Close()
	pagedDir := t.TempDir()
	paged := openSMSTWithRAMLimit(t, pagedDir, limit)
	defer paged.Close()

	addPrefixed(t, ram, prefixes, 2, 0)
	addPrefixed(t, paged, prefixes, 2, 0)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !paged.spilled {
		t.Fatal("expected spill")
	}
	early := paged.Count()
	earlyRoot := append([]byte(nil), paged.GetRoot()...)

	addPrefixed(t, ram, prefixes, 1, 2)
	addPrefixed(t, paged, prefixes, 1, 2)
	blocked := blockSuffixSeals(t, pagedDir)
	err := paged.Flush()
	unblockSuffixSeals(t, blocked)
	if err == nil {
		t.Fatal("expected writeSeal failure after log append")
	}

	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}

	gotEarly, err := paged.GetRootAt(early)
	if err != nil {
		t.Fatalf("GetRootAt(%d): %v", early, err)
	}
	if !bytes.Equal(earlyRoot, gotEarly) {
		t.Fatal("historical root mismatch after log-flush retry")
	}
	compareProofsAt(t, ram, paged, early, earlyRoot, []uint32{0, early - 1})

	addPrefixed(t, ram, prefixes, 1, 3)
	addPrefixed(t, paged, prefixes, 1, 3)
	if err := ram.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ram.GetRoot(), paged.GetRoot()) {
		t.Fatal("root mismatch after hot-miss insert")
	}

	tip := paged.Count()
	tipRoot := append([]byte(nil), paged.GetRoot()...)
	if err := paged.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openSMSTWithRAMLimit(t, pagedDir, limit)
	defer reopened.Close()
	if !bytes.Equal(tipRoot, reopened.GetRoot()) {
		t.Fatal("reopen root mismatch after log-flush retry")
	}
	compareProofsAt(t, ram, reopened, tip, tipRoot, []uint32{0, tip - 1})
}

func TestSMSTPagedCOWOffFlushOffsets(t *testing.T) {
	t.Setenv(envSMSTCOW, "0")

	dir := t.TempDir()
	store := openSMSTWithRAMLimit(t, dir, 1_000_000_000)
	defer store.Close()
	if store.cowEnabled {
		t.Fatal("expected SMST_COW=0")
	}

	for i := 0; i < 16; i++ {
		if err := store.AddWithNode(int32(i), pagedTestVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if store.spilled {
		t.Fatal("COW-off store must stay in RAM")
	}
	if got := len(store.offsets); got != int(store.flushedLeafCount) {
		t.Fatalf("offsets %d want %d after first flush", got, store.flushedLeafCount)
	}
	early := store.Count()
	earlyRoot := append([]byte(nil), store.GetRoot()...)

	for i := 16; i < 24; i++ {
		if err := store.AddWithNode(int32(i), pagedTestVector(i), "n"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := len(store.offsets); got != int(store.flushedLeafCount) {
		t.Fatalf("offsets %d want %d after second flush", got, store.flushedLeafCount)
	}

	gotEarly, err := store.GetRootAt(early)
	if err != nil {
		t.Fatalf("GetRootAt(%d): %v", early, err)
	}
	if !bytes.Equal(earlyRoot, gotEarly) {
		t.Fatal("historical root mismatch after second flush")
	}
	entries, err := store.GetArtifactsAndProofs([]uint32{0, early - 1}, early)
	if err != nil {
		t.Fatalf("proof at %d: %v", early, err)
	}
	for _, e := range entries {
		if !VerifySMSTProofSlice(earlyRoot, early, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
			t.Fatalf("early proof failed nonce %d", e.Nonce)
		}
	}

	tip := store.Count()
	tipRoot := append([]byte(nil), store.GetRoot()...)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openSMSTWithRAMLimit(t, dir, 1_000_000_000)
	defer reopened.Close()
	if !bytes.Equal(tipRoot, reopened.GetRoot()) {
		t.Fatal("reopen root mismatch")
	}
	entries, err = reopened.GetArtifactsAndProofs([]uint32{0, tip - 1}, tip)
	if err != nil {
		t.Fatalf("reopen proofs: %v", err)
	}
	for _, e := range entries {
		if !VerifySMSTProofSlice(tipRoot, tip, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
			t.Fatalf("reopen proof failed nonce %d", e.Nonce)
		}
	}
}

func TestSMSTPagedEvictedPrefixLiveTip(t *testing.T) {
	const limit = 8
	paged := openSMSTWithRAMLimit(t, t.TempDir(), limit)
	defer paged.Close()

	addPrefixed(t, paged, 4, 2, 0)
	if err := paged.Flush(); err != nil {
		t.Fatal(err)
	}
	if !paged.spilled {
		t.Fatal("expected spill")
	}

	addPrefixed(t, paged, 5, 1, 2)
	if err := os.Remove(paged.suffixTreePath(0)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	nonce := int32(2)
	tip := paged.Count()
	tipRoot := append([]byte(nil), paged.GetRoot()...)
	entries, err := paged.GetArtifactsAndProofsByNonce([]int32{nonce}, tip)
	if err != nil {
		t.Fatalf("live-tip proof after evict+missing blob: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries %d want 1", len(entries))
	}
	e := entries[0]
	if !VerifySMSTProofSlice(tipRoot, tip, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
		t.Fatalf("live-tip proof failed nonce %d", e.Nonce)
	}
}

func blockSuffixSeals(t *testing.T, dir string) []string {
	t.Helper()
	suffixDir := filepath.Join(dir, suffixDirName)
	entries, err := os.ReadDir(suffixDir)
	if err != nil {
		t.Fatal(err)
	}
	var blocked []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".seal" {
			continue
		}
		path := filepath.Join(suffixDir, e.Name())
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		blocked = append(blocked, path)
	}
	if len(blocked) == 0 {
		t.Fatal("expected suffix seals to block")
	}
	return blocked
}

func unblockSuffixSeals(t *testing.T, paths []string) {
	t.Helper()
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}
