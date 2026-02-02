package artifacts

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOpenEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	if store.Count() != 0 {
		t.Errorf("expected count 0, got %d", store.Count())
	}

	if store.GetRoot() != nil {
		t.Errorf("expected nil root for empty store")
	}
}

func TestAddAndCount(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Add some artifacts
	for i := int32(0); i < 10; i++ {
		vector := []byte{byte(i), byte(i + 1), byte(i + 2)}
		if err := store.Add(i, vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
	}

	if store.Count() != 10 {
		t.Errorf("expected count 10, got %d", store.Count())
	}
}

func TestDuplicateNonceRejection(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	vector := []byte{1, 2, 3}
	if err := store.Add(42, vector); err != nil {
		t.Fatalf("First Add failed: %v", err)
	}

	// Try to add duplicate
	if err := store.Add(42, vector); err != ErrDuplicateNonce {
		t.Errorf("expected ErrDuplicateNonce, got %v", err)
	}

	// Count should still be 1
	if store.Count() != 1 {
		t.Errorf("expected count 1, got %d", store.Count())
	}
}

func TestFlushAndGetArtifact(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Add artifacts
	artifacts := []struct {
		nonce  int32
		vector []byte
	}{
		{100, []byte{1, 2, 3, 4}},
		{200, []byte{5, 6, 7, 8, 9}},
		{-50, []byte{10, 11}}, // Negative nonce
	}

	for _, a := range artifacts {
		if err := store.Add(a.nonce, a.vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", a.nonce, err)
		}
	}

	// Get from buffer (before flush)
	for i, a := range artifacts {
		nonce, vector, err := store.GetArtifact(uint32(i))
		if err != nil {
			t.Fatalf("GetArtifact(%d) failed: %v", i, err)
		}
		if nonce != a.nonce {
			t.Errorf("artifact %d: expected nonce %d, got %d", i, a.nonce, nonce)
		}
		if !bytes.Equal(vector, a.vector) {
			t.Errorf("artifact %d: vector mismatch", i)
		}
	}

	// Flush to disk
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Get from disk (after flush)
	for i, a := range artifacts {
		nonce, vector, err := store.GetArtifact(uint32(i))
		if err != nil {
			t.Fatalf("GetArtifact(%d) after flush failed: %v", i, err)
		}
		if nonce != a.nonce {
			t.Errorf("artifact %d after flush: expected nonce %d, got %d", i, a.nonce, nonce)
		}
		if !bytes.Equal(vector, a.vector) {
			t.Errorf("artifact %d after flush: vector mismatch", i)
		}
	}
}

func TestGetArtifactOutOfRange(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	store.Add(1, []byte{1})

	_, _, err = store.GetArtifact(5)
	if err != ErrLeafIndexOutOfRange {
		t.Errorf("expected ErrLeafIndexOutOfRange, got %v", err)
	}
}

func TestRecovery(t *testing.T) {
	dir := t.TempDir()

	// Create store and add data
	store1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	artifacts := []struct {
		nonce  int32
		vector []byte
	}{
		{10, []byte{1, 2, 3}},
		{20, []byte{4, 5, 6}},
		{30, []byte{7, 8, 9}},
	}

	for _, a := range artifacts {
		if err := store1.Add(a.nonce, a.vector); err != nil {
			t.Fatalf("Add failed: %v", err)
		}
	}

	if err := store1.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	root1 := store1.GetRoot()
	count1 := store1.Count()

	store1.Close()

	// Reopen and verify recovery
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Reopen failed: %v", err)
	}
	defer store2.Close()

	if store2.Count() != count1 {
		t.Errorf("recovered count: expected %d, got %d", count1, store2.Count())
	}

	root2 := store2.GetRoot()
	if !bytes.Equal(root1, root2) {
		t.Errorf("recovered root mismatch")
	}

	// Verify artifacts
	for i, a := range artifacts {
		nonce, vector, err := store2.GetArtifact(uint32(i))
		if err != nil {
			t.Fatalf("GetArtifact(%d) after recovery failed: %v", i, err)
		}
		if nonce != a.nonce {
			t.Errorf("artifact %d: expected nonce %d, got %d", i, a.nonce, nonce)
		}
		if !bytes.Equal(vector, a.vector) {
			t.Errorf("artifact %d: vector mismatch", i)
		}
	}

	// Verify duplicate rejection still works after recovery
	if err := store2.Add(10, []byte{1}); err != ErrDuplicateNonce {
		t.Errorf("expected duplicate rejection after recovery, got %v", err)
	}
}

func TestProofGeneration(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	// Add 8 artifacts to get a single-peak tree
	for i := int32(0); i < 8; i++ {
		store.Add(i, []byte{byte(i)})
	}

	root := store.GetRoot()
	if root == nil {
		t.Fatal("root should not be nil")
	}

	// Generate and verify proof for each leaf
	for i := uint32(0); i < 8; i++ {
		proof, err := store.GetProof(i, 8)
		if err != nil {
			t.Fatalf("GetProof(%d, 8) failed: %v", i, err)
		}

		// Proof should have 3 siblings for a perfect 8-leaf tree (log2(8) = 3)
		if len(proof) != 3 {
			t.Errorf("proof for leaf %d: expected 3 elements, got %d", i, len(proof))
		}

		// Verify the proof
		leafData := encodeLeaf(int32(i), []byte{byte(i)})
		if !VerifyProof(root, 8, i, leafData, proof) {
			t.Errorf("proof verification failed for leaf %d", i)
		}
	}
}

func TestProofForVariousTreeSizes(t *testing.T) {
	sizes := []int{1, 2, 3, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65, 100, 127, 128, 255, 256}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			dir := t.TempDir()
			store, _ := Open(dir)
			defer store.Close()

			for i := 0; i < size; i++ {
				store.Add(int32(i), []byte{byte(i)})
			}

			root := store.GetRoot()
			for i := uint32(0); i < uint32(size); i++ {
				proof, err := store.GetProof(i, uint32(size))
				if err != nil {
					t.Fatalf("GetProof(%d, %d) failed: %v", i, size, err)
				}
				leafData := encodeLeaf(int32(i), []byte{byte(i)})
				if !VerifyProof(root, uint32(size), i, leafData, proof) {
					t.Errorf("proof failed for leaf %d in tree of size %d", i, size)
				}
			}
		})
	}
}

func TestGetRootAt(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Empty store
	root, err := store.GetRootAt(0)
	if err != nil {
		t.Errorf("GetRootAt(0) on empty store should not error: %v", err)
	}
	if root != nil {
		t.Errorf("GetRootAt(0) should return nil")
	}

	// Add artifacts and record roots at each count
	roots := make([][]byte, 11)
	for i := int32(1); i <= 10; i++ {
		if err := store.Add(i, []byte{byte(i)}); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
		root, err := store.GetRootAt(uint32(i))
		if err != nil {
			t.Fatalf("GetRootAt(%d) failed: %v", i, err)
		}
		roots[i] = root
	}

	// Verify historical roots are still accessible
	for i := uint32(1); i <= 10; i++ {
		root, err := store.GetRootAt(i)
		if err != nil {
			t.Fatalf("GetRootAt(%d) failed: %v", i, err)
		}
		if !bytes.Equal(root, roots[i]) {
			t.Errorf("GetRootAt(%d) returned different root", i)
		}
	}
}

func TestGetFlushedRoot(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Empty store
	count, root := store.GetFlushedRoot()
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
	if root != nil {
		t.Errorf("expected nil root for empty store")
	}

	// Add but don't flush
	for i := int32(0); i < 5; i++ {
		store.Add(i, []byte{byte(i)})
	}

	count, root = store.GetFlushedRoot()
	if count != 0 {
		t.Errorf("expected flushed count 0 before flush, got %d", count)
	}

	// Flush
	store.Flush()
	count, root = store.GetFlushedRoot()
	if count != 5 {
		t.Errorf("expected flushed count 5 after flush, got %d", count)
	}
	if root == nil {
		t.Error("expected non-nil root after flush")
	}
}

func TestAddAfterClose(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	store.Add(1, []byte{1})
	store.Close()

	err := store.Add(2, []byte{2})
	if err != ErrStoreClosed {
		t.Errorf("expected ErrStoreClosed, got %v", err)
	}
}

func TestNegativeNonce(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	store.Add(-1, []byte{1})
	store.Add(-100, []byte{2})
	store.Add(-2147483648, []byte{3}) // INT32_MIN

	if store.Count() != 3 {
		t.Errorf("expected 3, got %d", store.Count())
	}

	nonce, _, err := store.GetArtifact(0)
	if err != nil || nonce != -1 {
		t.Errorf("expected nonce -1, got %d, err: %v", nonce, err)
	}
}

func TestRecoveryWithTruncatedRecord(t *testing.T) {
	dir := t.TempDir()

	store1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	store1.Add(10, []byte{1, 2, 3})
	store1.Add(20, []byte{4, 5, 6})
	store1.Flush()
	root1 := store1.GetRoot()
	store1.Close()

	// Append garbage (partial record) to data file
	dataPath := filepath.Join(dir, "artifacts.data")
	f, err := os.OpenFile(dataPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open data file: %v", err)
	}
	f.Write([]byte{0x10, 0x00, 0x00, 0x00}) // partial header
	f.Close()

	// Reopen - should recover by truncating partial record
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Reopen with truncated record failed: %v", err)
	}
	defer store2.Close()

	if store2.Count() != 2 {
		t.Errorf("expected count 2 after truncation recovery, got %d", store2.Count())
	}

	root2 := store2.GetRoot()
	if !bytes.Equal(root1, root2) {
		t.Errorf("root mismatch after truncation recovery")
	}
}

func TestConcurrentGetArtifact(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	const artifactCount = 100
	const goroutines = 50
	const readsPerGoroutine = 20

	for i := 0; i < artifactCount; i++ {
		vector := []byte{byte(i), byte(i + 1), byte(i + 2), byte(i + 3)}
		if err := store.Add(int32(i), vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
	}

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	var wg sync.WaitGroup
	errChan := make(chan error, goroutines*readsPerGoroutine)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for r := 0; r < readsPerGoroutine; r++ {
				leafIdx := uint32((goroutineID*readsPerGoroutine + r) % artifactCount)
				nonce, vector, err := store.GetArtifact(leafIdx)
				if err != nil {
					errChan <- fmt.Errorf("goroutine %d: GetArtifact(%d) failed: %v", goroutineID, leafIdx, err)
					return
				}
				expectedNonce := int32(leafIdx)
				expectedVector := []byte{byte(leafIdx), byte(leafIdx + 1), byte(leafIdx + 2), byte(leafIdx + 3)}
				if nonce != expectedNonce {
					errChan <- fmt.Errorf("goroutine %d: leafIdx %d: expected nonce %d, got %d", goroutineID, leafIdx, expectedNonce, nonce)
					return
				}
				if !bytes.Equal(vector, expectedVector) {
					errChan <- fmt.Errorf("goroutine %d: leafIdx %d: vector mismatch", goroutineID, leafIdx)
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Error(err)
	}
}

func BenchmarkAdd(b *testing.B) {
	dir := b.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	vector := make([]byte, 100)
	for i := range vector {
		vector[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Add(int32(i), vector)
	}
}
