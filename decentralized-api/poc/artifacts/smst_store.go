package artifacts

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const (
	// MaxLeafCount caps artifacts to prevent overflow in size calculations.
	MaxLeafCount = (1 << 30) - 1 // 1,073,741,823
)

var (
	ErrDuplicateNonce      = errors.New("duplicate nonce")
	ErrLeafIndexOutOfRange = errors.New("leaf index out of range")
	ErrStoreClosed         = errors.New("store is closed")
	ErrCapacityExceeded    = errors.New("store capacity exceeded")
)

type bufferedArtifact struct {
	nonce  int32
	vector []byte
	nodeId string
}

// distributionEntry is a single line in distributions.jsonl
type distributionEntry struct {
	Count uint32            `json:"count"`
	Dist  map[string]uint32 `json:"dist"`
}

// SMSTArtifactStore provides artifact storage with SMST commitments.
// Nonce determines tree position, making duplicates impossible by design.
type SMSTArtifactStore struct {
	mu     sync.RWMutex
	dir    string
	closed bool

	dataFile *os.File

	buffer        []bufferedArtifact
	offsets       []uint64         // arrival order -> disk offset
	nonceToOffset map[int32]uint64 // nonce -> disk offset (for fast lookup)
	smst          *SMST

	flushedLeafCount  uint32
	flushedDataOffset uint64
	flushedRoots      map[uint32][]byte

	nodeCounts        map[string]uint32
	flushedNodeCounts map[string]uint32

	distributionHistory map[uint32]map[string]uint32 // count -> distribution snapshot
	distFile            *os.File                     // distributions.jsonl (append-only)

	prebuiltSnapshot *SMST
	prebuiltCount    uint32
}

var _ ArtifactStore = (*SMSTArtifactStore)(nil)

// OpenSMST opens or creates an SMST artifact store in the given directory.
func OpenSMST(dir string) (*SMSTArtifactStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}

	dataPath := filepath.Join(dir, "artifacts.data")
	distPath := filepath.Join(dir, "distributions.jsonl")

	dataFile, err := os.OpenFile(dataPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("open data file: %w", err)
	}

	distFile, err := os.OpenFile(distPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		dataFile.Close()
		return nil, fmt.Errorf("open distributions file: %w", err)
	}

	s := &SMSTArtifactStore{
		dir:                 dir,
		dataFile:            dataFile,
		distFile:            distFile,
		buffer:              make([]bufferedArtifact, 0, 1024),
		offsets:             make([]uint64, 0, 1024),
		nonceToOffset:       make(map[int32]uint64),
		smst:                NewSMST(smstDefaultDepth),
		flushedRoots:        make(map[uint32][]byte),
		nodeCounts:          make(map[string]uint32),
		flushedNodeCounts:   make(map[string]uint32),
		distributionHistory: make(map[uint32]map[string]uint32),
	}

	if err := s.recover(); err != nil {
		s.dataFile.Close()
		s.distFile.Close()
		return nil, fmt.Errorf("recover: %w", err)
	}

	return s, nil
}

func (s *SMSTArtifactStore) recover() error {
	info, err := s.dataFile.Stat()
	if err != nil {
		return fmt.Errorf("stat data file: %w", err)
	}

	if info.Size() == 0 {
		return s.recoverDistributionHistory()
	}

	if _, err := s.dataFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek data file: %w", err)
	}

	var offset uint64
	for {
		nonce, vector, n, err := readArtifact(s.dataFile)
		if err == io.EOF {
			break
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			if truncErr := s.dataFile.Truncate(int64(offset)); truncErr != nil {
				return fmt.Errorf("truncate after partial record: %w", truncErr)
			}
			break
		}
		if err != nil {
			return fmt.Errorf("read artifact at offset %d: %w", offset, err)
		}

		leafData := encodeLeaf(nonce, vector)
		leafHash := smstHashLeaf(leafData)

		if _, err := s.smst.Insert(nonce, leafHash); err != nil {
			return fmt.Errorf("insert nonce %d: %w", nonce, err)
		}

		s.offsets = append(s.offsets, offset)
		s.nonceToOffset[nonce] = offset
		offset += uint64(n)
	}

	s.flushedLeafCount = s.smst.Count()
	s.flushedDataOffset = offset

	rootHash, _ := s.smst.GetRoot()
	s.flushedRoots[s.flushedLeafCount] = rootHash

	if err := s.recoverDistributionHistory(); err != nil {
		log.Printf("warning: failed to recover distribution history: %v", err)
	}

	return nil
}

func (s *SMSTArtifactStore) recoverDistributionHistory() error {
	if _, err := s.distFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek distributions file: %w", err)
	}

	var latestCount uint32
	var latestDist map[string]uint32

	reader := bufio.NewReader(s.distFile)
	lineNum := 0
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("read distributions file: %w", err)
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 {
			lineNum++
			var entry distributionEntry
			if jsonErr := json.Unmarshal(line, &entry); jsonErr != nil {
				log.Printf("warning: skipping corrupted distribution entry at line %d: %v", lineNum, jsonErr)
			} else {
				distCopy := make(map[string]uint32, len(entry.Dist))
				for k, v := range entry.Dist {
					distCopy[k] = v
				}
				s.distributionHistory[entry.Count] = distCopy
				if entry.Count >= latestCount {
					latestCount = entry.Count
					latestDist = distCopy
				}
			}
		}
		if err == io.EOF {
			break
		}
	}

	if latestDist != nil {
		for k, v := range latestDist {
			s.flushedNodeCounts[k] = v
			s.nodeCounts[k] = v
		}
	}

	return nil
}

func (s *SMSTArtifactStore) AddWithNode(nonce int32, vector []byte, nodeId string) error {
	leafData := encodeLeaf(nonce, vector)
	leafHash := smstHashLeaf(leafData)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrStoreClosed
	}

	if s.smst.Count() >= MaxLeafCount {
		return ErrCapacityExceeded
	}

	if _, err := s.smst.Insert(nonce, leafHash); err != nil {
		return err
	}

	s.buffer = append(s.buffer, bufferedArtifact{nonce: nonce, vector: vector, nodeId: nodeId})

	if nodeId != "" {
		s.nodeCounts[nodeId]++
	}

	return nil
}

func (s *SMSTArtifactStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrStoreClosed
	}

	return s.flushLocked()
}

func (s *SMSTArtifactStore) flushLocked() error {
	if len(s.buffer) == 0 {
		return nil
	}

	if _, err := s.dataFile.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek data file: %w", err)
	}

	w := bufio.NewWriter(s.dataFile)
	offset := s.flushedDataOffset

	for _, art := range s.buffer {
		s.offsets = append(s.offsets, offset)
		s.nonceToOffset[art.nonce] = offset

		n, err := writeArtifact(w, art.nonce, art.vector)
		if err != nil {
			return fmt.Errorf("write artifact: %w", err)
		}
		offset += uint64(n)
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush buffer: %w", err)
	}
	if err := s.dataFile.Sync(); err != nil {
		return fmt.Errorf("sync data file: %w", err)
	}

	for k, v := range s.nodeCounts {
		s.flushedNodeCounts[k] = v
	}

	s.flushedLeafCount = s.smst.Count()
	s.flushedDataOffset = offset
	s.buffer = s.buffer[:0]

	rootHash, _ := s.smst.GetRoot()
	s.flushedRoots[s.flushedLeafCount] = rootHash

	if err := s.appendDistributionSnapshot(); err != nil {
		log.Printf("warning: distribution snapshot failed (will use simulation): %v", err)
	}

	return nil
}

func (s *SMSTArtifactStore) appendDistributionSnapshot() error {
	if s.distFile == nil {
		return nil
	}

	distCopy := make(map[string]uint32, len(s.flushedNodeCounts))
	for k, v := range s.flushedNodeCounts {
		distCopy[k] = v
	}

	entry := distributionEntry{
		Count: s.flushedLeafCount,
		Dist:  distCopy,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal distribution entry: %w", err)
	}

	data = append(data, '\n')
	if _, err := s.distFile.Write(data); err != nil {
		return fmt.Errorf("write distribution entry: %w", err)
	}

	if err := s.distFile.Sync(); err != nil {
		return fmt.Errorf("sync distributions file: %w", err)
	}

	s.distributionHistory[s.flushedLeafCount] = distCopy

	return nil
}

func (s *SMSTArtifactStore) getRoot() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.smst.Count() == 0 {
		return nil
	}

	root, _ := s.smst.GetRoot()
	return root
}

func (s *SMSTArtifactStore) GetRootAt(snapshotCount uint32) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, ErrStoreClosed
	}

	if snapshotCount == 0 {
		return nil, nil
	}

	if snapshotCount > s.smst.Count() {
		return nil, fmt.Errorf("snapshot count %d exceeds current count %d", snapshotCount, s.smst.Count())
	}

	if root, ok := s.flushedRoots[snapshotCount]; ok {
		return root, nil
	}

	tree := s.rebuildTreeAt(snapshotCount)
	root, _ := tree.GetRoot()
	return root, nil
}

func (s *SMSTArtifactStore) GetFlushedRoot() (count uint32, root []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.flushedLeafCount == 0 {
		return 0, nil
	}

	if root, ok := s.flushedRoots[s.flushedLeafCount]; ok {
		return s.flushedLeafCount, root
	}

	r, _ := s.smst.GetRoot()
	return s.flushedLeafCount, r
}

func (s *SMSTArtifactStore) Count() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.smst.Count()
}

func (s *SMSTArtifactStore) GetNodeDistribution() map[string]uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]uint32, len(s.flushedNodeCounts))
	for k, v := range s.flushedNodeCounts {
		result[k] = v
	}
	return result
}

func (s *SMSTArtifactStore) GetNodeCounts() map[string]uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]uint32, len(s.nodeCounts))
	for k, v := range s.nodeCounts {
		result[k] = v
	}
	return result
}

func (s *SMSTArtifactStore) GetNodeDistributionAt(count uint32) (map[string]uint32, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if count == 0 {
		return make(map[string]uint32), true, nil
	}

	if dist, ok := s.distributionHistory[count]; ok {
		result := make(map[string]uint32, len(dist))
		for k, v := range dist {
			result[k] = v
		}
		return result, true, nil
	}

	return s.simulateDistribution(count), false, nil
}

func (s *SMSTArtifactStore) simulateDistribution(targetCount uint32) map[string]uint32 {
	if len(s.flushedNodeCounts) == 0 {
		return make(map[string]uint32)
	}

	var totalFlushed uint32
	for _, c := range s.flushedNodeCounts {
		totalFlushed += c
	}

	if totalFlushed == 0 {
		return make(map[string]uint32)
	}

	result := make(map[string]uint32, len(s.flushedNodeCounts))
	var allocated uint32

	nodes := make([]string, 0, len(s.flushedNodeCounts))
	for k := range s.flushedNodeCounts {
		nodes = append(nodes, k)
	}

	for _, nodeId := range nodes {
		proportion := float64(s.flushedNodeCounts[nodeId]) / float64(totalFlushed)
		scaled := uint32(proportion * float64(targetCount))
		result[nodeId] = scaled
		allocated += scaled
	}

	remainder := targetCount - allocated
	if remainder > 0 && len(nodes) > 0 {
		result[nodes[0]] += remainder
	}

	return result
}

func (s *SMSTArtifactStore) getArtifactByNonce(targetNonce int32) (int32, []byte, error) {
	// Check flushed artifacts first (via index)
	if offset, ok := s.nonceToOffset[targetNonce]; ok {
		nonce, vector, _, err := readArtifactAt(s.dataFile, int64(offset))
		if err != nil {
			return 0, nil, fmt.Errorf("read artifact: %w", err)
		}
		return nonce, vector, nil
	}

	// Search in buffer (not yet flushed)
	for _, art := range s.buffer {
		if art.nonce == targetNonce {
			return art.nonce, art.vector, nil
		}
	}

	return 0, nil, ErrLeafIndexOutOfRange
}

// GetArtifactAndProof retrieves both artifact and proof for a dense index at a specific snapshot.
// This ensures the nonce/vector and proof are consistent with the same snapshot tree state,
// preventing the bug where GetArtifact uses current tree but GetProof uses snapshot tree.
func (s *SMSTArtifactStore) GetArtifactAndProof(denseIndex uint32, snapshotCount uint32) (nonce int32, vector []byte, proof [][]byte, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return 0, nil, nil, ErrStoreClosed
	}

	if denseIndex >= snapshotCount {
		return 0, nil, nil, ErrLeafIndexOutOfRange
	}

	if snapshotCount > s.smst.Count() {
		return 0, nil, nil, fmt.Errorf("snapshot count %d exceeds current count %d", snapshotCount, s.smst.Count())
	}

	tree := s.getSnapshotTree(snapshotCount)

	nonce, _, err = tree.GetLeafByDenseIndex(denseIndex)
	if err != nil {
		return 0, nil, nil, err
	}

	// Look up artifact data by nonce (from disk or buffer)
	nonce, vector, err = s.getArtifactByNonce(nonce)
	if err != nil {
		return 0, nil, nil, err
	}

	proofWithCounts := s.buildProofWithCounts(tree, nonce)
	proof = encodeProofForTransport(proofWithCounts)

	return nonce, vector, proof, nil
}

// getSnapshotTree returns the appropriate tree for the given snapshot count.
// Must be called with s.mu held.
func (s *SMSTArtifactStore) getSnapshotTree(snapshotCount uint32) *SMST {
	if snapshotCount == s.smst.Count() {
		return s.smst
	}
	if s.prebuiltSnapshot != nil && s.prebuiltCount == snapshotCount {
		return s.prebuiltSnapshot
	}
	return s.rebuildTreeAt(snapshotCount)
}

func (s *SMSTArtifactStore) buildProofWithCounts(tree *SMST, nonce int32) []smstProofElement {
	path := tree.noncePath(nonce)
	elements := make([]smstProofElement, 0, tree.depth)

	var collectWithCounts func(node *smstNode, level int)
	collectWithCounts = func(node *smstNode, level int) {
		if level == tree.depth || node == nil {
			return
		}

		goRight := path[level]
		if goRight {
			elements = append(elements, smstProofElement{
				siblingHash:  tree.nodeHash(node.left, level+1),
				siblingCount: tree.nodeCount(node.left),
			})
			collectWithCounts(node.right, level+1)
		} else {
			elements = append(elements, smstProofElement{
				siblingHash:  tree.nodeHash(node.right, level+1),
				siblingCount: tree.nodeCount(node.right),
			})
			collectWithCounts(node.left, level+1)
		}
	}

	collectWithCounts(tree.root, 0)
	return elements
}

func encodeProofForTransport(proof []smstProofElement) [][]byte {
	result := make([][]byte, len(proof))
	for i, elem := range proof {
		encoded := make([]byte, 36)
		copy(encoded[:32], elem.siblingHash)
		binary.LittleEndian.PutUint32(encoded[32:], elem.siblingCount)
		result[i] = encoded
	}
	return result
}

func (s *SMSTArtifactStore) rebuildTreeAt(count uint32) *SMST {
	// Seed with the default depth, not the live tree's depth: replaying the
	// first `count` artifacts triggers exactly the depth expansions the tree
	// had when the root for `count` was committed. Using the live depth would
	// bake in expansions caused by artifacts inserted after that commit,
	// producing a root that no longer matches the committed one.
	tree := NewSMST(smstDefaultDepth)

	// Read flushed artifacts from disk
	flushedToRead := count
	if flushedToRead > s.flushedLeafCount {
		flushedToRead = s.flushedLeafCount
	}

	var skipped uint32
	for i := uint32(0); i < flushedToRead; i++ {
		offset := s.offsets[i]
		nonce, vector, _, err := readArtifactAt(s.dataFile, int64(offset))
		if err != nil {
			skipped++
			continue
		}
		leafData := encodeLeaf(nonce, vector)
		leafHash := smstHashLeaf(leafData)
		if _, err := tree.Insert(nonce, leafHash); err != nil {
			log.Printf("[WARN] SMST rebuildTreeAt: insert failed for nonce %d: %v (possible data corruption)", nonce, err)
		}
	}
	if skipped > 0 {
		log.Printf("[WARN] SMST rebuildTreeAt: skipped %d/%d artifacts due to read errors", skipped, flushedToRead)
	}

	// Read remaining from buffer if needed
	if count > s.flushedLeafCount {
		remaining := count - s.flushedLeafCount
		for i := uint32(0); i < remaining && i < uint32(len(s.buffer)); i++ {
			art := s.buffer[i]
			leafData := encodeLeaf(art.nonce, art.vector)
			leafHash := smstHashLeaf(leafData)
			tree.Insert(art.nonce, leafHash)
		}
	}

	return tree
}

// PrebuildSnapshot builds the SMST at the specified count for fast proof queries.
// Should be called after weight distribution is determined.
func (s *SMSTArtifactStore) PrebuildSnapshot(count uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if count > s.smst.Count() {
		return fmt.Errorf("count %d exceeds current count %d", count, s.smst.Count())
	}

	s.prebuiltSnapshot = s.rebuildTreeAt(count)
	s.prebuiltCount = count

	return nil
}

func (s *SMSTArtifactStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true

	if err := s.flushLocked(); err != nil {
		return fmt.Errorf("flush on close: %w", err)
	}

	if err := s.dataFile.Close(); err != nil {
		return fmt.Errorf("close data file: %w", err)
	}

	if s.distFile != nil {
		if err := s.distFile.Close(); err != nil {
			return fmt.Errorf("close distributions file: %w", err)
		}
	}

	return nil
}

func writeArtifact(w io.Writer, nonce int32, vector []byte) (int, error) {
	totalLen := 4 + len(vector)
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[0:4], uint32(totalLen))
	binary.LittleEndian.PutUint32(header[4:8], uint32(nonce))

	n1, err := w.Write(header)
	if err != nil {
		return n1, err
	}

	n2, err := w.Write(vector)
	if err != nil {
		return n1 + n2, err
	}

	return n1 + n2, nil
}

func readArtifact(r io.Reader) (int32, []byte, int, error) {
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, 0, err
	}

	totalLen := binary.LittleEndian.Uint32(header[0:4])
	nonce := int32(binary.LittleEndian.Uint32(header[4:8]))

	vectorLen := totalLen - 4
	vector := make([]byte, vectorLen)
	if _, err := io.ReadFull(r, vector); err != nil {
		return 0, nil, 0, err
	}

	return nonce, vector, 8 + int(vectorLen), nil
}

func readArtifactAt(r io.ReaderAt, offset int64) (int32, []byte, int, error) {
	var header [8]byte
	if _, err := r.ReadAt(header[:], offset); err != nil {
		return 0, nil, 0, err
	}

	totalLen := binary.LittleEndian.Uint32(header[0:4])
	nonce := int32(binary.LittleEndian.Uint32(header[4:8]))

	vectorLen := totalLen - 4
	vector := make([]byte, vectorLen)
	if _, err := r.ReadAt(vector, offset+8); err != nil {
		return 0, nil, 0, err
	}

	return nonce, vector, 8 + int(vectorLen), nil
}

func encodeLeaf(nonce int32, vector []byte) []byte {
	buf := make([]byte, 4+len(vector))
	binary.LittleEndian.PutUint32(buf[:4], uint32(nonce))
	copy(buf[4:], vector)
	return buf
}
