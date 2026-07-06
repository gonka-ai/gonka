package artifacts

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestManagedArtifactStore_GetOrCreateStore(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	store1, err := m.GetOrCreateStore(100, "model-a")
	if err != nil {
		t.Fatalf("GetOrCreateStore(100, model-a) failed: %v", err)
	}
	if store1 == nil {
		t.Fatal("store1 is nil")
	}

	store1Again, err := m.GetOrCreateStore(100, "model-a")
	if err != nil {
		t.Fatalf("GetOrCreateStore(100, model-a) again failed: %v", err)
	}
	if store1 != store1Again {
		t.Error("expected same store instance for same stage/model")
	}

	store2, err := m.GetOrCreateStore(100, "org/model-b")
	if err != nil {
		t.Fatalf("GetOrCreateStore(100, org/model-b) failed: %v", err)
	}
	if store1 == store2 {
		t.Error("expected different store instance for different model")
	}

	// Verify directories created
	if _, err := os.Stat(filepath.Join(dir, "100", "model-a")); os.IsNotExist(err) {
		t.Error("stage/model directory for model-a not created")
	}
	if _, err := os.Stat(filepath.Join(dir, "100", "org%2Fmodel-b")); os.IsNotExist(err) {
		t.Error("stage/model directory for org/model-b not created")
	}
}

func TestManagedArtifactStore_GetStore_NotFound(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	_, err := m.GetStore(999, "missing-model")
	if err == nil {
		t.Error("expected error for non-existent epoch")
	}
}

func TestManagedArtifactStore_GetStore_ExistingDir(t *testing.T) {
	dir := t.TempDir()

	// Create epoch 100 with first manager
	m1 := NewManagedArtifactStore(dir, 3)
	store, err := m1.GetOrCreateStore(100, "org/model")
	if err != nil {
		t.Fatalf("GetOrCreateStore failed: %v", err)
	}
	if err := store.AddWithNode(1, []byte("test"), ""); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := m1.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	m1.Close()

	// New manager should find existing epoch via GetStore
	m2 := NewManagedArtifactStore(dir, 3)
	defer m2.Close()

	store2, err := m2.GetStore(100, "org/model")
	if err != nil {
		t.Fatalf("GetStore(100, org/model) failed: %v", err)
	}
	if store2.Count() != 1 {
		t.Errorf("expected count 1, got %d", store2.Count())
	}
}

func TestManagedArtifactStore_GetStoresForStage(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	for _, modelID := range []string{"z-model", "org/model-a", "model-b"} {
		store, err := m.GetOrCreateStore(100, modelID)
		if err != nil {
			t.Fatalf("GetOrCreateStore(%q) failed: %v", modelID, err)
		}
		if err := store.AddWithNode(1, []byte(modelID), ""); err != nil {
			t.Fatalf("Add failed for %q: %v", modelID, err)
		}
	}

	stores, err := m.GetStoresForStage(100)
	if err != nil {
		t.Fatalf("GetStoresForStage failed: %v", err)
	}
	if len(stores) != 3 {
		t.Fatalf("expected 3 model stores, got %d", len(stores))
	}
	if stores[0].ModelID != "model-b" || stores[1].ModelID != "org/model-a" || stores[2].ModelID != "z-model" {
		t.Fatalf("unexpected model ordering: %+v", stores)
	}
}

func TestManagedArtifactStore_PruneStore(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	// Create stores at different heights
	for _, tc := range []struct {
		height  int64
		modelID string
	}{
		{100, "model-a"},
		{100, "model-b"},
		{200, "model-a"},
		{300, "model-a"},
	} {
		if _, err := m.GetOrCreateStore(tc.height, tc.modelID); err != nil {
			t.Fatalf("GetOrCreateStore(%d, %q) failed: %v", tc.height, tc.modelID, err)
		}
	}

	// Prune store at height 100
	if err := m.PruneStore(100); err != nil {
		t.Fatalf("PruneStore(100) failed: %v", err)
	}

	// Verify directory removed
	if _, err := os.Stat(filepath.Join(dir, "100")); !os.IsNotExist(err) {
		t.Error("height 100 directory should be removed")
	}

	// Other heights should still exist
	if _, err := os.Stat(filepath.Join(dir, "200")); os.IsNotExist(err) {
		t.Error("height 200 directory should still exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "300")); os.IsNotExist(err) {
		t.Error("height 300 directory should still exist")
	}

	// GetStore should fail for pruned height
	if _, err := m.GetStore(100, "model-a"); err == nil {
		t.Error("expected error for pruned height")
	}
}

func TestManagedArtifactStore_ListStores(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	// Initially empty
	heights, err := m.ListStores()
	if err != nil {
		t.Fatalf("ListStores failed: %v", err)
	}
	if len(heights) != 0 {
		t.Errorf("expected 0 stores, got %d", len(heights))
	}

	// Create stores (out of order)
	for _, height := range []int64{300, 100, 200} {
		if _, err := m.GetOrCreateStore(height, "model-a"); err != nil {
			t.Fatalf("GetOrCreateStore(%d) failed: %v", height, err)
		}
	}

	// Should return sorted
	heights, err = m.ListStores()
	if err != nil {
		t.Fatalf("ListStores failed: %v", err)
	}
	if len(heights) != 3 {
		t.Fatalf("expected 3 stores, got %d", len(heights))
	}
	if heights[0] != 100 || heights[1] != 200 || heights[2] != 300 {
		t.Errorf("expected [100, 200, 300], got %v", heights)
	}
}

func TestManagedArtifactStore_AutoPrune(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3) // retainCount=3
	defer m.Close()

	// Create stores at heights 100, 200, 300, 400, 500
	for _, height := range []int64{100, 200, 300, 400, 500} {
		if _, err := m.GetOrCreateStore(height, "model-a"); err != nil {
			t.Fatalf("GetOrCreateStore(%d) failed: %v", height, err)
		}
		if _, err := m.GetOrCreateStore(height, "model-b"); err != nil {
			t.Fatalf("GetOrCreateStore(%d, model-b) failed: %v", height, err)
		}
	}

	// Trigger cleanup manually
	m.cleanup()

	// Wait for async prune goroutines
	time.Sleep(100 * time.Millisecond)

	heights, err := m.ListStores()
	if err != nil {
		t.Fatalf("ListStores failed: %v", err)
	}

	// With retainCount=3, should keep newest 3: 300, 400, 500
	if len(heights) != 3 {
		t.Errorf("expected 3 stores after prune, got %d: %v", len(heights), heights)
	}
	if len(heights) == 3 && (heights[0] != 300 || heights[1] != 400 || heights[2] != 500) {
		t.Errorf("expected [300, 400, 500], got %v", heights)
	}
}

func TestManagedArtifactStore_Flush(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	store, err := m.GetOrCreateStore(100, "model-a")
	if err != nil {
		t.Fatalf("GetOrCreateStore failed: %v", err)
	}

	if err := store.AddWithNode(1, []byte("test"), ""); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if err := m.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Verify data file has content
	dataFile := filepath.Join(dir, "100", "model-a", "artifacts.data")
	info, err := os.Stat(dataFile)
	if err != nil {
		t.Fatalf("stat data file: %v", err)
	}
	if info.Size() == 0 {
		t.Error("data file should not be empty after flush")
	}
}

func TestManagedArtifactStore_ParallelModelStores(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	const (
		stage          = int64(100)
		modelCount     = 12
		writesPerModel = 200
	)

	var writeWG sync.WaitGroup
	writeErrs := make(chan error, modelCount)

	for modelIdx := 0; modelIdx < modelCount; modelIdx++ {
		writeWG.Add(1)
		go func(modelIdx int) {
			defer writeWG.Done()

			modelID := fmt.Sprintf("org/model-%02d", modelIdx)
			store, err := m.GetOrCreateStore(stage, modelID)
			if err != nil {
				writeErrs <- fmt.Errorf("GetOrCreateStore(%s): %w", modelID, err)
				return
			}

			for i := 0; i < writesPerModel; i++ {
				nonce := int32(modelIdx*writesPerModel + i)
				vector := []byte{byte(modelIdx), byte(i), byte(i >> 8), byte((modelIdx + i) % 251)}
				nodeID := fmt.Sprintf("node-%02d", modelIdx)
				if err := store.AddWithNode(nonce, vector, nodeID); err != nil {
					writeErrs <- fmt.Errorf("AddWithNode(%s, %d): %w", modelID, nonce, err)
					return
				}
			}

			if err := store.Flush(); err != nil {
				writeErrs <- fmt.Errorf("Flush(%s): %w", modelID, err)
			}
		}(modelIdx)
	}

	writeWG.Wait()
	close(writeErrs)

	for err := range writeErrs {
		t.Fatal(err)
	}

	stageStores, err := m.GetStoresForStage(stage)
	if err != nil {
		t.Fatalf("GetStoresForStage(%d) failed: %v", stage, err)
	}
	if len(stageStores) != modelCount {
		t.Fatalf("expected %d stores, got %d", modelCount, len(stageStores))
	}

	var readWG sync.WaitGroup
	readErrs := make(chan error, modelCount)

	for modelIdx := 0; modelIdx < modelCount; modelIdx++ {
		readWG.Add(1)
		go func(modelIdx int) {
			defer readWG.Done()

			modelID := fmt.Sprintf("org/model-%02d", modelIdx)
			store, err := m.GetStore(stage, modelID)
			if err != nil {
				readErrs <- fmt.Errorf("GetStore(%s): %w", modelID, err)
				return
			}

			if count := store.Count(); count != writesPerModel {
				readErrs <- fmt.Errorf("Count(%s): expected %d, got %d", modelID, writesPerModel, count)
				return
			}

			nodeCounts := store.GetNodeCounts()
			expectedNodeID := fmt.Sprintf("node-%02d", modelIdx)
			if nodeCounts[expectedNodeID] != writesPerModel {
				readErrs <- fmt.Errorf("GetNodeDistribution(%s): expected %d for %s, got %d", modelID, writesPerModel, expectedNodeID, nodeCounts[expectedNodeID])
				return
			}

			for _, offset := range []uint32{0, writesPerModel / 2, writesPerModel - 1} {
				nonce, vector, _, err := store.GetArtifactAndProof(offset, writesPerModel)
				if err != nil {
					readErrs <- fmt.Errorf("GetArtifact(%s, %d): %w", modelID, offset, err)
					return
				}

				expectedNonce := int32(modelIdx*writesPerModel + int(offset))
				expectedVector := []byte{byte(modelIdx), byte(offset), byte(offset >> 8), byte((modelIdx + int(offset)) % 251)}
				if nonce != expectedNonce {
					readErrs <- fmt.Errorf("artifact nonce mismatch for %s at %d: expected %d, got %d", modelID, offset, expectedNonce, nonce)
					return
				}
				if !bytes.Equal(vector, expectedVector) {
					readErrs <- fmt.Errorf("artifact vector mismatch for %s at %d", modelID, offset)
					return
				}
			}
		}(modelIdx)
	}

	readWG.Wait()
	close(readErrs)

	for err := range readErrs {
		t.Fatal(err)
	}
}
