package artifacts

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	smstSuffixHeight     = 12
	smstSuffixMask       = (1 << smstSuffixHeight) - 1
	defaultSMSTRAMLeaves = 8_000_000
	envSMSTRAMLeaves     = "SMST_RAM_LEAVES"
	smstHotSuffixCap     = 4
	suffixDirName        = "suffix"
	suffixMarkerName     = "SPILLED"
)

type suffixRec struct {
	nonce int32
	seq   uint32
}

type suffixMeta struct {
	prefix   uint32
	count    uint32
	firstSeq uint32
	lastSeq  uint32
	hash     []byte
	dirty    []suffixRec
}

type hotSuffix struct {
	prefix   uint32
	tree     *SMST
	nonceSeq map[int32]uint32
}

type nonceBitmap struct {
	chunks map[uint32][]uint64
}

func smstRAMLeafLimitFromEnv() uint32 {
	v := strings.TrimSpace(os.Getenv(envSMSTRAMLeaves))
	if v == "" {
		return defaultSMSTRAMLeaves
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil || n == 0 {
		return defaultSMSTRAMLeaves
	}
	return uint32(n)
}

func suffixPrefix(nonce int32) uint32 {
	return uint32(nonce) >> smstSuffixHeight
}

func suffixLocal(nonce int32) int32 {
	return int32(uint32(nonce) & smstSuffixMask)
}

func fullNonce(prefix uint32, local int32) int32 {
	return int32((prefix << smstSuffixHeight) | (uint32(local) & smstSuffixMask))
}

func (b *nonceBitmap) set(nonce int32) {
	if b.chunks == nil {
		b.chunks = make(map[uint32][]uint64)
	}
	u := uint32(nonce)
	k := u >> 16
	if b.chunks[k] == nil {
		b.chunks[k] = make([]uint64, 1024)
	}
	bit := u & 0xFFFF
	b.chunks[k][bit/64] |= 1 << (bit % 64)
}

func (b *nonceBitmap) has(nonce int32) bool {
	if b == nil || b.chunks == nil {
		return false
	}
	u := uint32(nonce)
	ch := b.chunks[u>>16]
	if ch == nil {
		return false
	}
	bit := u & 0xFFFF
	return ch[bit/64]&(1<<(bit%64)) != 0
}

func (s *SMSTArtifactStore) suffixDir() string {
	return filepath.Join(s.dir, suffixDirName)
}

func (s *SMSTArtifactStore) suffixLogPath(prefix uint32) string {
	return filepath.Join(s.suffixDir(), fmt.Sprintf("%08x.log", prefix))
}

func (s *SMSTArtifactStore) suffixSealPath(prefix uint32) string {
	return filepath.Join(s.suffixDir(), fmt.Sprintf("%08x.seal", prefix))
}

func (s *SMSTArtifactStore) suffixMarkerPath() string {
	return filepath.Join(s.suffixDir(), suffixMarkerName)
}

func (s *SMSTArtifactStore) insertPaged(nonce int32, leafHash []byte) error {
	if s.bitmap.has(nonce) {
		return ErrDuplicateNonce
	}
	if s.smst.cutHeight == 0 {
		s.smst.cutHeight = smstSuffixHeight
	}
	seq := s.smst.Count()
	hot, err := s.ensureHotSuffix(suffixPrefix(nonce))
	if err != nil {
		return err
	}
	if _, err := hot.tree.Insert(suffixLocal(nonce), leafHash); err != nil {
		return err
	}
	hot.nonceSeq[nonce] = seq
	s.bitmap.set(nonce)

	root, count := hot.tree.GetRoot()
	s.smst.attachSealedCOW(nonce, &smstNode{hash: root, count: count})
	s.smst.leafCount = seq + 1

	meta := s.ensureSuffixMeta(suffixPrefix(nonce))
	if meta.count == 0 {
		meta.firstSeq = seq
	}
	meta.lastSeq = seq
	meta.count++
	meta.hash = append([]byte(nil), root...)
	meta.dirty = append(meta.dirty, suffixRec{nonce: nonce, seq: seq})
	return nil
}

func (s *SMSTArtifactStore) ensureSuffixMeta(prefix uint32) *suffixMeta {
	if s.suffixes == nil {
		s.suffixes = make(map[uint32]*suffixMeta)
	}
	m := s.suffixes[prefix]
	if m == nil {
		m = &suffixMeta{prefix: prefix}
		s.suffixes[prefix] = m
	}
	return m
}

func (s *SMSTArtifactStore) ensureHotSuffix(prefix uint32) (*hotSuffix, error) {
	for _, h := range s.hot {
		if h != nil && h.prefix == prefix {
			return h, nil
		}
	}
	h, err := s.loadHotSuffix(prefix)
	if err != nil {
		return nil, err
	}
	if len(s.hot) >= smstHotSuffixCap {
		evict := s.hot[0]
		if evict != nil {
			s.persistHotSeal(evict)
		}
		s.hot = s.hot[1:]
	}
	s.hot = append(s.hot, h)
	return h, nil
}

func (s *SMSTArtifactStore) persistHotSeal(h *hotSuffix) {
	if h == nil || h.tree == nil {
		return
	}
	root, count := h.tree.GetRoot()
	meta := s.ensureSuffixMeta(h.prefix)
	meta.hash = append([]byte(nil), root...)
	meta.count = count
	_ = s.writeSeal(h.prefix, root, count)
}

func (s *SMSTArtifactStore) loadHotSuffix(prefix uint32) (*hotSuffix, error) {
	h := &hotSuffix{
		prefix:   prefix,
		tree:     NewSMST(smstSuffixHeight),
		nonceSeq: make(map[int32]uint32),
	}
	h.tree.deferredHash = s.smst.deferredHash
	h.tree.parallelHash = s.smst.parallelHash
	recs, err := s.readSuffixLog(prefix, ^uint32(0))
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		leafHash, err := s.leafHashAtSeq(rec.seq, rec.nonce)
		if err != nil {
			return nil, err
		}
		if _, err := h.tree.Insert(suffixLocal(rec.nonce), leafHash); err != nil {
			return nil, err
		}
		h.nonceSeq[rec.nonce] = rec.seq
	}
	return h, nil
}

func (s *SMSTArtifactStore) readSuffixLog(prefix uint32, maxSeq uint32) ([]suffixRec, error) {
	f, err := os.Open(s.suffixLogPath(prefix))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []suffixRec
	var buf [8]byte
	for {
		if _, err := io.ReadFull(f, buf[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return nil, err
		}
		rec := suffixRec{
			nonce: int32(binary.LittleEndian.Uint32(buf[0:4])),
			seq:   binary.LittleEndian.Uint32(buf[4:8]),
		}
		if rec.seq < maxSeq {
			out = append(out, rec)
		}
	}
	if meta := s.suffixes[prefix]; meta != nil {
		for _, rec := range meta.dirty {
			if rec.seq < maxSeq {
				out = append(out, rec)
			}
		}
	}
	return out, nil
}

func (s *SMSTArtifactStore) writeSuffixDirtyLocked() error {
	if err := os.MkdirAll(s.suffixDir(), 0755); err != nil {
		return err
	}
	for prefix, meta := range s.suffixes {
		if meta == nil || len(meta.dirty) == 0 {
			continue
		}
		f, err := os.OpenFile(s.suffixLogPath(prefix), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return err
		}
		var buf [8]byte
		for _, rec := range meta.dirty {
			binary.LittleEndian.PutUint32(buf[0:4], uint32(rec.nonce))
			binary.LittleEndian.PutUint32(buf[4:8], rec.seq)
			if _, err := f.Write(buf[:]); err != nil {
				f.Close()
				return err
			}
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if meta.hash != nil {
			if err := s.writeSeal(prefix, meta.hash, meta.count); err != nil {
				return err
			}
		}
		meta.dirty = meta.dirty[:0]
	}
	return nil
}

func (s *SMSTArtifactStore) writeSeal(prefix uint32, hash []byte, count uint32) error {
	if err := os.MkdirAll(s.suffixDir(), 0755); err != nil {
		return err
	}
	var buf [36]byte
	copy(buf[:32], hash)
	binary.LittleEndian.PutUint32(buf[32:], count)
	return os.WriteFile(s.suffixSealPath(prefix), buf[:], 0644)
}

func (s *SMSTArtifactStore) readSeal(prefix uint32) (hash []byte, count uint32, ok bool) {
	data, err := os.ReadFile(s.suffixSealPath(prefix))
	if err != nil || len(data) < 36 {
		return nil, 0, false
	}
	return append([]byte(nil), data[:32]...), binary.LittleEndian.Uint32(data[32:36]), true
}

func (s *SMSTArtifactStore) writeSpillMarker() error {
	if err := os.MkdirAll(s.suffixDir(), 0755); err != nil {
		return err
	}
	body := fmt.Sprintf("record_size=%d\ncount=%d\n", s.pagedRecordSize, s.smst.Count())
	return os.WriteFile(s.suffixMarkerPath(), []byte(body), 0644)
}

func (s *SMSTArtifactStore) readSpillMarker() (recordSize int, count uint32, ok bool) {
	data, err := os.ReadFile(s.suffixMarkerPath())
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch k {
		case "record_size":
			n, err := strconv.Atoi(v)
			if err == nil {
				recordSize = n
			}
		case "count":
			n, err := strconv.ParseUint(v, 10, 32)
			if err == nil {
				count = uint32(n)
			}
		}
	}
	return recordSize, count, recordSize > 0
}

func (s *SMSTArtifactStore) spillLocked() error {
	if s.spilled || s.smst.Count() == 0 {
		return nil
	}
	if err := os.MkdirAll(s.suffixDir(), 0755); err != nil {
		return fmt.Errorf("suffix dir: %w", err)
	}
	if _, err := s.dataFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var seq uint32
	var recSize int
	for {
		nonce, _, n, err := readArtifact(s.dataFile)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("spill read: %w", err)
		}
		if recSize == 0 {
			recSize = n
		} else if n != recSize {
			return fmt.Errorf("spill requires fixed-size records, got %d then %d", recSize, n)
		}
		prefix := suffixPrefix(nonce)
		meta := s.ensureSuffixMeta(prefix)
		if meta.count == 0 {
			meta.firstSeq = seq
		}
		meta.lastSeq = seq
		meta.count++
		meta.dirty = append(meta.dirty, suffixRec{nonce: nonce, seq: seq})
		s.bitmap.set(nonce)
		seq++
	}
	if recSize == 0 {
		return nil
	}
	s.pagedRecordSize = recSize
	if err := s.writeSuffixDirtyLocked(); err != nil {
		return err
	}
	s.smst.sealAtCut(smstSuffixHeight)
	if err := s.persistSealedCuts(); err != nil {
		return err
	}
	s.spilled = true
	s.smst.hasNonce = nil
	s.offsets = nil
	s.nonceToOffset = nil
	s.hot = nil
	s.retained = make(map[uint32]smstSnapshot)
	s.smst.ensureHashed()
	s.captureRetainedLocked()
	return s.writeSpillMarker()
}

func (s *SMSTArtifactStore) persistSealedCuts() error {
	cut := s.smst.cutLevel()
	if cut < 0 || s.smst.root == nil {
		return nil
	}
	var walk func(node *smstNode, level int, prefix uint32)
	walk = func(node *smstNode, level int, prefix uint32) {
		if node == nil {
			return
		}
		if level == cut {
			meta := s.ensureSuffixMeta(prefix)
			meta.hash = append([]byte(nil), node.hash...)
			meta.count = node.count
			_ = s.writeSeal(prefix, node.hash, node.count)
			return
		}
		walk(node.left, level+1, prefix<<1)
		walk(node.right, level+1, prefix<<1|1)
	}
	walk(s.smst.root, 0, 0)
	return nil
}

func (s *SMSTArtifactStore) recoverPaged() error {
	if err := s.recoverFlushedRoots(); err != nil {
		return err
	}
	if err := s.recoverDistributionHistory(); err != nil {
		return err
	}
	recSize, _, ok := s.readSpillMarker()
	if !ok {
		return fmt.Errorf("paged recover: missing spill marker")
	}
	s.pagedRecordSize = recSize
	s.spilled = true
	s.bitmap.chunks = make(map[uint32][]uint64)
	s.suffixes = make(map[uint32]*suffixMeta)
	s.smst = NewSMST(smstDefaultDepth)
	s.smst.deferredHash = smstDeferredHashFromEnv()
	s.smst.parallelHash = smstParallelHashFromEnv()
	s.smst.cutHeight = smstSuffixHeight

	entries, err := os.ReadDir(s.suffixDir())
	if err != nil {
		return err
	}
	var total uint32
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".log") {
			continue
		}
		hexPrefix, ok := strings.CutSuffix(name, ".log")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(hexPrefix, 16, 32)
		if err != nil {
			continue
		}
		prefix := uint32(p)
		recs, err := s.readSuffixLog(prefix, ^uint32(0))
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			continue
		}
		meta := s.ensureSuffixMeta(prefix)
		meta.count = uint32(len(recs))
		meta.firstSeq = recs[0].seq
		meta.lastSeq = recs[len(recs)-1].seq
		for _, rec := range recs {
			s.bitmap.set(rec.nonce)
		}
		hash, count, sealed := s.readSeal(prefix)
		if !sealed || count != meta.count {
			tree, err := s.rebuildSuffixTree(prefix, ^uint32(0))
			if err != nil {
				return err
			}
			hash, count = tree.GetRoot()
			meta.hash = append([]byte(nil), hash...)
			_ = s.writeSeal(prefix, hash, count)
		} else {
			meta.hash = hash
		}
		s.smst.attachSealedCOW(fullNonce(prefix, 0), &smstNode{hash: meta.hash, count: count})
		total += count
	}
	s.smst.leafCount = total
	s.smst.ensureHashed()
	s.flushedLeafCount = total
	s.flushedDataOffset = uint64(total) * uint64(s.pagedRecordSize)
	s.flushedRoots[total] = s.smst.root.hash
	// Live tip only. Historical counts rebuild from suffix seq cutoffs.
	s.captureRetainedLocked()
	return nil
}

func (s *SMSTArtifactStore) rebuildSuffixTree(prefix uint32, maxSeq uint32) (*SMST, error) {
	tree, _, err := s.rebuildSuffixState(prefix, maxSeq)
	return tree, err
}

func (s *SMSTArtifactStore) rebuildSuffixState(prefix uint32, maxSeq uint32) (*SMST, map[int32]uint32, error) {
	recs, err := s.readSuffixLog(prefix, maxSeq)
	if err != nil {
		return nil, nil, err
	}
	tree := NewSMST(smstSuffixHeight)
	tree.deferredHash = s.smst.deferredHash
	tree.parallelHash = false
	seqs := make(map[int32]uint32, len(recs))
	for _, rec := range recs {
		leafHash, err := s.leafHashAtSeq(rec.seq, rec.nonce)
		if err != nil {
			return nil, nil, err
		}
		if _, err := tree.Insert(suffixLocal(rec.nonce), leafHash); err != nil {
			return nil, nil, err
		}
		seqs[rec.nonce] = rec.seq
	}
	tree.ensureHashed()
	return tree, seqs, nil
}

func (s *SMSTArtifactStore) buildPagedUpperAt(count uint32) (*SMST, error) {
	tree := NewSMST(smstDefaultDepth)
	tree.deferredHash = s.smst.deferredHash
	tree.parallelHash = s.smst.parallelHash
	tree.cutHeight = smstSuffixHeight
	var total uint32
	for prefix, meta := range s.suffixes {
		if meta == nil {
			continue
		}
		if meta.firstSeq >= count {
			continue
		}
		if meta.lastSeq < count && meta.hash != nil && meta.count > 0 && meta.lastSeq+1 <= count {
			tree.attachSealedCOW(fullNonce(prefix, 0), &smstNode{hash: meta.hash, count: meta.count})
			total += meta.count
			continue
		}
		suf, err := s.rebuildSuffixTree(prefix, count)
		if err != nil {
			return nil, err
		}
		if suf.Count() == 0 {
			continue
		}
		root, c := suf.GetRoot()
		tree.attachSealedCOW(fullNonce(prefix, 0), &smstNode{hash: root, count: c})
		total += c
	}
	tree.leafCount = total
	tree.ensureHashed()
	if total != count {
		return nil, fmt.Errorf("paged historical count %d != requested %d", total, count)
	}
	return tree, nil
}

func (s *SMSTArtifactStore) leafHashAtSeq(seq uint32, nonce int32) ([]byte, error) {
	vec, err := s.vectorAtSeq(seq, nonce)
	if err != nil {
		return nil, err
	}
	return smstHashLeaf(encodeLeaf(nonce, vec)), nil
}

func (s *SMSTArtifactStore) vectorAtSeq(seq uint32, nonce int32) ([]byte, error) {
	if seq >= s.flushedLeafCount {
		idx := int(seq - s.flushedLeafCount)
		if idx >= 0 && idx < len(s.buffer) && s.buffer[idx].nonce == nonce {
			return s.buffer[idx].vector, nil
		}
		for _, art := range s.buffer {
			if art.nonce == nonce {
				return art.vector, nil
			}
		}
		return nil, ErrNonceNotFound
	}
	if s.pagedRecordSize > 0 {
		_, vector, _, err := readArtifactAt(s.dataFile, int64(seq)*int64(s.pagedRecordSize))
		if err != nil {
			return nil, err
		}
		return vector, nil
	}
	if offset, ok := s.nonceToOffset[nonce]; ok {
		_, vector, _, err := readArtifactAt(s.dataFile, int64(offset))
		if err != nil {
			return nil, err
		}
		return vector, nil
	}
	return nil, ErrNonceNotFound
}

func (s *SMSTArtifactStore) getArtifactPaged(nonce int32) (int32, []byte, error) {
	for _, art := range s.buffer {
		if art.nonce == nonce {
			return art.nonce, art.vector, nil
		}
	}
	if !s.bitmap.has(nonce) {
		return 0, nil, ErrLeafIndexOutOfRange
	}
	seq, ok := s.seqForNonce(nonce)
	if !ok {
		return 0, nil, ErrNonceNotFound
	}
	vec, err := s.vectorAtSeq(seq, nonce)
	if err != nil {
		return 0, nil, err
	}
	return nonce, vec, nil
}

func (s *SMSTArtifactStore) seqForNonce(nonce int32) (uint32, bool) {
	prefix := suffixPrefix(nonce)
	for _, h := range s.hot {
		if h != nil && h.prefix == prefix {
			if seq, ok := h.nonceSeq[nonce]; ok {
				return seq, true
			}
		}
	}
	recs, err := s.readSuffixLog(prefix, ^uint32(0))
	if err != nil {
		return 0, false
	}
	for _, rec := range recs {
		if rec.nonce == nonce {
			return rec.seq, true
		}
	}
	return 0, false
}

func (s *SMSTArtifactStore) pagedGetArtifactsAndProofs(denseIndices []uint32, snapshotCount uint32) ([]ProofEntry, error) {
	tree, unlock, err := s.acquireSnapshotTree(snapshotCount)
	if err != nil {
		return nil, err
	}
	defer unlock()

	type item struct {
		idx        int
		denseIndex uint32
		cut        denseCut
	}
	byPrefix := make(map[uint32][]item)
	for i, di := range denseIndices {
		cut, err := tree.walkDenseToCut(di)
		if err != nil {
			return nil, err
		}
		byPrefix[cut.prefix] = append(byPrefix[cut.prefix], item{idx: i, denseIndex: di, cut: cut})
	}

	// One suffix at a time: rebuild, prove, drop.
	entries := make([]ProofEntry, len(denseIndices))
	for prefix, group := range byPrefix {
		suf, seqs, err := s.rebuildSuffixState(prefix, snapshotCount)
		if err != nil {
			return nil, err
		}
		for _, it := range group {
			localNonce, _, err := suf.GetLeafByDenseIndex(it.cut.localIndex)
			if err != nil {
				return nil, err
			}
			nonce := fullNonce(prefix, localNonce)
			seq, ok := seqs[nonce]
			if !ok {
				return nil, ErrNonceNotFound
			}
			vector, err := s.vectorAtSeq(seq, nonce)
			if err != nil {
				return nil, err
			}
			elems := append(append([]smstProofElement(nil), it.cut.elements...), s.buildProofWithCounts(suf, localNonce)...)
			entries[it.idx] = ProofEntry{
				DenseIndex: it.denseIndex,
				Nonce:      nonce,
				Vector:     vector,
				Proof:      encodeProofForTransport(elems),
			}
		}
	}
	return entries, nil
}

func (s *SMSTArtifactStore) pagedGetArtifactsAndProofsByNonce(nonces []int32, snapshotCount uint32) ([]ProofEntry, error) {
	tree, unlock, err := s.acquireSnapshotTree(snapshotCount)
	if err != nil {
		return nil, err
	}
	defer unlock()

	byPrefix := make(map[uint32][]int32)
	for _, nonce := range nonces {
		if s.bitmap.has(nonce) || s.bufferHasNonce(nonce) {
			byPrefix[suffixPrefix(nonce)] = append(byPrefix[suffixPrefix(nonce)], nonce)
		}
	}
	var entries []ProofEntry
	for prefix, group := range byPrefix {
		suf, seqs, err := s.rebuildSuffixState(prefix, snapshotCount)
		if err != nil {
			return nil, err
		}
		if suf.Count() == 0 {
			continue
		}
		for _, nonce := range group {
			local := suffixLocal(nonce)
			if !suf.HasNonce(local) {
				continue
			}
			denseLocal, err := suf.denseIndexForNonce(local)
			if err != nil {
				return nil, err
			}
			seq, ok := seqs[nonce]
			if !ok {
				return nil, ErrNonceNotFound
			}
			vector, err := s.vectorAtSeq(seq, nonce)
			if err != nil {
				return nil, err
			}
			elems := append(append([]smstProofElement(nil), tree.proofElementsToCut(nonce)...), s.buildProofWithCounts(suf, local)...)
			entries = append(entries, ProofEntry{
				DenseIndex: s.upperBase(tree, nonce) + denseLocal,
				Nonce:      nonce,
				Vector:     vector,
				Proof:      encodeProofForTransport(elems),
			})
		}
	}
	return entries, nil
}

func (s *SMSTArtifactStore) bufferHasNonce(nonce int32) bool {
	for _, art := range s.buffer {
		if art.nonce == nonce {
			return true
		}
	}
	return false
}

func (s *SMSTArtifactStore) upperBase(tree *SMST, nonce int32) uint32 {
	if tree.root == nil {
		return 0
	}
	path := tree.noncePath(nonce)
	cut := tree.cutLevel()
	node := tree.root
	var base uint32
	for level := 0; level < cut && node != nil; level++ {
		if path[level] {
			base += tree.nodeCount(node.left)
			node = node.right
		} else {
			node = node.left
		}
	}
	return base
}

