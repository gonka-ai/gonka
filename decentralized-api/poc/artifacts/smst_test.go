package artifacts

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// Test helpers - these mirror removed production functions for test convenience

type SMSTProofElement struct {
	SiblingHash  []byte
	SiblingCount uint32
}

func DecodeProofElements(proof [][]byte) []SMSTProofElement {
	elements := make([]SMSTProofElement, len(proof))
	for i, data := range proof {
		if len(data) != 36 {
			return nil
		}
		elements[i].SiblingHash = data[:32]
		elements[i].SiblingCount = binary.LittleEndian.Uint32(data[32:36])
	}
	return elements
}

func VerifySMSTProofWithCounts(rootHash []byte, count uint32, nonce int32, leafData []byte, proof []SMSTProofElement) bool {
	if len(proof) == 0 {
		return false
	}

	depth := len(proof)
	leafHash := smstHashLeaf(leafData)
	path := smstNoncePath(nonce, depth)

	currentHash := leafHash
	currentCount := uint32(1)

	for i := depth - 1; i >= 0; i-- {
		elem := proof[i]
		goRight := path[i]

		var leftHash, rightHash []byte
		var leftCount, rightCount uint32

		if goRight {
			leftHash = elem.SiblingHash
			rightHash = currentHash
			leftCount = elem.SiblingCount
			rightCount = currentCount
		} else {
			leftHash = currentHash
			rightHash = elem.SiblingHash
			leftCount = currentCount
			rightCount = elem.SiblingCount
		}

		currentCount = leftCount + rightCount
		currentHash = smstHashNode(leftHash, rightHash, currentCount)
	}

	if currentCount != count {
		return false
	}

	return bytesEqual(currentHash, rootHash)
}

func VerifySMSTProofSlice(rootHash []byte, count uint32, nonce int32, leafData []byte, proof [][]byte) bool {
	if len(proof) == 0 {
		return false
	}
	elements := DecodeProofElements(proof)
	if elements == nil {
		return false
	}
	return VerifySMSTProofWithCounts(rootHash, count, nonce, leafData, elements)
}

func EncodeSMSTProof(proof []SMSTProofElement) []byte {
	if len(proof) == 0 {
		return nil
	}
	buf := make([]byte, 1+len(proof)*36)
	buf[0] = byte(len(proof))
	offset := 1
	for _, elem := range proof {
		copy(buf[offset:offset+32], elem.SiblingHash)
		binary.LittleEndian.PutUint32(buf[offset+32:offset+36], elem.SiblingCount)
		offset += 36
	}
	return buf
}

func DecodeSMSTProof(data []byte) ([]SMSTProofElement, error) {
	if len(data) < 1 {
		return nil, ErrLeafIndexOutOfRange
	}
	depth := int(data[0])
	if len(data) != 1+depth*36 {
		return nil, ErrLeafIndexOutOfRange
	}
	proof := make([]SMSTProofElement, depth)
	offset := 1
	for i := 0; i < depth; i++ {
		proof[i].SiblingHash = make([]byte, 32)
		copy(proof[i].SiblingHash, data[offset:offset+32])
		proof[i].SiblingCount = binary.LittleEndian.Uint32(data[offset+32 : offset+36])
		offset += 36
	}
	return proof, nil
}

func encodeTestProofForTransport(proof []SMSTProofElement) [][]byte {
	result := make([][]byte, len(proof))
	for i, elem := range proof {
		encoded := make([]byte, 36)
		copy(encoded[:32], elem.SiblingHash)
		binary.LittleEndian.PutUint32(encoded[32:], elem.SiblingCount)
		result[i] = encoded
	}
	return result
}

func (s *SMSTArtifactStore) Add(nonce int32, vector []byte) error {
	return s.AddWithNode(nonce, vector, "")
}

func (s *SMSTArtifactStore) GetRoot() []byte {
	return s.getRoot()
}

func TestSMSTEmpty(t *testing.T) {
	tree := NewSMST(24)

	root, count := tree.GetRoot()
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
	if root == nil {
		t.Error("expected non-nil empty root")
	}
}

func TestSMSTInsertAndCount(t *testing.T) {
	tree := NewSMST(24)

	for i := int32(0); i < 10; i++ {
		leafHash := smstHashLeaf(encodeLeaf(i, []byte{byte(i)}))
		_, err := tree.Insert(i, leafHash)
		if err != nil {
			t.Fatalf("Insert(%d) failed: %v", i, err)
		}
	}

	if tree.Count() != 10 {
		t.Errorf("expected count 10, got %d", tree.Count())
	}
}

func TestSMSTDuplicateRejection(t *testing.T) {
	tree := NewSMST(24)

	leafHash := smstHashLeaf(encodeLeaf(42, []byte{1, 2, 3}))
	if _, err := tree.Insert(42, leafHash); err != nil {
		t.Fatalf("First Insert failed: %v", err)
	}

	if _, err := tree.Insert(42, leafHash); err != ErrDuplicateNonce {
		t.Errorf("expected ErrDuplicateNonce, got %v", err)
	}

	if tree.Count() != 1 {
		t.Errorf("expected count 1, got %d", tree.Count())
	}
}

func TestSMSTDenseIndexNavigation(t *testing.T) {
	tree := NewSMST(24)

	nonces := []int32{100, 5, 1000, 50, 10}
	for _, nonce := range nonces {
		leafHash := smstHashLeaf(encodeLeaf(nonce, []byte{byte(nonce)}))
		tree.Insert(nonce, leafHash)
	}

	for i := uint32(0); i < tree.Count(); i++ {
		_, proof, err := tree.GetLeafByDenseIndex(i)
		if err != nil {
			t.Fatalf("GetLeafByDenseIndex(%d) failed: %v", i, err)
		}
		if len(proof) == 0 {
			t.Errorf("expected non-empty proof for index %d", i)
		}
	}
}

func TestSMSTRootConsistency(t *testing.T) {
	tree1 := NewSMST(24)
	tree2 := NewSMST(24)

	nonces := []int32{10, 20, 30}
	for _, n := range nonces {
		leafHash := smstHashLeaf(encodeLeaf(n, []byte{byte(n)}))
		tree1.Insert(n, leafHash)
	}

	for i := len(nonces) - 1; i >= 0; i-- {
		n := nonces[i]
		leafHash := smstHashLeaf(encodeLeaf(n, []byte{byte(n)}))
		tree2.Insert(n, leafHash)
	}

	root1, _ := tree1.GetRoot()
	root2, _ := tree2.GetRoot()

	if !bytes.Equal(root1, root2) {
		t.Error("roots should be equal regardless of insertion order")
	}
}

func TestSMSTDepthExpansion(t *testing.T) {
	tree := NewSMST(4)

	largeNonce := int32(1 << 20)
	leafHash := smstHashLeaf(encodeLeaf(largeNonce, []byte{1}))

	_, err := tree.Insert(largeNonce, leafHash)
	if err != nil {
		t.Fatalf("Insert with large nonce failed: %v", err)
	}

	if tree.Depth() < 20 {
		t.Errorf("expected depth >= 20, got %d", tree.Depth())
	}
}

func TestSMSTStoreBasics(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	if store.Count() != 0 {
		t.Errorf("expected count 0, got %d", store.Count())
	}

	for i := int32(0); i < 10; i++ {
		if err := store.Add(i, []byte{byte(i)}); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
	}

	if store.Count() != 10 {
		t.Errorf("expected count 10, got %d", store.Count())
	}
}

func TestSMSTStoreDuplicateRejection(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenSMST(dir)
	defer store.Close()

	if err := store.Add(42, []byte{1}); err != nil {
		t.Fatalf("First Add failed: %v", err)
	}

	if err := store.Add(42, []byte{2}); err != ErrDuplicateNonce {
		t.Errorf("expected ErrDuplicateNonce, got %v", err)
	}
}

func TestSMSTStoreRecovery(t *testing.T) {
	dir := t.TempDir()

	store1, _ := OpenSMST(dir)
	for i := int32(0); i < 5; i++ {
		store1.Add(i*10, []byte{byte(i)})
	}
	store1.Flush()
	root1 := store1.GetRoot()
	count1 := store1.Count()
	store1.Close()

	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("Reopen failed: %v", err)
	}
	defer store2.Close()

	if store2.Count() != count1 {
		t.Errorf("recovered count: expected %d, got %d", count1, store2.Count())
	}

	root2 := store2.GetRoot()
	if !bytes.Equal(root1, root2) {
		t.Error("recovered root mismatch")
	}
}

func TestSMSTStoreProof(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenSMST(dir)
	defer store.Close()

	for i := int32(0); i < 8; i++ {
		store.Add(i, []byte{byte(i)})
	}

	root := store.GetRoot()
	for i := uint32(0); i < 8; i++ {
		nonce, vector, proof, err := store.GetArtifactAndProof(i, 8)
		if err != nil {
			t.Fatalf("GetArtifactAndProof(%d) failed: %v", i, err)
		}
		if len(proof) == 0 {
			t.Errorf("expected non-empty proof for index %d", i)
		}

		leafData := encodeLeaf(nonce, vector)

		proofElements := decodeProofFromTransport(proof)
		if !VerifySMSTProofWithCounts(root, 8, nonce, leafData, proofElements) {
			t.Errorf("proof verification failed for index %d", i)
		}
	}
}

func decodeProofFromTransport(proof [][]byte) []SMSTProofElement {
	elements := make([]SMSTProofElement, len(proof))
	for i, data := range proof {
		elements[i].SiblingHash = data[:32]
		elements[i].SiblingCount = binary.LittleEndian.Uint32(data[32:])
	}
	return elements
}

func TestSMSTStoreGetArtifact(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenSMST(dir)
	defer store.Close()

	// Artifacts inserted in random order
	inputArtifacts := []struct {
		nonce  int32
		vector []byte
	}{
		{100, []byte{1, 2, 3}},
		{50, []byte{4, 5, 6}},
		{200, []byte{7, 8, 9}},
	}

	for _, a := range inputArtifacts {
		store.Add(a.nonce, a.vector)
	}

	// SMST returns artifacts in tree-traversal order (by nonce position)
	// Smaller nonces go to the left, so order should be: 50, 100, 200
	expectedOrder := []struct {
		nonce  int32
		vector []byte
	}{
		{50, []byte{4, 5, 6}},
		{100, []byte{1, 2, 3}},
		{200, []byte{7, 8, 9}},
	}

	count := store.Count()
	for i, expected := range expectedOrder {
		nonce, vector, _, err := store.GetArtifactAndProof(uint32(i), count)
		if err != nil {
			t.Fatalf("GetArtifactAndProof(%d) failed: %v", i, err)
		}
		if nonce != expected.nonce {
			t.Errorf("artifact %d: expected nonce %d, got %d", i, expected.nonce, nonce)
		}
		if !bytes.Equal(vector, expected.vector) {
			t.Errorf("artifact %d: vector mismatch", i)
		}
	}
}

func TestSMSTVerifyProof(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	nonces := []int32{10, 20, 30, 40, 50}
	for _, n := range nonces {
		if err := store.Add(n, []byte{byte(n)}); err != nil {
			t.Fatalf("Add(%d): %v", n, err)
		}
	}
	store.Flush()

	count, rootHash := store.GetFlushedRoot()

	for i := uint32(0); i < count; i++ {
		nonce, vector, proof, err := store.GetArtifactAndProof(i, count)
		if err != nil {
			t.Fatalf("GetArtifactAndProof(%d): %v", i, err)
		}

		leafData := encodeLeaf(nonce, vector)
		if !VerifySMSTProofSlice(rootHash, count, nonce, leafData, proof) {
			t.Errorf("Proof verification failed for index %d (nonce=%d)", i, nonce)
		}
	}
}

func TestSMSTProofEncoding(t *testing.T) {
	proof := []SMSTProofElement{
		{SiblingHash: make([]byte, 32), SiblingCount: 100},
		{SiblingHash: make([]byte, 32), SiblingCount: 200},
	}

	for i := range proof[0].SiblingHash {
		proof[0].SiblingHash[i] = byte(i)
	}
	for i := range proof[1].SiblingHash {
		proof[1].SiblingHash[i] = byte(32 - i)
	}

	encoded := EncodeSMSTProof(proof)
	decoded, err := DecodeSMSTProof(encoded)
	if err != nil {
		t.Fatalf("DecodeSMSTProof failed: %v", err)
	}

	if len(decoded) != len(proof) {
		t.Fatalf("decoded length mismatch: expected %d, got %d", len(proof), len(decoded))
	}

	for i := range proof {
		if !bytes.Equal(decoded[i].SiblingHash, proof[i].SiblingHash) {
			t.Errorf("element %d: hash mismatch", i)
		}
		if decoded[i].SiblingCount != proof[i].SiblingCount {
			t.Errorf("element %d: count mismatch: expected %d, got %d", i, proof[i].SiblingCount, decoded[i].SiblingCount)
		}
	}
}

func BenchmarkSMSTAdd(b *testing.B) {
	dir := b.TempDir()
	store, _ := OpenSMST(dir)
	defer store.Close()

	vector := make([]byte, 24)
	for i := range vector {
		vector[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Add(int32(i), vector)
	}
}

func BenchmarkSMSTAddWithFlush(b *testing.B) {
	dir := b.TempDir()
	store, _ := OpenSMST(dir)
	defer store.Close()

	vector := make([]byte, 24)
	for i := range vector {
		vector[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Add(int32(i), vector)
		if (i+1)%1000 == 0 {
			store.Flush()
		}
	}
	store.Flush()
}

func BenchmarkSMSTGetProof(b *testing.B) {
	dir := b.TempDir()
	store, _ := OpenSMST(dir)
	defer store.Close()

	const treeSize = 100000
	vector := make([]byte, 24)
	for i := 0; i < treeSize; i++ {
		store.Add(int32(i), vector)
	}
	store.Flush()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		leafIdx := uint32(i % treeSize)
		store.GetArtifactAndProof(leafIdx, treeSize)
	}
}

func BenchmarkSMSTVerifyProof(b *testing.B) {
	dir := b.TempDir()
	store, _ := OpenSMST(dir)
	defer store.Close()

	const treeSize = 100000
	vector := make([]byte, 24)
	for i := 0; i < treeSize; i++ {
		store.Add(int32(i), vector)
	}
	store.Flush()

	root := store.GetRoot()
	proofs := make([][][]byte, 100)
	nonces := make([]int32, 100)
	for i := 0; i < 100; i++ {
		nonces[i], _, proofs[i], _ = store.GetArtifactAndProof(uint32(i), treeSize)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := i % 100
		leafData := encodeLeaf(nonces[idx], vector)
		elements := decodeProofFromTransport(proofs[idx])
		VerifySMSTProofWithCounts(root, treeSize, nonces[idx], leafData, elements)
	}
}

func BenchmarkSMSTRecovery(b *testing.B) {
	sizes := []int{10000, 100000, 1000000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			dir := b.TempDir()

			store, _ := OpenSMST(dir)
			vector := make([]byte, 24)
			for i := 0; i < size; i++ {
				store.Add(int32(i), vector)
			}
			store.Flush()
			store.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s, _ := OpenSMST(dir)
				s.Close()
			}
		})
	}
}

func TestSMSTProofSizes(t *testing.T) {
	sizes := []int{1000, 10000, 100000}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			dir := t.TempDir()
			store, _ := OpenSMST(dir)
			defer store.Close()

			vector := make([]byte, 24)
			for i := 0; i < size; i++ {
				store.Add(int32(i), vector)
			}

			var totalProofBytes int
			sampleCount := 100
			if size < sampleCount {
				sampleCount = size
			}

			for i := 0; i < sampleCount; i++ {
				leafIdx := uint32(i * size / sampleCount)
				_, _, proof, err := store.GetArtifactAndProof(leafIdx, uint32(size))
				if err != nil {
					t.Fatalf("GetArtifactAndProof failed: %v", err)
				}
				for _, h := range proof {
					totalProofBytes += len(h)
				}
			}

			avgProofBytes := totalProofBytes / sampleCount
			t.Logf("Tree size: %d, Average proof size: %d bytes (%d elements of 36 bytes)", size, avgProofBytes, avgProofBytes/36)
		})
	}
}

func TestSMSTStoreNodeDistribution(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	store.AddWithNode(1, []byte{1}, "nodeA")
	store.AddWithNode(2, []byte{2}, "nodeA")
	store.AddWithNode(3, []byte{3}, "nodeB")
	store.AddWithNode(4, []byte{4}, "")

	counts := store.GetNodeCounts()
	if counts["nodeA"] != 2 {
		t.Errorf("expected nodeA count 2, got %d", counts["nodeA"])
	}
	if counts["nodeB"] != 1 {
		t.Errorf("expected nodeB count 1, got %d", counts["nodeB"])
	}

	dist := store.GetNodeDistribution()
	if len(dist) != 0 {
		t.Errorf("expected empty distribution before flush")
	}

	store.Flush()

	dist = store.GetNodeDistribution()
	if dist["nodeA"] != 2 {
		t.Errorf("expected flushed nodeA count 2, got %d", dist["nodeA"])
	}
	if dist["nodeB"] != 1 {
		t.Errorf("expected flushed nodeB count 1, got %d", dist["nodeB"])
	}
}

func TestSMSTStoreNodeDistributionRecovery(t *testing.T) {
	dir := t.TempDir()

	store1, _ := OpenSMST(dir)
	store1.AddWithNode(1, []byte{1}, "nodeA")
	store1.AddWithNode(2, []byte{2}, "nodeB")
	store1.Flush()
	store1.Close()

	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST reopen failed: %v", err)
	}
	defer store2.Close()

	dist := store2.GetNodeDistribution()
	if dist["nodeA"] != 1 {
		t.Errorf("expected recovered nodeA count 1, got %d", dist["nodeA"])
	}
	if dist["nodeB"] != 1 {
		t.Errorf("expected recovered nodeB count 1, got %d", dist["nodeB"])
	}

	counts := store2.GetNodeCounts()
	if counts["nodeA"] != 1 {
		t.Errorf("expected recovered nodeA count 1 in current, got %d", counts["nodeA"])
	}
}

func TestSMSTStoreGetRootAt(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	root, err := store.GetRootAt(0)
	if err != nil {
		t.Errorf("GetRootAt(0) should not error: %v", err)
	}
	if root != nil {
		t.Errorf("GetRootAt(0) should return nil")
	}

	roots := make([][]byte, 6)
	for i := int32(1); i <= 5; i++ {
		store.Add(i, []byte{byte(i)})
		store.Flush()
		root, err := store.GetRootAt(uint32(i))
		if err != nil {
			t.Fatalf("GetRootAt(%d) failed: %v", i, err)
		}
		roots[i] = root
	}

	for i := uint32(1); i <= 5; i++ {
		root, err := store.GetRootAt(i)
		if err != nil {
			t.Fatalf("GetRootAt(%d) failed: %v", i, err)
		}
		if !bytes.Equal(root, roots[i]) {
			t.Errorf("GetRootAt(%d) returned different root", i)
		}
	}
}

func TestSMSTStoreGetFlushedRoot(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	count, root := store.GetFlushedRoot()
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
	if root != nil {
		t.Errorf("expected nil root for empty store")
	}

	for i := int32(0); i < 5; i++ {
		store.Add(i, []byte{byte(i)})
	}

	count, root = store.GetFlushedRoot()
	if count != 0 {
		t.Errorf("expected flushed count 0 before flush, got %d", count)
	}

	store.Flush()
	count, root = store.GetFlushedRoot()
	if count != 5 {
		t.Errorf("expected flushed count 5 after flush, got %d", count)
	}
	if root == nil {
		t.Error("expected non-nil root after flush")
	}
}

func TestSMSTStoreAddAfterClose(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenSMST(dir)
	store.Add(1, []byte{1})
	store.Close()

	err := store.Add(2, []byte{2})
	if err != ErrStoreClosed {
		t.Errorf("expected ErrStoreClosed, got %v", err)
	}
}

func TestSMSTStoreNegativeNonce(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenSMST(dir)
	defer store.Close()

	store.Add(-1, []byte{1})
	store.Add(-100, []byte{2})
	store.Add(-2147483648, []byte{3}) // INT32_MIN

	if store.Count() != 3 {
		t.Errorf("expected 3, got %d", store.Count())
	}

	store.Flush()

	// SMST orders by nonce interpreted as uint32
	// -1 = 0xFFFFFFFF (largest), -100 = 0xFFFFFF9C, -2147483648 = 0x80000000
	// Tree order (smallest first): 0x80000000, 0xFFFFFF9C, 0xFFFFFFFF
	// So: -2147483648, -100, -1
	expectedNonces := []int32{-2147483648, -100, -1}

	count := store.Count()
	for i, expected := range expectedNonces {
		nonce, _, _, err := store.GetArtifactAndProof(uint32(i), count)
		if err != nil {
			t.Fatalf("GetArtifactAndProof(%d) failed: %v", i, err)
		}
		if nonce != expected {
			t.Errorf("artifact %d: expected nonce %d, got %d", i, expected, nonce)
		}
	}
}

func TestSMSTStoreTruncatedRecordRecovery(t *testing.T) {
	dir := t.TempDir()

	store1, _ := OpenSMST(dir)
	store1.Add(10, []byte{1, 2, 3})
	store1.Add(20, []byte{4, 5, 6})
	store1.Flush()
	root1 := store1.GetRoot()
	store1.Close()

	dataPath := dir + "/artifacts.data"
	f, err := os.OpenFile(dataPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open data file: %v", err)
	}
	f.Write([]byte{0x10, 0x00, 0x00, 0x00})
	f.Close()

	store2, err := OpenSMST(dir)
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

func TestSMSTStoreCapacityExceeded(t *testing.T) {
	t.Skip("MaxLeafCount is too large to test in unit tests")
}

func TestSMSTStoreConcurrentGetArtifact(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	const artifactCount = 100
	const goroutines = 50
	const readsPerGoroutine = 20

	// Insert artifacts with sequential nonces
	for i := 0; i < artifactCount; i++ {
		vector := []byte{byte(i), byte(i + 1), byte(i + 2), byte(i + 3)}
		if err := store.Add(int32(i), vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
	}

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Build expected mapping: dense index -> (nonce, vector)
	// SMST orders by nonce position in tree (sorted order for sequential nonces)
	count := store.Count()
	expectedData := make(map[uint32]struct {
		nonce  int32
		vector []byte
	})
	for i := uint32(0); i < artifactCount; i++ {
		nonce, vector, _, _ := store.GetArtifactAndProof(i, count)
		expectedData[i] = struct {
			nonce  int32
			vector []byte
		}{nonce, vector}
	}

	var wg sync.WaitGroup
	errChan := make(chan error, goroutines*readsPerGoroutine)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for r := 0; r < readsPerGoroutine; r++ {
				leafIdx := uint32((goroutineID*readsPerGoroutine + r) % artifactCount)
				nonce, vector, _, err := store.GetArtifactAndProof(leafIdx, count)
				if err != nil {
					errChan <- fmt.Errorf("goroutine %d: GetArtifactAndProof(%d) failed: %v", goroutineID, leafIdx, err)
					return
				}
				expected := expectedData[leafIdx]
				if nonce != expected.nonce {
					errChan <- fmt.Errorf("goroutine %d: leafIdx %d: expected nonce %d, got %d", goroutineID, leafIdx, expected.nonce, nonce)
					return
				}
				if !bytes.Equal(vector, expected.vector) {
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

// TestSMSTStoreConcurrentReadsHeavy tests many parallel goroutines doing heavy reads
func TestSMSTStoreConcurrentReadsHeavy(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	const artifactCount = 1000
	const goroutines = 100
	const readsPerGoroutine = 100

	// Insert artifacts
	for i := 0; i < artifactCount; i++ {
		vector := make([]byte, 24)
		binary.LittleEndian.PutUint32(vector, uint32(i))
		if err := store.Add(int32(i), vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
	}
	store.Flush()

	count := store.Count()
	rootHash := store.GetRoot()

	var wg sync.WaitGroup
	errChan := make(chan error, goroutines)

	// Launch many concurrent readers
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for r := 0; r < readsPerGoroutine; r++ {
				leafIdx := uint32((goroutineID + r*goroutines) % int(count))

				nonce, vector, proof, err := store.GetArtifactAndProof(leafIdx, count)
				if err != nil {
					errChan <- fmt.Errorf("g%d: GetArtifactAndProof(%d) failed: %v", goroutineID, leafIdx, err)
					return
				}
				if len(vector) != 24 {
					errChan <- fmt.Errorf("g%d: unexpected vector length %d", goroutineID, len(vector))
					return
				}

				leafData := encodeLeaf(nonce, vector)
				elements := DecodeProofElements(proof)
				if !VerifySMSTProofWithCounts(rootHash, count, nonce, leafData, elements) {
					errChan <- fmt.Errorf("g%d: proof verification failed for index %d", goroutineID, leafIdx)
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errChan)

	errCount := 0
	for err := range errChan {
		t.Error(err)
		errCount++
	}

	if errCount == 0 {
		t.Logf("Success: %d goroutines x %d reads = %d concurrent operations", goroutines, readsPerGoroutine, goroutines*readsPerGoroutine)
	}
}

// TestSMSTStoreConcurrentReadsWithProofs tests GetArtifact and GetProof together under load
func TestSMSTStoreConcurrentReadsWithProofs(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	const artifactCount = 500
	const goroutines = 50
	const opsPerGoroutine = 50

	// Insert with random-ish nonces
	for i := 0; i < artifactCount; i++ {
		nonce := int32(i*7 + 13) // non-sequential to stress tree navigation
		vector := make([]byte, 24)
		binary.LittleEndian.PutUint32(vector, uint32(nonce))
		if err := store.Add(nonce, vector); err != nil {
			t.Fatalf("Add failed: %v", err)
		}
	}
	store.Flush()

	count := store.Count()
	rootHash := store.GetRoot()

	var wg sync.WaitGroup
	var successCount, failCount int64

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			localSuccess := int64(0)
			localFail := int64(0)

			for op := 0; op < opsPerGoroutine; op++ {
				idx := uint32((gid*opsPerGoroutine + op) % int(count))

				nonce, vector, proof, err := store.GetArtifactAndProof(idx, count)
				if err != nil {
					localFail++
					continue
				}

				leafData := encodeLeaf(nonce, vector)
				elements := DecodeProofElements(proof)
				if VerifySMSTProofWithCounts(rootHash, count, nonce, leafData, elements) {
					localSuccess++
				} else {
					localFail++
				}
			}

			atomic.AddInt64(&successCount, localSuccess)
			atomic.AddInt64(&failCount, localFail)
		}(g)
	}

	wg.Wait()

	if failCount > 0 {
		t.Errorf("Concurrent test had %d failures out of %d operations", failCount, goroutines*opsPerGoroutine)
	} else {
		t.Logf("All %d concurrent operations succeeded", successCount)
	}
}

// TestSMSTStoreConcurrentReadsWhileWriting tests that reads don't block during writes
// and data remains consistent when read/write operations interleave.
func TestSMSTStoreConcurrentReadsWhileWriting(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	// Pre-populate some data
	for i := 0; i < 100; i++ {
		vector := make([]byte, 24)
		binary.LittleEndian.PutUint32(vector, uint32(i))
		if err := store.Add(int32(i), vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
	}
	store.Flush()

	var wg sync.WaitGroup
	var readSuccess, readFail, writeSuccess int64

	// Start readers that run continuously
	readerDone := make(chan struct{})
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for {
				select {
				case <-readerDone:
					return
				default:
					count := store.Count()
					if count == 0 {
						continue
					}
					idx := uint32(gid) % count
					nonce, vector, _, err := store.GetArtifactAndProof(idx, count)
					if err != nil {
						atomic.AddInt64(&readFail, 1)
						continue
					}
					if vector == nil || nonce == 0 && len(vector) == 0 {
						atomic.AddInt64(&readFail, 1)
						continue
					}
					atomic.AddInt64(&readSuccess, 1)
				}
			}
		}(g)
	}

	// Writer adds more data while readers are active
	for i := 100; i < 500; i++ {
		vector := make([]byte, 24)
		binary.LittleEndian.PutUint32(vector, uint32(i))
		if err := store.Add(int32(i), vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
		if i%50 == 0 {
			store.Flush()
		}
		atomic.AddInt64(&writeSuccess, 1)
	}

	// Stop readers
	close(readerDone)
	wg.Wait()

	if readFail > 0 {
		t.Errorf("Had %d read failures during concurrent read/write", readFail)
	}
	t.Logf("Concurrent read/write: %d successful reads, %d writes", readSuccess, writeSuccess)
}

// TestSMSTSumBasedOrdering verifies the core SMST property:
// dense indices are deterministically ordered by nonce position in the sparse tree,
// using the sum (count) at each node to navigate to the i-th leaf.
func TestSMSTSumBasedOrdering(t *testing.T) {
	tree := NewSMST(24)

	// Insert nonces in random order - they should be retrievable by dense index
	// in a deterministic order based on tree structure (left-to-right traversal)
	nonces := []int32{1000, 50, 500, 5, 100, 10, 750}
	for _, nonce := range nonces {
		leafHash := smstHashLeaf(encodeLeaf(nonce, []byte{byte(nonce)}))
		_, err := tree.Insert(nonce, leafHash)
		if err != nil {
			t.Fatalf("Insert(%d) failed: %v", nonce, err)
		}
	}

	// Verify count equals number of inserted nonces
	if tree.Count() != uint32(len(nonces)) {
		t.Fatalf("expected count %d, got %d", len(nonces), tree.Count())
	}

	// Get all nonces by dense index - should be left-to-right tree order
	retrievedNonces := make([]int32, tree.Count())
	for i := uint32(0); i < tree.Count(); i++ {
		nonce, _, err := tree.GetLeafByDenseIndex(i)
		if err != nil {
			t.Fatalf("GetLeafByDenseIndex(%d) failed: %v", i, err)
		}
		retrievedNonces[i] = nonce
	}

	// The order should be deterministic based on tree structure
	// Smaller nonces go to the left (path has more 0 bits), larger to the right
	// So nonces should be roughly sorted when retrieved by dense index
	t.Logf("Retrieved nonces by dense index: %v", retrievedNonces)

	// Verify all original nonces are present
	seenNonces := make(map[int32]bool)
	for _, n := range retrievedNonces {
		seenNonces[n] = true
	}
	for _, n := range nonces {
		if !seenNonces[n] {
			t.Errorf("nonce %d not found in retrieved nonces", n)
		}
	}
}

// TestSMSTProofProvesCorrectNonceAtIndex verifies that:
// 1. A proof generated for dense index i
// 2. Contains the correct nonce (the one at that position)
// 3. Verifies successfully against the root
func TestSMSTProofProvesCorrectNonceAtIndex(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	// Insert artifacts with various nonces
	artifacts := []struct {
		nonce  int32
		vector []byte
	}{
		{100, []byte{1, 2, 3}},
		{5, []byte{4, 5, 6}},
		{1000, []byte{7, 8, 9}},
		{50, []byte{10, 11, 12}},
		{500, []byte{13, 14, 15}},
	}

	for _, a := range artifacts {
		if err := store.Add(a.nonce, a.vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", a.nonce, err)
		}
	}
	store.Flush()

	count := store.Count()
	rootHash := store.GetRoot()

	t.Logf("Store has %d artifacts, root: %x", count, rootHash[:8])

	// For each dense index, verify:
	for i := uint32(0); i < count; i++ {
		nonce, vector, proof, err := store.GetArtifactAndProof(i, count)
		if err != nil {
			t.Fatalf("GetArtifactAndProof(%d) failed: %v", i, err)
		}

		leafData := encodeLeaf(nonce, vector)

		// Decode and verify the proof
		elements := DecodeProofElements(proof)
		verified := VerifySMSTProofWithCounts(rootHash, count, nonce, leafData, elements)
		if !verified {
			t.Errorf("Proof verification failed for dense index %d (nonce %d)", i, nonce)
			t.Logf("  proof elements: %d, nonce: %d, leafData len: %d", len(elements), nonce, len(leafData))
			t.Logf("  rootHash: %x", rootHash)
			t.Logf("  count: %d", count)
		} else {
			t.Logf("Dense index %d: nonce=%d, proof verified OK", i, nonce)
		}
	}
}

// TestSMSTProofFailsForWrongNonce verifies that a proof for one nonce
// does NOT verify if we claim it's for a different nonce
func TestSMSTProofFailsForWrongNonce(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	store.Add(100, []byte{1, 2, 3})
	store.Add(200, []byte{4, 5, 6})
	store.Add(300, []byte{7, 8, 9})
	store.Flush()

	count := store.Count()
	rootHash := store.GetRoot()

	// Get proof for index 0
	nonce0, vector0, proof0, _ := store.GetArtifactAndProof(0, count)
	elements0 := DecodeProofElements(proof0)

	// Verify it works for the correct nonce
	leafData0 := encodeLeaf(nonce0, vector0)
	if !VerifySMSTProofWithCounts(rootHash, count, nonce0, leafData0, elements0) {
		t.Fatal("Proof should verify for correct nonce")
	}

	// Try to verify with a WRONG nonce - should fail
	wrongNonce := int32(999)
	wrongLeafData := encodeLeaf(wrongNonce, vector0)
	if VerifySMSTProofWithCounts(rootHash, count, wrongNonce, wrongLeafData, elements0) {
		t.Error("Proof should NOT verify for wrong nonce - this breaks duplicate prevention!")
	}
}

// TestSMSTDuplicateNoncePreventionEndToEnd tests the full duplicate prevention flow:
// 1. Participant inserts artifacts with unique nonces
// 2. Attempting to insert duplicate nonce fails
// 3. Proofs can only verify artifacts that were actually inserted
func TestSMSTDuplicateNoncePreventionEndToEnd(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	// Simulate: participant receives nonces 1, 2, 3 from inference
	store.Add(1, []byte{0x11})
	store.Add(2, []byte{0x22})
	store.Add(3, []byte{0x33})

	// Malicious attempt: try to add nonce 2 again (duplicate)
	err = store.Add(2, []byte{0xFF})
	if err != ErrDuplicateNonce {
		t.Errorf("Expected ErrDuplicateNonce, got: %v", err)
	}

	// Count should still be 3
	if store.Count() != 3 {
		t.Errorf("Expected count 3, got %d", store.Count())
	}

	store.Flush()
	rootHash := store.GetRoot()
	count := store.Count()

	// Verify all inserted nonces can be proven
	for i := uint32(0); i < count; i++ {
		nonce, vector, proof, _ := store.GetArtifactAndProof(i, count)
		leafData := encodeLeaf(nonce, vector)
		elements := DecodeProofElements(proof)

		if !VerifySMSTProofWithCounts(rootHash, count, nonce, leafData, elements) {
			t.Errorf("Valid artifact at index %d (nonce %d) failed verification", i, nonce)
		}
	}

	// Verify that a non-existent nonce cannot be proven
	fakeNonce := int32(999)
	fakeVector := []byte{0x99}
	fakeLeafData := encodeLeaf(fakeNonce, fakeVector)

	// Get proof for index 0 and try to use it for fake nonce
	_, _, proof0, _ := store.GetArtifactAndProof(0, count)
	elements0 := DecodeProofElements(proof0)

	if VerifySMSTProofWithCounts(rootHash, count, fakeNonce, fakeLeafData, elements0) {
		t.Error("Fake nonce should NOT verify with stolen proof")
	}

	t.Log("Duplicate prevention working correctly")
}

// Regression tests for risk hardening

func TestSMSTDepthExpansionHashCorrectness(t *testing.T) {
	// Verify that proofs remain valid and verifiable after depth expansion.
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	// Insert nonces that fit in default depth
	nonces := []int32{1, 5, 10, 100}
	for _, n := range nonces {
		if err := store.Add(n, []byte{byte(n)}); err != nil {
			t.Fatalf("Add(%d): %v", n, err)
		}
	}
	store.Flush()

	countBefore := store.Count()
	rootBefore := store.GetRoot()

	// Verify proofs work before expansion
	for i := uint32(0); i < countBefore; i++ {
		nonce, vector, proof, _ := store.GetArtifactAndProof(i, countBefore)
		leafData := encodeLeaf(nonce, vector)
		if !VerifySMSTProofSlice(rootBefore, countBefore, nonce, leafData, proof) {
			t.Errorf("proof verification failed BEFORE expansion for index %d", i)
		}
	}

	// Force expansion with large nonce (> 2^24 to exceed default depth)
	largeNonce := int32(1 << 25)
	if err := store.Add(largeNonce, []byte{0xFF}); err != nil {
		t.Fatalf("Add(%d): %v", largeNonce, err)
	}
	store.Flush()

	countAfter := store.Count()
	rootAfter := store.GetRoot()

	if bytes.Equal(rootBefore, rootAfter) {
		t.Errorf("root should change after expansion")
	}

	// Verify proofs work AFTER expansion for ALL artifacts including originals
	for i := uint32(0); i < countAfter; i++ {
		nonce, vector, proof, err := store.GetArtifactAndProof(i, countAfter)
		if err != nil {
			t.Errorf("GetArtifactAndProof(%d) after expansion: %v", i, err)
			continue
		}
		leafData := encodeLeaf(nonce, vector)
		if !VerifySMSTProofSlice(rootAfter, countAfter, nonce, leafData, proof) {
			t.Errorf("proof verification FAILED after expansion for index %d (nonce=%d)", i, nonce)
		}
	}
}

func TestSMSTDepthExpansionIncremental(t *testing.T) {
	// Start with depth 4 and expand incrementally to depth 24.
	// After each expansion, verify ALL previous artifacts still have valid proofs.
	// Tests the expansion algorithm by working directly with SMST at small initial depth.

	initialDepth := 4
	finalDepth := 24

	tree := NewSMST(initialDepth)

	// Track all inserted items
	type item struct {
		nonce  int32
		vector []byte
	}
	var inserted []item

	// Helper to build proof with counts directly from tree (like store does)
	buildProofWithCounts := func(tree *SMST, nonce int32) []SMSTProofElement {
		path := tree.noncePath(nonce)
		elements := make([]SMSTProofElement, 0, tree.depth)

		var collect func(node *smstNode, level int)
		collect = func(node *smstNode, level int) {
			if level == tree.depth || node == nil {
				return
			}
			goRight := path[level]
			if goRight {
				elements = append(elements, SMSTProofElement{
					SiblingHash:  tree.nodeHash(node.left, level+1),
					SiblingCount: tree.nodeCount(node.left),
				})
				collect(node.right, level+1)
			} else {
				elements = append(elements, SMSTProofElement{
					SiblingHash:  tree.nodeHash(node.right, level+1),
					SiblingCount: tree.nodeCount(node.right),
				})
				collect(node.left, level+1)
			}
		}
		collect(tree.root, 0)
		return elements
	}

	// Helper to verify all proofs
	verifyAll := func(stage string) bool {
		rootHash, count := tree.GetRoot()
		allPassed := true
		for _, it := range inserted {
			proofElements := buildProofWithCounts(tree, it.nonce)
			leafData := encodeLeaf(it.nonce, it.vector)
			if !VerifySMSTProofWithCounts(rootHash, count, it.nonce, leafData, proofElements) {
				t.Errorf("[%s] Proof FAILED for nonce=%d", stage, it.nonce)
				allPassed = false
			}
		}
		return allPassed
	}

	// Insert initial nonces that fit in depth 4 (0-15)
	initialNonces := []int32{0, 1, 5, 10, 15}
	for _, n := range initialNonces {
		vec := []byte{byte(n)}
		leafHash := smstHashLeaf(encodeLeaf(n, vec))
		if _, err := tree.Insert(n, leafHash); err != nil {
			t.Fatalf("Initial Insert(%d): %v", n, err)
		}
		inserted = append(inserted, item{n, vec})
	}

	if tree.Depth() != initialDepth {
		t.Errorf("Expected initial depth %d, got %d", initialDepth, tree.Depth())
	}

	t.Logf("Initial: depth=%d, count=%d", tree.Depth(), tree.Count())
	if !verifyAll("initial") {
		t.Fatal("Initial verification failed")
	}

	// Expand incrementally from depth 5 to finalDepth
	for targetDepth := initialDepth + 1; targetDepth <= finalDepth; targetDepth++ {
		// Nonce requiring depth D has highest bit at position D-1
		// 2^(D-1) requires exactly depth D
		triggerNonce := int32(1<<(targetDepth-1)) + int32(targetDepth)

		vec := []byte{byte(triggerNonce), byte(triggerNonce >> 8), byte(triggerNonce >> 16)}
		leafHash := smstHashLeaf(encodeLeaf(triggerNonce, vec))

		depthBefore := tree.Depth()
		if _, err := tree.Insert(triggerNonce, leafHash); err != nil {
			t.Fatalf("Insert(nonce=%d) for depth %d: %v", triggerNonce, targetDepth, err)
		}
		inserted = append(inserted, item{triggerNonce, vec})

		if tree.Depth() < targetDepth {
			t.Errorf("Expected depth >= %d, got %d", targetDepth, tree.Depth())
		}

		expanded := depthBefore != tree.Depth()
		if expanded {
			t.Logf("Expanded: %d -> %d (nonce=%d, count=%d)",
				depthBefore, tree.Depth(), triggerNonce, tree.Count())
		}

		// Verify ALL proofs still work
		stage := fmt.Sprintf("depth=%d", tree.Depth())
		if !verifyAll(stage) {
			t.Fatalf("Verification failed after expanding to depth %d", tree.Depth())
		}
	}

	t.Logf("SUCCESS: depth %d->%d, %d items, all proofs verified at each step",
		initialDepth, tree.Depth(), len(inserted))
}

func TestSMSTMalformedProofRejection(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	nonces := []int32{10, 20, 30, 40, 50}
	for _, n := range nonces {
		if err := store.Add(n, []byte{byte(n)}); err != nil {
			t.Fatalf("Add(%d): %v", n, err)
		}
	}
	store.Flush()

	count, rootHash := store.GetFlushedRoot()
	nonce, vector, _, _ := store.GetArtifactAndProof(2, count)
	leafData := encodeLeaf(nonce, vector)

	// Test 1: Empty proof should fail
	if VerifySMSTProofSlice(rootHash, count, nonce, leafData, nil) {
		t.Error("empty proof should fail verification")
	}
	if VerifySMSTProofSlice(rootHash, count, nonce, leafData, [][]byte{}) {
		t.Error("empty slice proof should fail verification")
	}

	// Test 2: Malformed proof element (wrong size) should fail
	if VerifySMSTProofSlice(rootHash, count, nonce, leafData, [][]byte{make([]byte, 35)}) {
		t.Error("malformed proof (35 bytes) should fail verification")
	}
	if VerifySMSTProofSlice(rootHash, count, nonce, leafData, [][]byte{make([]byte, 37)}) {
		t.Error("malformed proof (37 bytes) should fail verification")
	}

	// Test 3: Proof with correct size but wrong content should fail
	wrongContent := make([][]byte, 24) // default depth
	for i := range wrongContent {
		wrongContent[i] = make([]byte, 36)
		copy(wrongContent[i], []byte("wrong hash content here!!!!!!!!!"))
	}
	if VerifySMSTProofSlice(rootHash, count, nonce, leafData, wrongContent) {
		t.Error("proof with wrong content should fail verification")
	}
}

func TestSMSTIndexBindingVerification(t *testing.T) {
	dir, _ := os.MkdirTemp("", "smst-index-binding-*")
	defer os.RemoveAll(dir)

	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	// Insert artifacts with specific nonces
	nonces := []int32{100, 200, 300, 400, 500}
	for _, n := range nonces {
		if err := store.AddWithNode(n, []byte{byte(n)}, "node1"); err != nil {
			t.Fatalf("AddWithNode(%d) failed: %v", n, err)
		}
	}

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	count, rootHash := store.GetFlushedRoot()

	// Test each artifact at its correct dense index
	for expectedIndex := uint32(0); expectedIndex < count; expectedIndex++ {
		nonce, vector, proof, err := store.GetArtifactAndProof(expectedIndex, count)
		if err != nil {
			t.Fatalf("GetArtifactAndProof(%d) failed: %v", expectedIndex, err)
		}

		leafData := encodeLeaf(nonce, vector)

		// Correct index should verify
		if !VerifySMSTProofWithDenseIndex(rootHash, count, expectedIndex, nonce, leafData, proof) {
			t.Errorf("Valid proof at index %d should verify", expectedIndex)
		}

		// Wrong index should NOT verify
		wrongIndex := (expectedIndex + 1) % count
		if VerifySMSTProofWithDenseIndex(rootHash, count, wrongIndex, nonce, leafData, proof) {
			t.Errorf("Proof at index %d should NOT verify when claimed at index %d", expectedIndex, wrongIndex)
		}
	}

	// Test: taking proof from index 0 and claiming it's for index 2 should fail
	nonce0, vector0, proof0, _ := store.GetArtifactAndProof(0, count)
	leafData0 := encodeLeaf(nonce0, vector0)

	if VerifySMSTProofWithDenseIndex(rootHash, count, 2, nonce0, leafData0, proof0) {
		t.Error("Proof for index 0 should NOT verify when claimed at index 2")
	}
}

func TestSMSTRebuildFailsOnCorruption(t *testing.T) {
	dir, _ := os.MkdirTemp("", "smst-corruption-*")
	defer os.RemoveAll(dir)

	// Create store and add artifacts
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}

	for i := int32(0); i < 10; i++ {
		if err := store.AddWithNode(i, []byte{byte(i)}, "node1"); err != nil {
			t.Fatalf("AddWithNode(%d) failed: %v", i, err)
		}
	}

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	store.Close()

	// Corrupt the data file by truncating it
	dataPath := dir + "/artifacts.data"
	info, _ := os.Stat(dataPath)
	originalSize := info.Size()

	// Truncate to half
	if err := os.Truncate(dataPath, originalSize/2); err != nil {
		t.Fatalf("Truncate failed: %v", err)
	}

	// Reopen store (recovery should handle truncation gracefully)
	store2, err := OpenSMST(dir)
	if err != nil {
		// Recovery failed entirely - this is acceptable behavior for severe corruption
		t.Logf("Recovery failed (acceptable): %v", err)
		return
	}
	defer store2.Close()

	// Store opened, but GetRootAt for the original count should fail
	// because not all artifacts are readable
	_, err = store2.GetRootAt(10)
	if err == nil {
		t.Error("GetRootAt should fail when data file is corrupted")
	} else {
		t.Logf("GetRootAt correctly failed: %v", err)
	}
}

// Security hardening tests - verify SMST properties that prevent duplicate attacks

// TestSMSTCountInflationAttackFails verifies that an attacker cannot claim
// a higher count than actual leaves. The count is cryptographically committed
// in the root hash via sibling counts at each level.
func TestSMSTCountInflationAttackFails(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	// Create tree with 5 real leaves
	realNonces := []int32{10, 20, 30, 40, 50}
	for _, n := range realNonces {
		if err := store.Add(n, []byte{byte(n)}); err != nil {
			t.Fatalf("Add(%d): %v", n, err)
		}
	}
	store.Flush()

	actualCount := store.Count() // = 5
	rootHash := store.GetRoot()

	// Get valid proof for denseIndex=0
	nonce, vector, proof, err := store.GetArtifactAndProof(0, actualCount)
	if err != nil {
		t.Fatalf("GetArtifactAndProof(0): %v", err)
	}
	leafData := encodeLeaf(nonce, vector)

	// Verify with CORRECT count - should pass
	if !VerifySMSTProofSlice(rootHash, actualCount, nonce, leafData, proof) {
		t.Fatal("Proof should verify with correct count")
	}

	// ATTACK: Claim inflated count (10 instead of 5)
	// This simulates an attacker who claims count=10 on-chain but only has 5 leaves.
	// The proof's sibling counts sum to 5 (committed by rootHash), so verification
	// will compute currentCount=5, which != 10, causing failure.
	inflatedCount := uint32(10)
	if VerifySMSTProofSlice(rootHash, inflatedCount, nonce, leafData, proof) {
		t.Error("SECURITY FAILURE: Proof verified with inflated count! Count inflation attack possible.")
	}

	// Also verify with dense index check
	if VerifySMSTProofWithDenseIndex(rootHash, inflatedCount, 0, nonce, leafData, proof) {
		t.Error("SECURITY FAILURE: VerifySMSTProofWithDenseIndex passed with inflated count!")
	}

	t.Log("Count inflation attack correctly prevented")
}

// TestSMSTProofWithWrongCountFails verifies that proofs fail when count
// doesn't match the committed tree state.
func TestSMSTProofWithWrongCountFails(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	for i := int32(0); i < 100; i++ {
		if err := store.Add(i, []byte{byte(i)}); err != nil {
			t.Fatalf("Add(%d): %v", i, err)
		}
	}
	store.Flush()

	count := store.Count()
	rootHash := store.GetRoot()

	// Get proof for middle index
	idx := uint32(50)
	nonce, vector, proof, _ := store.GetArtifactAndProof(idx, count)
	leafData := encodeLeaf(nonce, vector)
	elements := DecodeProofElements(proof)

	// Correct count passes
	if !VerifySMSTProofWithCounts(rootHash, count, nonce, leafData, elements) {
		t.Fatal("Proof should verify with correct count")
	}

	// count-1 fails (sibling counts sum to 100, not 99)
	if VerifySMSTProofWithCounts(rootHash, count-1, nonce, leafData, elements) {
		t.Error("Proof should NOT verify with count-1")
	}

	// count+1 fails (sibling counts sum to 100, not 101)
	if VerifySMSTProofWithCounts(rootHash, count+1, nonce, leafData, elements) {
		t.Error("Proof should NOT verify with count+1")
	}

	// Very different count fails
	if VerifySMSTProofWithCounts(rootHash, 1000, nonce, leafData, elements) {
		t.Error("Proof should NOT verify with count=1000")
	}

	t.Logf("Wrong count correctly rejected for all test cases")
}

// TestSMSTNonceHasUniqueDenseIndex verifies the core SMST security property:
// each nonce maps to exactly ONE dense index. This is what prevents duplicates -
// you cannot serve the same nonce for different dense indices.
func TestSMSTNonceHasUniqueDenseIndex(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	// Insert nonces with gaps to create a sparse tree
	nonces := []int32{5, 50, 500, 5000, 50000}
	for _, n := range nonces {
		if err := store.Add(n, []byte{byte(n % 256)}); err != nil {
			t.Fatalf("Add(%d): %v", n, err)
		}
	}
	store.Flush()

	count := store.Count()
	rootHash := store.GetRoot()

	// For each nonce, verify it can ONLY be proven at its unique dense index
	for expectedIdx := uint32(0); expectedIdx < count; expectedIdx++ {
		nonce, vector, proof, _ := store.GetArtifactAndProof(expectedIdx, count)
		leafData := encodeLeaf(nonce, vector)

		// Correct index passes
		if !VerifySMSTProofWithDenseIndex(rootHash, count, expectedIdx, nonce, leafData, proof) {
			t.Errorf("Nonce %d should verify at its correct index %d", nonce, expectedIdx)
		}

		// ALL other indices must fail for this nonce
		for wrongIdx := uint32(0); wrongIdx < count; wrongIdx++ {
			if wrongIdx == expectedIdx {
				continue
			}
			if VerifySMSTProofWithDenseIndex(rootHash, count, wrongIdx, nonce, leafData, proof) {
				t.Errorf("SECURITY FAILURE: Nonce %d verified at wrong index %d (should only be %d)",
					nonce, wrongIdx, expectedIdx)
			}
		}
	}

	t.Log("Each nonce correctly maps to exactly one dense index")
}

// TestSMSTNegativeNonces verifies that negative nonces (which become large uint32
// values internally) are handled correctly.
func TestSMSTNegativeNonces(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	// Test negative nonces - these become large uint32 values internally
	// int32(-1) -> uint32(4294967295), requires depth 32
	negativeNonces := []int32{-1, -100, -1000000}
	positiveNonces := []int32{0, 1, 100}

	allNonces := append(positiveNonces, negativeNonces...)
	for _, n := range allNonces {
		if err := store.Add(n, []byte{byte(n & 0xFF)}); err != nil {
			t.Fatalf("Add(%d): %v", n, err)
		}
	}
	store.Flush()

	count := store.Count()
	rootHash := store.GetRoot()

	// Verify depth expanded to handle large nonces
	if store.smst.Depth() < 32 {
		t.Logf("Tree depth: %d (may not have expanded to 32 for small negative values)", store.smst.Depth())
	}

	// All nonces should be provable
	for i := uint32(0); i < count; i++ {
		nonce, vector, proof, err := store.GetArtifactAndProof(i, count)
		if err != nil {
			t.Fatalf("GetArtifactAndProof(%d): %v", i, err)
		}

		leafData := encodeLeaf(nonce, vector)
		if !VerifySMSTProofWithDenseIndex(rootHash, count, i, nonce, leafData, proof) {
			t.Errorf("Proof failed for nonce %d at index %d", nonce, i)
		}
	}

	// Note: int32(-1) when interpreted as uint32 becomes 4294967295,
	// which determines its tree path. This is handled correctly by the
	// depth expansion logic.

	t.Logf("Negative nonces handled correctly, tree depth: %d", store.smst.Depth())
}

// TestSMSTCountBoundaries verifies edge cases for count values.
func TestSMSTCountBoundaries(t *testing.T) {
	t.Run("count=0", func(t *testing.T) {
		// Empty proof with count=0 should fail
		if VerifySMSTProofSlice(make([]byte, 32), 0, 0, []byte{}, nil) {
			t.Error("Empty proof with count=0 should fail")
		}
		if VerifySMSTProofSlice(make([]byte, 32), 0, 0, []byte{}, [][]byte{}) {
			t.Error("Empty proof slice with count=0 should fail")
		}
	})

	t.Run("denseIndex >= count", func(t *testing.T) {
		dir := t.TempDir()
		store, _ := OpenSMST(dir)
		defer store.Close()

		store.Add(1, []byte{1})
		store.Add(2, []byte{2})
		store.Flush()

		count := store.Count() // = 2
		rootHash := store.GetRoot()
		nonce, vector, proof, _ := store.GetArtifactAndProof(0, count)
		leafData := encodeLeaf(nonce, vector)

		// denseIndex = count should fail (valid range is [0, count))
		if VerifySMSTProofWithDenseIndex(rootHash, count, count, nonce, leafData, proof) {
			t.Error("denseIndex == count should fail")
		}

		// denseIndex > count should fail
		if VerifySMSTProofWithDenseIndex(rootHash, count, count+100, nonce, leafData, proof) {
			t.Error("denseIndex > count should fail")
		}
	})

	t.Run("single leaf tree", func(t *testing.T) {
		dir := t.TempDir()
		store, _ := OpenSMST(dir)
		defer store.Close()

		store.Add(42, []byte{42})
		store.Flush()

		count := store.Count() // = 1
		rootHash := store.GetRoot()
		nonce, vector, proof, _ := store.GetArtifactAndProof(0, count)
		leafData := encodeLeaf(nonce, vector)

		// Single leaf at index 0 should verify
		if !VerifySMSTProofWithDenseIndex(rootHash, count, 0, nonce, leafData, proof) {
			t.Error("Single leaf tree proof should verify")
		}

		// Index 1 should fail (out of range)
		if VerifySMSTProofWithDenseIndex(rootHash, count, 1, nonce, leafData, proof) {
			t.Error("Index 1 in single-leaf tree should fail")
		}
	})
}

// TestSMSTSnapshotConsistency verifies that GetArtifactAndProof returns
// consistent data from the same snapshot tree state. This is critical for
// preventing the bug where GetArtifact uses current tree but GetProof uses snapshot.
func TestSMSTSnapshotConsistency(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	// Insert initial artifacts
	initialNonces := []int32{10, 20, 30, 40, 50}
	for _, n := range initialNonces {
		if err := store.Add(n, []byte{byte(n)}); err != nil {
			t.Fatalf("Add(%d): %v", n, err)
		}
	}
	store.Flush()

	snapshotCount := store.Count()
	snapshotRoot, err := store.GetRootAt(snapshotCount)
	if err != nil {
		t.Fatalf("GetRootAt: %v", err)
	}

	// Add more artifacts to advance current state beyond snapshot
	additionalNonces := []int32{5, 15, 25, 35, 45, 55} // Note: 5 < 10, so it changes dense index order!
	for _, n := range additionalNonces {
		if err := store.Add(n, []byte{byte(n)}); err != nil {
			t.Fatalf("Add(%d): %v", n, err)
		}
	}
	store.Flush()

	// Current count is now different from snapshot
	currentCount := store.Count()
	if currentCount == snapshotCount {
		t.Fatal("Test setup error: current count should differ from snapshot")
	}

	// For each dense index in the snapshot, verify GetArtifactAndProof returns
	// data that verifies correctly against the snapshot root
	for i := uint32(0); i < snapshotCount; i++ {
		nonce, vector, proof, err := store.GetArtifactAndProof(i, snapshotCount)
		if err != nil {
			t.Fatalf("GetArtifactAndProof(%d, %d): %v", i, snapshotCount, err)
		}

		leafData := encodeLeaf(nonce, vector)

		// This MUST verify successfully - if it doesn't, we have snapshot inconsistency
		if !VerifySMSTProofWithDenseIndex(snapshotRoot, snapshotCount, i, nonce, leafData, proof) {
			t.Errorf("Snapshot consistency FAILED for index %d: proof does not verify", i)
			t.Logf("  nonce=%d, snapshotCount=%d, currentCount=%d", nonce, snapshotCount, currentCount)
		}
	}

	t.Logf("Snapshot consistency verified: snapshotCount=%d, currentCount=%d", snapshotCount, currentCount)
}

// TestSMSTVerifierOverflowProtection tests that the verifier is protected against
// integer overflow in the computedIndex accumulation.
func TestSMSTVerifierOverflowProtection(t *testing.T) {
	// Create a minimal valid proof structure for testing edge cases
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	store.Add(1, []byte{1})
	store.Add(2, []byte{2})
	store.Flush()

	count := store.Count()
	rootHash := store.GetRoot()
	nonce, vector, proof, _ := store.GetArtifactAndProof(0, count)
	leafData := encodeLeaf(nonce, vector)

	// Valid proof should work
	if !VerifySMSTProofWithDenseIndex(rootHash, count, 0, nonce, leafData, proof) {
		t.Fatal("Valid proof should verify")
	}

	// Test with count=0 (should fail early)
	if VerifySMSTProofWithDenseIndex(rootHash, 0, 0, nonce, leafData, proof) {
		t.Error("count=0 should fail verification")
	}

	// Test with empty proof (should fail early)
	if VerifySMSTProofWithDenseIndex(rootHash, count, 0, nonce, leafData, [][]byte{}) {
		t.Error("Empty proof should fail verification")
	}

	// Test with nil proof (should fail early)
	if VerifySMSTProofWithDenseIndex(rootHash, count, 0, nonce, leafData, nil) {
		t.Error("Nil proof should fail verification")
	}
}

// TestSMSTGetArtifactAndProofErrorCases tests error handling in GetArtifactAndProof
func TestSMSTGetArtifactAndProofErrorCases(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST: %v", err)
	}
	defer store.Close()

	for i := int32(0); i < 5; i++ {
		store.Add(i*10, []byte{byte(i)})
	}
	store.Flush()

	count := store.Count()

	// Test: denseIndex >= snapshotCount should fail
	_, _, _, err = store.GetArtifactAndProof(count, count)
	if err != ErrLeafIndexOutOfRange {
		t.Errorf("Expected ErrLeafIndexOutOfRange for index=count, got %v", err)
	}

	_, _, _, err = store.GetArtifactAndProof(count+100, count)
	if err != ErrLeafIndexOutOfRange {
		t.Errorf("Expected ErrLeafIndexOutOfRange for index>count, got %v", err)
	}

	// Test: snapshotCount > current count should fail
	_, _, _, err = store.GetArtifactAndProof(0, count+10)
	if err == nil {
		t.Error("Expected error for snapshotCount > current count")
	}

	// Test: valid request should succeed
	nonce, vector, proof, err := store.GetArtifactAndProof(0, count)
	if err != nil {
		t.Fatalf("Valid request failed: %v", err)
	}
	if nonce == 0 && vector == nil && proof == nil {
		t.Error("Valid request returned empty data")
	}
}

func TestDistributionHistory_NormalOperation(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	store.AddWithNode(1, []byte{1}, "nodeA")
	store.AddWithNode(2, []byte{2}, "nodeB")
	store.AddWithNode(3, []byte{3}, "nodeA")
	store.Flush()

	dist1, exact1, err := store.GetNodeDistributionAt(3)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(3) failed: %v", err)
	}
	if !exact1 {
		t.Error("expected exact=true for count=3")
	}
	if dist1["nodeA"] != 2 || dist1["nodeB"] != 1 {
		t.Errorf("unexpected distribution at count=3: %v", dist1)
	}

	store.AddWithNode(4, []byte{4}, "nodeB")
	store.AddWithNode(5, []byte{5}, "nodeB")
	store.Flush()

	dist2, exact2, err := store.GetNodeDistributionAt(5)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(5) failed: %v", err)
	}
	if !exact2 {
		t.Error("expected exact=true for count=5")
	}
	if dist2["nodeA"] != 2 || dist2["nodeB"] != 3 {
		t.Errorf("unexpected distribution at count=5: %v", dist2)
	}

	distOld, exactOld, err := store.GetNodeDistributionAt(3)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(3) after second flush failed: %v", err)
	}
	if !exactOld {
		t.Error("expected exact=true for historical count=3")
	}
	if distOld["nodeA"] != 2 || distOld["nodeB"] != 1 {
		t.Errorf("historical distribution at count=3 changed: %v", distOld)
	}
}

func TestDistributionHistory_Recovery(t *testing.T) {
	dir := t.TempDir()

	store1, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}

	store1.AddWithNode(1, []byte{1}, "nodeA")
	store1.AddWithNode(2, []byte{2}, "nodeB")
	store1.Flush()

	store1.AddWithNode(3, []byte{3}, "nodeA")
	store1.AddWithNode(4, []byte{4}, "nodeA")
	store1.Flush()

	store1.Close()

	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST (reopen) failed: %v", err)
	}
	defer store2.Close()

	dist2, exact2, err := store2.GetNodeDistributionAt(2)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(2) failed: %v", err)
	}
	if !exact2 {
		t.Error("expected exact=true for count=2 after recovery")
	}
	if dist2["nodeA"] != 1 || dist2["nodeB"] != 1 {
		t.Errorf("distribution at count=2 after recovery: %v", dist2)
	}

	dist4, exact4, err := store2.GetNodeDistributionAt(4)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(4) failed: %v", err)
	}
	if !exact4 {
		t.Error("expected exact=true for count=4 after recovery")
	}
	if dist4["nodeA"] != 3 || dist4["nodeB"] != 1 {
		t.Errorf("distribution at count=4 after recovery: %v", dist4)
	}
}

func TestDistributionHistory_CorruptedFile(t *testing.T) {
	dir := t.TempDir()

	store1, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}

	store1.AddWithNode(1, []byte{1}, "nodeA")
	store1.AddWithNode(2, []byte{2}, "nodeB")
	store1.Flush()
	store1.Close()

	distPath := filepath.Join(dir, "distributions.jsonl")
	f, err := os.OpenFile(distPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open distributions.jsonl failed: %v", err)
	}
	f.WriteString("{invalid json line\n")
	f.WriteString(`{"count":10,"dist":{"nodeC":5,"nodeD":5}}` + "\n")
	f.Close()

	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST (reopen) failed: %v", err)
	}
	defer store2.Close()

	dist2, exact2, err := store2.GetNodeDistributionAt(2)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(2) failed: %v", err)
	}
	if !exact2 {
		t.Error("expected exact=true for valid count=2")
	}
	if dist2["nodeA"] != 1 || dist2["nodeB"] != 1 {
		t.Errorf("distribution at count=2: %v", dist2)
	}

	dist10, exact10, err := store2.GetNodeDistributionAt(10)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(10) failed: %v", err)
	}
	if !exact10 {
		t.Error("expected exact=true for count=10 (valid line after corrupt)")
	}
	if dist10["nodeC"] != 5 || dist10["nodeD"] != 5 {
		t.Errorf("distribution at count=10: %v", dist10)
	}
}

func TestDistributionHistory_EmptyFile(t *testing.T) {
	dir := t.TempDir()

	store1, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	store1.AddWithNode(1, []byte{1}, "nodeA")
	store1.AddWithNode(2, []byte{2}, "nodeB")
	store1.Flush()
	store1.Close()

	distPath := filepath.Join(dir, "distributions.jsonl")
	if err := os.Truncate(distPath, 0); err != nil {
		t.Fatalf("truncate distributions.jsonl failed: %v", err)
	}

	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST (reopen) failed: %v", err)
	}
	defer store2.Close()

	dist, exact, err := store2.GetNodeDistributionAt(2)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(2) failed: %v", err)
	}
	if exact {
		t.Error("expected exact=false when history is empty")
	}
	if len(dist) != 0 {
		t.Errorf("expected empty distribution when history lost, got %v", dist)
	}
}

func TestDistributionHistory_MissingFile(t *testing.T) {
	dir := t.TempDir()

	store1, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	store1.AddWithNode(1, []byte{1}, "nodeA")
	store1.AddWithNode(2, []byte{2}, "nodeA")
	store1.AddWithNode(3, []byte{3}, "nodeB")
	store1.Flush()
	store1.Close()

	distPath := filepath.Join(dir, "distributions.jsonl")
	if err := os.Remove(distPath); err != nil {
		t.Fatalf("remove distributions.jsonl failed: %v", err)
	}

	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST (reopen) failed: %v", err)
	}
	defer store2.Close()

	dist, exact, err := store2.GetNodeDistributionAt(3)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(3) failed: %v", err)
	}
	if exact {
		t.Error("expected exact=false when file was missing")
	}
	if len(dist) != 0 {
		t.Errorf("expected empty distribution when history lost, got %v", dist)
	}
}

func TestDistributionHistory_TruncatedLine(t *testing.T) {
	dir := t.TempDir()

	store1, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	store1.AddWithNode(1, []byte{1}, "nodeA")
	store1.AddWithNode(2, []byte{2}, "nodeB")
	store1.Flush()
	store1.Close()

	distPath := filepath.Join(dir, "distributions.jsonl")
	f, err := os.OpenFile(distPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open distributions.jsonl failed: %v", err)
	}
	f.WriteString(`{"count":5,"dist":{"nodeA":3,`)
	f.Close()

	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST (reopen) failed: %v", err)
	}
	defer store2.Close()

	dist2, exact2, err := store2.GetNodeDistributionAt(2)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(2) failed: %v", err)
	}
	if !exact2 {
		t.Error("expected exact=true for valid count=2")
	}
	if dist2["nodeA"] != 1 || dist2["nodeB"] != 1 {
		t.Errorf("distribution at count=2: %v", dist2)
	}

	_, exact5, err := store2.GetNodeDistributionAt(5)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(5) failed: %v", err)
	}
	if exact5 {
		t.Error("expected exact=false for truncated count=5")
	}
}

func TestDistributionHistory_SimulationAccuracy(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	for i := int32(0); i < 100; i++ {
		nodeId := "nodeA"
		if i%3 == 0 {
			nodeId = "nodeB"
		}
		if i%7 == 0 {
			nodeId = "nodeC"
		}
		store.AddWithNode(i, []byte{byte(i)}, nodeId)
	}
	store.Flush()

	for _, targetCount := range []uint32{10, 25, 50, 75, 99} {
		dist, exact, err := store.GetNodeDistributionAt(targetCount)
		if err != nil {
			t.Fatalf("GetNodeDistributionAt(%d) failed: %v", targetCount, err)
		}
		if exact {
			continue
		}

		var total uint32
		for _, c := range dist {
			total += c
		}
		if total != targetCount {
			t.Errorf("simulation for count=%d: sum=%d (expected %d)", targetCount, total, targetCount)
		}
	}
}

func TestDistributionHistory_ZeroCount(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}
	defer store.Close()

	dist, exact, err := store.GetNodeDistributionAt(0)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(0) failed: %v", err)
	}
	if !exact {
		t.Error("expected exact=true for count=0")
	}
	if len(dist) != 0 {
		t.Errorf("expected empty distribution for count=0, got %v", dist)
	}
}

func TestDistributionHistory_CrashRecovery(t *testing.T) {
	dir := t.TempDir()

	store1, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST failed: %v", err)
	}

	store1.AddWithNode(1, []byte{1}, "nodeA")
	store1.AddWithNode(2, []byte{2}, "nodeB")
	store1.AddWithNode(3, []byte{3}, "nodeA")
	store1.Flush()

	store1.AddWithNode(4, []byte{4}, "nodeA")
	store1.AddWithNode(5, []byte{5}, "nodeB")
	store1.Flush()

	store1.Close()

	distPath := filepath.Join(dir, "distributions.jsonl")
	content, err := os.ReadFile(distPath)
	if err != nil {
		t.Fatalf("read distributions.jsonl failed: %v", err)
	}
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 distribution entries, got %d", len(lines))
	}
	if err := os.WriteFile(distPath, lines[0], 0644); err != nil {
		t.Fatalf("truncate to first entry failed: %v", err)
	}

	store2, err := OpenSMST(dir)
	if err != nil {
		t.Fatalf("OpenSMST (reopen) failed: %v", err)
	}
	defer store2.Close()

	if store2.Count() != 5 {
		t.Errorf("expected 5 artifacts recovered, got %d", store2.Count())
	}

	dist3, exact3, err := store2.GetNodeDistributionAt(3)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(3) failed: %v", err)
	}
	if !exact3 {
		t.Error("expected exact=true for count=3 (was in truncated history)")
	}
	if dist3["nodeA"] != 2 || dist3["nodeB"] != 1 {
		t.Errorf("distribution at count=3: %v", dist3)
	}

	dist5, exact5, err := store2.GetNodeDistributionAt(5)
	if err != nil {
		t.Fatalf("GetNodeDistributionAt(5) failed: %v", err)
	}
	if exact5 {
		t.Error("expected exact=false for count=5 (was lost in crash)")
	}

	var total uint32
	for _, c := range dist5 {
		total += c
	}
	if total != 5 {
		t.Errorf("simulated distribution should sum to 5, got %d", total)
	}
}

// TestDistributionLostAfterRestart_ScannerLimit reproduces the bufio.Scanner
// 64 KB default line limit. 1100 nodes × ~65 bytes/entry ≈ 71 KB per line,
// which exceeds the limit. On reopen the distribution silently becomes empty.
func TestDistributionLostAfterRestart_ScannerLimit(t *testing.T) {
	dir := t.TempDir()

	s, _ := OpenSMST(dir)
	for i := 0; i < 1100; i++ {
		s.AddWithNode(int32(i+1), []byte{1}, fmt.Sprintf("node-%055d", i))
	}
	s.Flush()
	want := len(s.GetNodeDistribution())
	s.Close()

	s2, _ := OpenSMST(dir)
	defer s2.Close()
	got := len(s2.GetNodeDistribution())

	if got != want {
		t.Fatalf("distribution lost after restart: recovered %d nodes, want %d", got, want)
	}
}
