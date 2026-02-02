package artifacts

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	// MaxLeafCount caps artifacts to ensure MMR size calculations stay within int limits.
	// mmrSizeForLeaves(n) = 2n - popcount(n), so 2n must fit in int32: n <= MaxInt32/2.
	MaxLeafCount = (1 << 30) - 1 // 1,073,741,823
)

var (
	ErrDuplicateNonce      = errors.New("duplicate nonce")
	ErrLeafIndexOutOfRange = errors.New("leaf index out of range")
	ErrStoreClosed         = errors.New("store is closed")
	ErrCapacityExceeded    = errors.New("store capacity exceeded")
)

// ArtifactStore provides append-only storage for PoC artifacts with MMR commitments.
//
// Uses single file on disk + in-memory state (offsets, MMR, nonce map).
// On restart, state is rebuilt by reading the data file (~2-3 sec for 1M artifacts, 1 cpu core).
type ArtifactStore struct {
	mu     sync.RWMutex
	dir    string
	closed bool

	dataFile  *os.File // artifacts.data: [LE32 len][LE32 nonce][vector]...
	nodesFile *os.File // nodes.data: JSON map of node_id -> count

	buffer           []bufferedArtifact
	offsets          []uint64
	nonceToLeafIndex map[int32]uint32
	mmrNodes         [][]byte
	nextLeafIndex    uint32

	flushedLeafCount  uint32
	flushedDataOffset uint64

	// Node distribution tracking
	nodeCounts        map[string]uint32 // node_id -> artifact count (in-memory, includes unflushed)
	flushedNodeCounts map[string]uint32 // persisted node counts
}

type bufferedArtifact struct {
	nonce  int32
	vector []byte
	nodeId string
}

func Open(dir string) (*ArtifactStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}

	dataPath := filepath.Join(dir, "artifacts.data")
	nodesPath := filepath.Join(dir, "nodes.json")

	dataFile, err := os.OpenFile(dataPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("open data file: %w", err)
	}

	nodesFile, err := os.OpenFile(nodesPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		dataFile.Close()
		return nil, fmt.Errorf("open nodes file: %w", err)
	}

	s := &ArtifactStore{
		dir:               dir,
		dataFile:          dataFile,
		nodesFile:         nodesFile,
		buffer:            make([]bufferedArtifact, 0, 1024),
		offsets:           make([]uint64, 0, 1024),
		nonceToLeafIndex:  make(map[int32]uint32),
		mmrNodes:          make([][]byte, 0, 1024),
		nodeCounts:        make(map[string]uint32),
		flushedNodeCounts: make(map[string]uint32),
	}

	if err := s.recover(); err != nil {
		s.dataFile.Close()
		s.nodesFile.Close()
		return nil, fmt.Errorf("recover: %w", err)
	}

	return s, nil
}

func (s *ArtifactStore) recover() error {
	info, err := s.dataFile.Stat()
	if err != nil {
		return fmt.Errorf("stat data file: %w", err)
	}

	if info.Size() == 0 {
		return nil
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

		if _, exists := s.nonceToLeafIndex[nonce]; exists {
			return fmt.Errorf("duplicate nonce %d at offset %d", nonce, offset)
		}

		if s.nextLeafIndex >= MaxLeafCount {
			return fmt.Errorf("data file exceeds max leaf count %d", MaxLeafCount)
		}

		s.offsets = append(s.offsets, offset)
		s.nonceToLeafIndex[nonce] = s.nextLeafIndex
		leafHash := hashLeaf(encodeLeaf(nonce, vector))
		appendToMMR(&s.mmrNodes, leafHash, s.nextLeafIndex)
		s.nextLeafIndex++
		offset += uint64(n)
	}

	s.flushedLeafCount = s.nextLeafIndex
	s.flushedDataOffset = offset

	// Recover node counts from nodes.json
	if err := s.recoverNodeCounts(); err != nil {
		return fmt.Errorf("recover node counts: %w", err)
	}

	return nil
}

func (s *ArtifactStore) recoverNodeCounts() error {
	info, err := s.nodesFile.Stat()
	if err != nil {
		return fmt.Errorf("stat nodes file: %w", err)
	}

	if info.Size() == 0 {
		return nil
	}

	if _, err := s.nodesFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek nodes file: %w", err)
	}

	decoder := json.NewDecoder(s.nodesFile)
	if err := decoder.Decode(&s.flushedNodeCounts); err != nil {
		return fmt.Errorf("decode nodes file: %w", err)
	}

	// Copy flushed counts to current counts
	for k, v := range s.flushedNodeCounts {
		s.nodeCounts[k] = v
	}

	return nil
}

// Add appends an artifact if nonce is not already in the store.
// Deprecated: Use AddWithNode to track per-node distribution.
func (s *ArtifactStore) Add(nonce int32, vector []byte) error {
	return s.AddWithNode(nonce, vector, "")
}

// AddWithNode appends an artifact and tracks which node contributed it.
func (s *ArtifactStore) AddWithNode(nonce int32, vector []byte, nodeId string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrStoreClosed
	}

	if s.nextLeafIndex >= MaxLeafCount {
		return ErrCapacityExceeded
	}

	if _, exists := s.nonceToLeafIndex[nonce]; exists {
		return ErrDuplicateNonce
	}

	for _, b := range s.buffer {
		if b.nonce == nonce {
			return ErrDuplicateNonce
		}
	}

	s.nonceToLeafIndex[nonce] = s.nextLeafIndex
	s.buffer = append(s.buffer, bufferedArtifact{nonce: nonce, vector: vector, nodeId: nodeId})

	leafHash := hashLeaf(encodeLeaf(nonce, vector))
	appendToMMR(&s.mmrNodes, leafHash, s.nextLeafIndex)
	s.nextLeafIndex++

	// Track node contribution
	if nodeId != "" {
		s.nodeCounts[nodeId]++
	}

	return nil
}

func (s *ArtifactStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrStoreClosed
	}

	return s.flushLocked()
}

// flushLocked flushes buffered artifacts to disk. Caller must hold s.mu.
func (s *ArtifactStore) flushLocked() error {
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

	// Persist node counts
	if err := s.flushNodeCountsLocked(); err != nil {
		return fmt.Errorf("flush node counts: %w", err)
	}

	s.flushedLeafCount = s.nextLeafIndex
	s.flushedDataOffset = offset
	s.buffer = s.buffer[:0]

	return nil
}

func (s *ArtifactStore) flushNodeCountsLocked() error {
	// Copy current counts to flushed
	for k, v := range s.nodeCounts {
		s.flushedNodeCounts[k] = v
	}

	// Truncate and rewrite
	if err := s.nodesFile.Truncate(0); err != nil {
		return fmt.Errorf("truncate nodes file: %w", err)
	}
	if _, err := s.nodesFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek nodes file: %w", err)
	}

	encoder := json.NewEncoder(s.nodesFile)
	if err := encoder.Encode(s.flushedNodeCounts); err != nil {
		return fmt.Errorf("encode nodes file: %w", err)
	}

	if err := s.nodesFile.Sync(); err != nil {
		return fmt.Errorf("sync nodes file: %w", err)
	}

	return nil
}

// GetRoot returns the current MMR root hash (32 bytes), or nil if empty.
func (s *ArtifactStore) GetRoot() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.nextLeafIndex == 0 {
		return nil
	}

	return bagPeaks(s.mmrNodes, s.nextLeafIndex)
}

// GetRootAt returns the MMR root hash at a specific snapshot count.
// This enables snapshot binding validation: callers can verify that a
// (root_hash, count) pair matches the store's historical state.
// Returns nil if snapshotCount is 0, error if snapshotCount exceeds current count.
func (s *ArtifactStore) GetRootAt(snapshotCount uint32) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, ErrStoreClosed
	}

	if snapshotCount == 0 {
		return nil, nil
	}

	if snapshotCount > s.nextLeafIndex {
		return nil, fmt.Errorf("snapshot count %d exceeds current count %d", snapshotCount, s.nextLeafIndex)
	}

	return bagPeaks(s.mmrNodes, snapshotCount), nil
}

// GetFlushedRoot returns the root and count of ONLY persisted artifacts.
// Safe to report externally - survives process crashes.
func (s *ArtifactStore) GetFlushedRoot() (count uint32, root []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.flushedLeafCount == 0 {
		return 0, nil
	}

	return s.flushedLeafCount, bagPeaks(s.mmrNodes, s.flushedLeafCount)
}

func (s *ArtifactStore) Count() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nextLeafIndex
}

// GetNodeDistribution returns a copy of the flushed node distribution.
func (s *ArtifactStore) GetNodeDistribution() map[string]uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]uint32, len(s.flushedNodeCounts))
	for k, v := range s.flushedNodeCounts {
		result[k] = v
	}
	return result
}

// GetNodeCounts returns a copy of the current (unflushed) node distribution.
// Useful for logging/debugging to see real-time artifact counts per node.
func (s *ArtifactStore) GetNodeCounts() map[string]uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]uint32, len(s.nodeCounts))
	for k, v := range s.nodeCounts {
		result[k] = v
	}
	return result
}

func (s *ArtifactStore) GetArtifact(leafIndex uint32) (nonce int32, vector []byte, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return 0, nil, ErrStoreClosed
	}

	if leafIndex >= s.nextLeafIndex {
		return 0, nil, ErrLeafIndexOutOfRange
	}

	if leafIndex >= s.flushedLeafCount {
		bufIdx := leafIndex - s.flushedLeafCount
		art := s.buffer[bufIdx]
		return art.nonce, art.vector, nil
	}

	offset := s.offsets[leafIndex]
	// Use ReadAt for thread-safe concurrent reads (doesn't modify shared file position)
	nonce, vector, _, err = readArtifactAt(s.dataFile, int64(offset))
	if err != nil {
		return 0, nil, fmt.Errorf("read artifact: %w", err)
	}

	return nonce, vector, nil
}

// GetProof generates a merkle proof for leafIndex at snapshotCount.
func (s *ArtifactStore) GetProof(leafIndex uint32, snapshotCount uint32) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, ErrStoreClosed
	}

	if leafIndex >= snapshotCount {
		return nil, ErrLeafIndexOutOfRange
	}

	if snapshotCount > s.nextLeafIndex {
		return nil, fmt.Errorf("snapshot count %d exceeds current count %d", snapshotCount, s.nextLeafIndex)
	}

	return generateProof(s.mmrNodes, leafIndex, snapshotCount)
}

func (s *ArtifactStore) Close() error {
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

	if err := s.nodesFile.Close(); err != nil {
		return fmt.Errorf("close nodes file: %w", err)
	}

	return nil
}

// writeArtifact format: [LE32 len][LE32 nonce][vector bytes]
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

// readArtifactAt reads an artifact at a specific offset using ReadAt.
// Unlike readArtifact, this is thread-safe for concurrent reads because
// ReadAt doesn't modify the shared file position.
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

// encodeLeaf: LE32(nonce) || vector
func encodeLeaf(nonce int32, vector []byte) []byte {
	buf := make([]byte, 4+len(vector))
	binary.LittleEndian.PutUint32(buf[:4], uint32(nonce))
	copy(buf[4:], vector)
	return buf
}
