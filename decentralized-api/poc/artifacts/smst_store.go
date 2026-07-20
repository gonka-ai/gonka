package artifacts

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

const (
	// MaxLeafCount caps artifacts to prevent overflow in size calculations.
	MaxLeafCount = (1 << 30) - 1 // 1,073,741,823
)

var (
	ErrDuplicateNonce      = errors.New("duplicate nonce")
	ErrLeafIndexOutOfRange = errors.New("leaf index out of range")
	ErrNonceNotFound       = errors.New("nonce not found")
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

// flushedRootEntry is a single line in flushed_roots.jsonl. This is the
// durable record of committed flush counts (and their roots), independent of
// distributions.jsonl — so a failed distribution append cannot make an
// on-chain flush count unprovable after restart.
type flushedRootEntry struct {
	Count uint32 `json:"count"`
	Root  string `json:"root"` // hex-encoded SMST root
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

	// retained holds snapshots at committed counts so historical proofs are
	// O(depth) instead of an O(N) rebuild. With COW: O(1) shared roots from
	// flush. Without COW: deep clones from PrebuildSnapshot/WarmSnapshot while
	// tip still equals that count (under write lock). A miss falls back to the
	// rebuild path (speed only, never correctness).
	retained map[uint32]smstSnapshot

	nodeCounts        map[string]uint32
	flushedNodeCounts map[string]uint32

	distributionHistory map[uint32]map[string]uint32 // count -> distribution snapshot
	distFile            *os.File                     // distributions.jsonl (append-only)
	rootsFile           *os.File                     // flushed_roots.jsonl (append-only)

	// cowEnabled selects insertCOW vs in-place Insert. Default true (SMST_COW
	// unset).
	cowEnabled bool

	// snapshotInMemoryClone: when COW is off and PrebuildSnapshot is called at
	// the live tip, true deep-clones under the write lock into retained; false
	// rebuilds from artifacts into the process cache without holding the write
	// lock (upgrade-v0.2.14 style). Default true (SMST_SNAPSHOT_IN_MEMORY_CLONE
	// unset).
	snapshotInMemoryClone bool
}

var _ ArtifactStore = (*SMSTArtifactStore)(nil)

// OpenSMST opens or creates an SMST artifact store in the given directory.
func OpenSMST(dir string) (*SMSTArtifactStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}

	dataPath := filepath.Join(dir, "artifacts.data")
	distPath := filepath.Join(dir, "distributions.jsonl")
	rootsPath := filepath.Join(dir, "flushed_roots.jsonl")

	dataFile, err := os.OpenFile(dataPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("open data file: %w", err)
	}

	distFile, err := os.OpenFile(distPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		dataFile.Close()
		return nil, fmt.Errorf("open distributions file: %w", err)
	}

	rootsFile, err := os.OpenFile(rootsPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		dataFile.Close()
		distFile.Close()
		return nil, fmt.Errorf("open flushed roots file: %w", err)
	}

	s := &SMSTArtifactStore{
		dir:                 dir,
		dataFile:            dataFile,
		distFile:            distFile,
		rootsFile:           rootsFile,
		buffer:              make([]bufferedArtifact, 0, 1024),
		offsets:             make([]uint64, 0, 1024),
		nonceToOffset:       make(map[int32]uint64),
		smst:                NewSMST(smstDefaultDepth),
		flushedRoots:        make(map[uint32][]byte),
		retained:            make(map[uint32]smstSnapshot),
		nodeCounts:          make(map[string]uint32),
		flushedNodeCounts:   make(map[string]uint32),
		distributionHistory: make(map[uint32]map[string]uint32),
		// Defaults when env unset: COW on, deferred hashing on, tip clone on.
		cowEnabled:            smstCOWEnabledFromEnv(),
		snapshotInMemoryClone: smstSnapshotInMemoryCloneFromEnv(),
	}
	s.smst.deferredHash = smstDeferredHashFromEnv()
	s.smst.parallelHash = smstParallelHashFromEnv()
	if !s.cowEnabled {
		log.Printf("[WARN] SMST_COW=0: non-COW insert path is deprecated; use only for profiling baselines")
	}

	if err := s.recover(); err != nil {
		s.dataFile.Close()
		s.distFile.Close()
		s.rootsFile.Close()
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
		if err := s.recoverFlushedRoots(); err != nil {
			log.Printf("warning: failed to recover flushed roots: %v", err)
		}
		return s.recoverDistributionHistory()
	}

	// Recover durable committed counts before replaying so mid-replay can
	// re-capture retained COW snapshots at each. Prefer flushed_roots.jsonl
	// (authoritative for flush boundaries); also union distributionHistory for
	// stores that predate the roots file.
	if err := s.recoverFlushedRoots(); err != nil {
		log.Printf("warning: failed to recover flushed roots: %v", err)
	}
	if err := s.recoverDistributionHistory(); err != nil {
		log.Printf("warning: failed to recover distribution history: %v", err)
	}
	committed := make(map[uint32]struct{}, len(s.flushedRoots)+len(s.distributionHistory))
	for c := range s.flushedRoots {
		committed[c] = struct{}{}
	}
	for c := range s.distributionHistory {
		committed[c] = struct{}{}
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

		// COW keeps mid-replay retained snapshots valid; in-place Insert is used
		// when SMST_COW=0 (deferred hashing only).
		if _, err := s.insertLeaf(nonce, leafHash); err != nil {
			return fmt.Errorf("insert nonce %d: %w", nonce, err)
		}

		s.offsets = append(s.offsets, offset)
		s.nonceToOffset[nonce] = offset
		offset += uint64(n)

		if _, ok := committed[s.smst.Count()]; ok {
			rootHash, _ := s.smst.GetRoot() // hashes the tree before capture
			if prev, ok := s.flushedRoots[s.smst.Count()]; ok && prev != nil && !bytes.Equal(prev, rootHash) {
				log.Printf("warning: flushed root mismatch at count %d: persisted=%x recomputed=%x",
					s.smst.Count(), prev, rootHash)
			}
			s.flushedRoots[s.smst.Count()] = rootHash
			s.captureRetainedLocked()
		}
	}

	s.flushedLeafCount = s.smst.Count()
	s.flushedDataOffset = offset

	rootHash, _ := s.smst.GetRoot()
	s.flushedRoots[s.flushedLeafCount] = rootHash
	s.captureRetainedLocked()

	// Backfill flushed_roots.jsonl for stores that only had distributionHistory
	// (upgrade path), so a later dist-only loss cannot drop those counts.
	if err := s.backfillFlushedRootsLocked(); err != nil {
		log.Printf("warning: failed to backfill flushed roots: %v", err)
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

	for k, v := range latestDist {
		s.flushedNodeCounts[k] = v
		s.nodeCounts[k] = v
	}

	return nil
}

func (s *SMSTArtifactStore) recoverFlushedRoots() error {
	if s.rootsFile == nil {
		return nil
	}
	if _, err := s.rootsFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek flushed roots file: %w", err)
	}

	reader := bufio.NewReader(s.rootsFile)
	lineNum := 0
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("read flushed roots file: %w", err)
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 {
			lineNum++
			var entry flushedRootEntry
			if jsonErr := json.Unmarshal(line, &entry); jsonErr != nil {
				log.Printf("warning: skipping corrupted flushed root entry at line %d: %v", lineNum, jsonErr)
			} else if entry.Count > 0 {
				root, decErr := hex.DecodeString(entry.Root)
				if decErr != nil || len(root) == 0 {
					log.Printf("warning: skipping flushed root entry at line %d: bad root hex: %v", lineNum, decErr)
				} else {
					s.flushedRoots[entry.Count] = root
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	return nil
}

// backfillFlushedRootsLocked appends any in-memory flushedRoots counts that are
// missing from flushed_roots.jsonl (e.g. upgraded stores that only had
// distributions.jsonl). Call after recover has recomputed roots.
func (s *SMSTArtifactStore) backfillFlushedRootsLocked() error {
	if s.rootsFile == nil {
		return nil
	}
	persisted := make(map[uint32]struct{}, len(s.flushedRoots))
	// Re-read what is already on disk so we do not duplicate lines.
	if _, err := s.rootsFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(s.rootsFile)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return err
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 {
			var entry flushedRootEntry
			if json.Unmarshal(line, &entry) == nil && entry.Count > 0 {
				persisted[entry.Count] = struct{}{}
			}
		}
		if err == io.EOF {
			break
		}
	}
	for count, root := range s.flushedRoots {
		if count == 0 || root == nil {
			continue
		}
		if _, ok := persisted[count]; ok {
			continue
		}
		if err := s.appendFlushedRootLocked(count, root); err != nil {
			return err
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

	if _, err := s.insertLeaf(nonce, leafHash); err != nil {
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
	if s.closed {
		s.mu.Unlock()
		return ErrStoreClosed
	}
	err := s.flushLocked()
	s.mu.Unlock()
	return err
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
	s.captureRetainedLocked()

	// Persist the flush boundary before the (best-effort) distribution snapshot
	// so a dist-append failure cannot lose the committed count across restart.
	if err := s.appendFlushedRootLocked(s.flushedLeafCount, rootHash); err != nil {
		return fmt.Errorf("persist flushed root: %w", err)
	}

	if err := s.appendDistributionSnapshot(); err != nil {
		log.Printf("warning: distribution snapshot failed (will use simulation): %v", err)
	}

	return nil
}

func (s *SMSTArtifactStore) appendFlushedRootLocked(count uint32, root []byte) error {
	if s.rootsFile == nil || count == 0 || root == nil {
		return nil
	}

	entry := flushedRootEntry{
		Count: count,
		Root:  hex.EncodeToString(root),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal flushed root entry: %w", err)
	}
	data = append(data, '\n')
	if _, err := s.rootsFile.Write(data); err != nil {
		return fmt.Errorf("write flushed root entry: %w", err)
	}
	if err := s.rootsFile.Sync(); err != nil {
		return fmt.Errorf("sync flushed roots file: %w", err)
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
	if s.smst.Count() == 0 {
		s.mu.RUnlock()
		return nil
	}
	if s.liveRootHashed() {
		root, _ := s.smst.GetRoot()
		s.mu.RUnlock()
		return root
	}
	s.mu.RUnlock()

	// Deferred hashes need a write; GetRoot → ensureHashed.
	s.mu.Lock()
	defer s.mu.Unlock()
	root, _ := s.smst.GetRoot()
	return root
}

func (s *SMSTArtifactStore) GetRootAt(snapshotCount uint32) ([]byte, error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, ErrStoreClosed
	}
	if snapshotCount == 0 {
		s.mu.RUnlock()
		return nil, nil
	}
	if snapshotCount > s.smst.Count() {
		currentCount := s.smst.Count()
		s.mu.RUnlock()
		return nil, fmt.Errorf("snapshot count %d exceeds current count %d", snapshotCount, currentCount)
	}
	if snapshotCount == s.smst.Count() && s.liveRootHashed() {
		root, _ := s.smst.GetRoot()
		s.mu.RUnlock()
		return root, nil
	}
	if root, ok := s.flushedRoots[snapshotCount]; ok {
		s.mu.RUnlock()
		return root, nil
	}
	s.mu.RUnlock()

	// Live tip still needs ensureHashed, or a cold historical miss.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrStoreClosed
	}
	if snapshotCount == s.smst.Count() {
		root, _ := s.smst.GetRoot()
		s.mu.Unlock()
		return root, nil
	}
	if root, ok := s.flushedRoots[snapshotCount]; ok {
		s.mu.Unlock()
		return root, nil
	}
	s.mu.Unlock()

	tree, unlock, err := s.acquireSnapshotTree(snapshotCount)
	if err != nil {
		return nil, err
	}
	defer unlock()
	root, _ := tree.GetRoot()
	return root, nil
}

func (s *SMSTArtifactStore) GetFlushedRoot() (count uint32, root []byte) {
	s.mu.RLock()
	if s.flushedLeafCount == 0 {
		s.mu.RUnlock()
		return 0, nil
	}
	if root, ok := s.flushedRoots[s.flushedLeafCount]; ok {
		c := s.flushedLeafCount
		s.mu.RUnlock()
		return c, root
	}
	s.mu.RUnlock()

	// Fallback: map miss (should be rare). May need ensureHashed on the live tree.
	s.mu.Lock()
	defer s.mu.Unlock()
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
	entries, err := s.GetArtifactsAndProofs([]uint32{denseIndex}, snapshotCount)
	if err != nil {
		return 0, nil, nil, err
	}
	if len(entries) != 1 {
		return 0, nil, nil, ErrLeafIndexOutOfRange
	}
	entry := entries[0]
	return entry.Nonce, entry.Vector, entry.Proof, nil
}

func (s *SMSTArtifactStore) GetArtifactsAndProofs(denseIndices []uint32, snapshotCount uint32) ([]ProofEntry, error) {
	if len(denseIndices) == 0 {
		return nil, nil
	}
	if snapshotCount == 0 {
		return nil, ErrLeafIndexOutOfRange
	}
	for _, denseIndex := range denseIndices {
		if denseIndex >= snapshotCount {
			return nil, ErrLeafIndexOutOfRange
		}
	}

	tree, unlock, err := s.acquireSnapshotTree(snapshotCount)
	if err != nil {
		return nil, err
	}
	defer unlock()

	entries := make([]ProofEntry, 0, len(denseIndices))
	for _, denseIndex := range denseIndices {
		nonce, _, err := tree.GetLeafByDenseIndex(denseIndex)
		if err != nil {
			return nil, err
		}
		entry, err := s.proofEntry(tree, nonce, denseIndex)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// GetArtifactAndProofByNonce retrieves artifact data and proof for a nonce at a
// specific snapshot, returning the nonce's dense index in that snapshot.
func (s *SMSTArtifactStore) GetArtifactAndProofByNonce(targetNonce int32, snapshotCount uint32) (denseIndex uint32, vector []byte, proof [][]byte, err error) {
	entries, err := s.GetArtifactsAndProofsByNonce([]int32{targetNonce}, snapshotCount)
	if err != nil {
		return 0, nil, nil, err
	}
	if len(entries) == 0 {
		return 0, nil, nil, ErrNonceNotFound
	}
	entry := entries[0]
	return entry.DenseIndex, entry.Vector, entry.Proof, nil
}

func (s *SMSTArtifactStore) GetArtifactsAndProofsByNonce(nonces []int32, snapshotCount uint32) ([]ProofEntry, error) {
	if len(nonces) == 0 || snapshotCount == 0 {
		return nil, nil
	}

	tree, unlock, err := s.acquireSnapshotTree(snapshotCount)
	if err != nil {
		return nil, err
	}
	defer unlock()

	entries := make([]ProofEntry, 0, len(nonces))
	for _, nonce := range nonces {
		if !tree.HasNonce(nonce) {
			continue
		}
		denseIndex, err := tree.denseIndexForNonce(nonce)
		if err != nil {
			return nil, err
		}
		entry, err := s.proofEntry(tree, nonce, denseIndex)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// acquireSnapshotTree returns the tree for snapshotCount with the store
// locked for the caller; the caller must call unlock() when done.
// Live tip (hashes already filled) and retained historical snapshots are
// served under RLock so concurrent proofs stay parallel. Filling deferred
// hashes on the live tip takes Lock only for ensureHashed, then retries so
// proof I/O runs under RLock. Cold-start rebuilds use the process-wide
// snapshot cache, then RLock for artifact reads.
func (s *SMSTArtifactStore) acquireSnapshotTree(snapshotCount uint32) (*SMST, func(), error) {
	// Fast path: if the requested count is the live tip and its hashes are
	// already filled, serve under a read lock so concurrent proof reads stay
	// parallel. Writers hold the write lock, so under RLock neither the count
	// nor the node hashes can change; a filled root implies a fully hashed tree.
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, nil, ErrStoreClosed
	}
	if snapshotCount == s.smst.Count() && s.liveRootHashed() {
		return s.smst, s.mu.RUnlock, nil
	}
	s.mu.RUnlock()

	// Slow path: a historical count, or the live tip still needs deferred hashes
	// filled, which is a mutation and requires the write lock.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrStoreClosed
	}
	currentCount := s.smst.Count()
	if snapshotCount > currentCount {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("snapshot count %d exceeds current count %d", snapshotCount, currentCount)
	}
	if snapshotCount == currentCount {
		// Fill hashes under Lock only — do not hold the write lock across proof
		// I/O. Retry so concurrent tip readers share the RLock fast path.
		s.smst.ensureHashed()
		s.mu.Unlock()
		return s.acquireSnapshotTree(snapshotCount)
	}
	// Historical count: serve from the retained copy-on-write snapshot in
	// O(depth) if one was captured at this committed count. Snapshots are
	// captured after a GetRoot, so their nodes are already hashed; the shared
	// nodes are immutable (insertCOW never rewrites them), so the view stays
	// valid after the write lock is released. Drop Lock before proof I/O so
	// ingest and concurrent proofs are not serialized behind getArtifactByNonce;
	// re-take RLock so proofEntry can safely read nonceToOffset / buffer.
	if view, ok := s.retainedSnapshotViewLocked(snapshotCount); ok {
		s.mu.Unlock()
		s.mu.RLock()
		if s.closed {
			s.mu.RUnlock()
			return nil, nil, ErrStoreClosed
		}
		return view, s.mu.RUnlock, nil
	}
	_, committed := s.flushedRoots[snapshotCount]
	s.mu.Unlock()

	if !committed {
		// Not a committed count: its root can never match the on-chain
		// commitment, so it is never legitimately provable. Rejecting here
		// instead of rebuilding removes the per-request O(N) rebuild, and with it
		// the rebuild-DoS surface — so no per-validator snapshot-count quota is
		// needed. Committed counts are all retained (live capture, or recovery
		// re-capture after a restart); the rebuild below is a cold-start edge.
		return nil, nil, fmt.Errorf("snapshot count %d is not a committed count", snapshotCount)
	}

	tree, err := globalSnapshotCache.getOrBuild(s, snapshotCount, false)
	if err != nil {
		return nil, nil, err
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, nil, ErrStoreClosed
	}
	return tree, s.mu.RUnlock, nil
}

// liveRootHashed reports whether the live tree's deferred hashes are already
// filled. A nil root (empty tree) is treated as ready. Callers must hold mu.
func (s *SMSTArtifactStore) liveRootHashed() bool {
	return s.smst.root == nil || s.smst.root.hash != nil
}

// insertLeaf adds a leaf via insertCOW (default) or in-place Insert when
// SMST_COW is disabled. Both paths use deferred hashing.
//
// Deprecated: the SMST_COW=0 in-place branch is a profiling baseline only.
// Production must keep copy-on-write enabled.
func (s *SMSTArtifactStore) insertLeaf(nonce int32, leafHash []byte) (uint32, error) {
	if s.cowEnabled {
		return s.smst.insertCOW(nonce, leafHash)
	}
	return s.smst.Insert(nonce, leafHash)
}

// captureRetainedLocked records a copy-on-write snapshot of the tree at the
// current committed count so its proofs are served in O(depth) without a
// rebuild. The write lock must be held; call after GetRoot so the retained nodes
// are already hashed. Bounded by the store's per-stage lifetime. No-op when
// COW is disabled — in-place inserts would invalidate shared snapshot nodes.
func (s *SMSTArtifactStore) captureRetainedLocked() {
	if !s.cowEnabled {
		return
	}
	count := s.smst.Count()
	if count == 0 {
		return
	}
	if _, ok := s.retained[count]; ok {
		return
	}
	s.retained[count] = s.smst.snapshot()
}

// retainedSnapshotViewLocked returns an O(depth) read-only view over the
// retained snapshot at count, or ok=false when none was captured. The lock must
// be held (it reads the retained map); the returned view shares immutable nodes.
func (s *SMSTArtifactStore) retainedSnapshotViewLocked(count uint32) (*SMST, bool) {
	snap, ok := s.retained[count]
	if !ok {
		return nil, false
	}
	return s.smst.snapshotView(snap), true
}

// proofEntry builds a ProofEntry for a nonce known to be in tree.
// Requires the store read lock to be held.
func (s *SMSTArtifactStore) proofEntry(tree *SMST, nonce int32, denseIndex uint32) (ProofEntry, error) {
	_, vector, err := s.getArtifactByNonce(nonce)
	if err != nil {
		return ProofEntry{}, err
	}
	return ProofEntry{
		DenseIndex: denseIndex,
		Nonce:      nonce,
		Vector:     vector,
		Proof:      encodeProofForTransport(s.buildProofWithCounts(tree, nonce)),
	}, nil
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

func (s *SMSTArtifactStore) snapshotRebuildInputs(count uint32) ([]uint64, []bufferedArtifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, nil, ErrStoreClosed
	}
	if count > s.smst.Count() {
		return nil, nil, fmt.Errorf("snapshot count %d exceeds current count %d", count, s.smst.Count())
	}

	flushedToRead := count
	if flushedToRead > s.flushedLeafCount {
		flushedToRead = s.flushedLeafCount
	}
	offsets := slices.Clone(s.offsets[:flushedToRead])

	var buffered []bufferedArtifact
	if count > s.flushedLeafCount {
		remaining := count - s.flushedLeafCount
		bufferedCount := min(int(remaining), len(s.buffer))
		buffered = slices.Clone(s.buffer[:bufferedCount])
	}

	return offsets, buffered, nil
}

func rebuildTreeFromInputs(dataFile *os.File, offsets []uint64, buffered []bufferedArtifact, count uint32) *SMST {
	// Seed with the default depth, not the live tree's depth: replaying the
	// first `count` artifacts triggers exactly the depth expansions the tree
	// had when the root for `count` was committed. Using the live depth would
	// bake in expansions caused by artifacts inserted after that commit,
	// producing a root that no longer matches the committed one.
	tree := NewSMST(smstDefaultDepth)
	tree.parallelHash = smstParallelHashFromEnv()

	// Read flushed artifacts from disk
	var skipped uint32
	for _, offset := range offsets {
		nonce, vector, _, err := readArtifactAt(dataFile, int64(offset))
		if err != nil {
			skipped++
			continue
		}
		leafData := encodeLeaf(nonce, vector)
		leafHash := smstHashLeaf(leafData)
		if _, err := tree.Insert(nonce, leafHash); err != nil {
			log.Printf("[WARN] SMST snapshot rebuild: insert failed for nonce %d: %v (possible data corruption)", nonce, err)
		}
	}
	if skipped > 0 {
		log.Printf("[WARN] SMST snapshot rebuild: skipped %d/%d artifacts due to read errors", skipped, len(offsets))
	}

	// Read remaining from buffer if needed
	for _, art := range buffered {
		leafData := encodeLeaf(art.nonce, art.vector)
		leafHash := smstHashLeaf(leafData)
		tree.Insert(art.nonce, leafHash)
	}

	if tree.Count() != count {
		log.Printf("[WARN] SMST snapshot rebuild: rebuilt count %d differs from requested %d", tree.Count(), count)
	}
	tree.ensureHashed() // fresh, single-owned tree — hash before it is cached/served
	return tree
}

// PrebuildSnapshot pins a historical tree at count for fast proof queries.
//
// Live tip (count == leaf count):
//   - COW on: O(1) retain under write lock (no-op if flush already retained).
//   - COW off + SMST_SNAPSHOT_IN_MEMORY_CLONE=1 (default): deep clone under
//     write lock into retained.
//   - COW off + SMST_SNAPSHOT_IN_MEMORY_CLONE=0: unlock, then rebuild from
//     artifacts into the process snapshot cache (upgrade-v0.2.14 Warm/Prebuild;
//     write lock not held during rebuild).
//
// Tip already advanced with no retained capture: committed counts rebuild into
// the cache (cold path); uncommitted counts are rejected.
func (s *SMSTArtifactStore) PrebuildSnapshot(count uint32) error {
	if count == 0 {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrStoreClosed
	}
	if _, ok := s.retained[count]; ok {
		s.mu.Unlock()
		return nil
	}
	if count == s.smst.Count() {
		if s.cowEnabled {
			s.smst.ensureHashed()
			s.retained[count] = s.smst.snapshot()
			s.mu.Unlock()
			return nil
		}
		if s.snapshotInMemoryClone {
			s.smst.ensureHashed()
			s.retained[count] = s.smst.cloneSnapshot()
			s.mu.Unlock()
			return nil
		}
		// v0.2.14-style: do not hold the write lock across artifact rebuild.
		s.mu.Unlock()
		_, err := globalSnapshotCache.getOrBuild(s, count, true)
		return err
	}
	_, committed := s.flushedRoots[count]
	s.mu.Unlock()

	if !committed {
		return fmt.Errorf("snapshot count %d is not a committed count", count)
	}
	_, err := globalSnapshotCache.getOrBuild(s, count, true)
	return err
}

// WarmSnapshot pins the snapshot tree for count so later proof requests hit
// retained (tip deep-copy / COW) or the process cache. Blocking; callers may
// run it in the background.
func (s *SMSTArtifactStore) WarmSnapshot(count uint32) {
	if count == 0 {
		return
	}
	if err := s.PrebuildSnapshot(count); err != nil {
		log.Printf("[WARN] SMST WarmSnapshot(%d) failed: %v", count, err)
	}
}

func (s *SMSTArtifactStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	globalSnapshotCache.purgeStore(s)

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

	if s.rootsFile != nil {
		if err := s.rootsFile.Close(); err != nil {
			return fmt.Errorf("close flushed roots file: %w", err)
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
