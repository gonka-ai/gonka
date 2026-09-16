package artifacts

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	smstSuffixHeight     = 12
	smstSuffixMask       = (1 << smstSuffixHeight) - 1
	defaultSMSTRAMLeaves = 8_000_000
	envSMSTRAMLeaves     = "SMST_RAM_LEAVES"
	smstHotSuffixCap     = 32
	suffixDirName        = "suffix"
	suffixMarkerName     = "SPILLED"
	suffixHistoryName    = "history.jsonl"
)

type sealDelta struct {
	Prefix uint32 `json:"p"`
	Hash   string `json:"h"`
	Count  uint32 `json:"c"`
	Last   uint32 `json:"last"`
}

type sealJournalEntry struct {
	Count uint32      `json:"count"`
	Seals []sealDelta `json:"seals"`
}

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

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
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

func (s *SMSTArtifactStore) suffixHistoryPath() string {
	return filepath.Join(s.suffixDir(), suffixHistoryName)
}

func (s *SMSTArtifactStore) suffixTreePath(prefix uint32) string {
	return filepath.Join(s.suffixDir(), fmt.Sprintf("%08x.tree", prefix))
}

func (s *SMSTArtifactStore) findHot(prefix uint32) *hotSuffix {
	for _, h := range s.hot {
		if h != nil && h.prefix == prefix {
			return h
		}
	}
	return nil
}

func (s *SMSTArtifactStore) insertPaged(nonce int32, leafHash []byte) error {
	if s.bitmap.has(nonce) {
		return ErrDuplicateNonce
	}
	if s.smst.cutHeight == 0 {
		s.smst.cutHeight = smstSuffixHeight
	}
	seq := s.smst.Count()
	prefix := suffixPrefix(nonce)
	hot, err := s.ensureHotSuffix(prefix)
	if err != nil {
		return err
	}
	if _, err := hot.tree.insertCOW(suffixLocal(nonce), leafHash); err != nil {
		return err
	}
	hot.nonceSeq[nonce] = seq
	s.bitmap.set(nonce)
	s.smst.attachCutCOW(nonce, hot.tree.root)
	s.smst.leafCount = seq + 1

	meta := s.ensureSuffixMeta(prefix)
	if meta.count == 0 {
		meta.firstSeq = seq
	}
	meta.lastSeq = seq
	meta.count++
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
	if h := s.findHot(prefix); h != nil {
		return h, nil
	}
	var dirty []suffixRec
	var bufCopy []bufferedArtifact
	if meta := s.suffixes[prefix]; meta != nil {
		if len(meta.dirty) > 0 {
			dirty = append([]suffixRec(nil), meta.dirty...)
			needBuf := false
			for _, rec := range dirty {
				if rec.seq >= s.flushedLeafCount {
					needBuf = true
					break
				}
			}
			if needBuf {
				bufCopy = append([]bufferedArtifact(nil), s.buffer...)
			}
		}
	}
	s.mu.Unlock()
	loaded, err := s.loadHotSuffix(prefix, dirty, bufCopy)
	s.mu.Lock()
	if err != nil {
		return nil, err
	}
	if s.closed {
		return nil, ErrStoreClosed
	}
	if h := s.findHot(prefix); h != nil {
		return h, nil
	}
	if err := s.evictHotIfNeeded(); err != nil {
		return nil, err
	}
	s.hot = append(s.hot, loaded)
	return loaded, nil
}

func (s *SMSTArtifactStore) evictHotIfNeeded() error {
	if len(s.hot) < smstHotSuffixCap {
		return nil
	}
	evict := s.hot[0]
	s.hot = s.hot[1:]
	if evict == nil || evict.tree == nil {
		return nil
	}
	root, count := evict.tree.GetRoot()
	meta := s.ensureSuffixMeta(evict.prefix)
	meta.hash = append([]byte(nil), root...)
	meta.count = count
	s.smst.attachSealedCOW(fullNonce(evict.prefix, 0), &smstNode{hash: root, count: count})
	return nil
}

func (s *SMSTArtifactStore) loadHotSuffix(prefix uint32, dirty []suffixRec, buffer []bufferedArtifact) (*hotSuffix, error) {
	h := &hotSuffix{
		prefix:   prefix,
		nonceSeq: make(map[int32]uint32),
	}
	recs, err := s.readSuffixLog(prefix, ^uint32(0), dirty)
	if err != nil {
		return nil, err
	}
	h.tree = NewSMST(smstSuffixHeight)
	h.tree.deferredHash = s.smst.deferredHash
	h.tree.parallelHash = s.smst.parallelHash
	for _, rec := range recs {
		leafHash, err := s.leafHashAtSeqBuffered(rec.seq, rec.nonce, buffer)
		if err != nil {
			return nil, err
		}
		if _, err := h.tree.insertCOW(suffixLocal(rec.nonce), leafHash); err != nil {
			return nil, err
		}
		h.nonceSeq[rec.nonce] = rec.seq
	}
	return h, nil
}

func (s *SMSTArtifactStore) readSuffixLog(prefix uint32, maxSeq uint32, dirty []suffixRec) ([]suffixRec, error) {
	f, err := os.Open(s.suffixLogPath(prefix))
	if err != nil {
		if os.IsNotExist(err) {
			return appendDirtyRecs(nil, dirty, maxSeq), nil
		}
		return nil, err
	}
	defer f.Close()
	var out []suffixRec
	seen := make(map[uint32]struct{})
	r := bufio.NewReaderSize(f, 64*1024)
	var buf [8]byte
	for {
		if _, err := io.ReadFull(r, buf[:]); err != nil {
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
			if _, ok := seen[rec.seq]; ok {
				continue
			}
			out = append(out, rec)
			seen[rec.seq] = struct{}{}
		}
	}
	for _, rec := range dirty {
		if rec.seq < maxSeq {
			if _, ok := seen[rec.seq]; ok {
				continue
			}
			out = append(out, rec)
			seen[rec.seq] = struct{}{}
		}
	}
	return out, nil
}

func appendDirtyRecs(out []suffixRec, dirty []suffixRec, maxSeq uint32) []suffixRec {
	for _, rec := range dirty {
		if rec.seq < maxSeq {
			out = append(out, rec)
		}
	}
	return out
}

func (s *SMSTArtifactStore) writeSuffixLogs(truncate bool) error {
	if err := os.MkdirAll(s.suffixDir(), 0755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if truncate {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	type started struct {
		path string
		size int64
	}
	var written []started
	rollback := func() {
		if truncate {
			return
		}
		for _, st := range written {
			_ = os.Truncate(st.path, st.size)
		}
	}
	for prefix, meta := range s.suffixes {
		if meta == nil || len(meta.dirty) == 0 {
			continue
		}
		logPath := s.suffixLogPath(prefix)
		var startSize int64
		if !truncate {
			if info, err := os.Stat(logPath); err == nil {
				startSize = info.Size()
			}
		}
		f, err := os.OpenFile(logPath, flags, 0644)
		if err != nil {
			rollback()
			return err
		}
		var buf [8]byte
		for _, rec := range meta.dirty {
			binary.LittleEndian.PutUint32(buf[0:4], uint32(rec.nonce))
			binary.LittleEndian.PutUint32(buf[4:8], rec.seq)
			if _, err := f.Write(buf[:]); err != nil {
				f.Close()
				rollback()
				if !truncate {
					_ = os.Truncate(logPath, startSize)
				}
				return err
			}
		}
		if err := f.Sync(); err != nil {
			f.Close()
			rollback()
			if !truncate {
				_ = os.Truncate(logPath, startSize)
			}
			return err
		}
		if err := f.Close(); err != nil {
			rollback()
			if !truncate {
				_ = os.Truncate(logPath, startSize)
			}
			return err
		}
		if meta.hash != nil {
			if err := s.writeSeal(prefix, meta.hash, meta.count); err != nil {
				rollback()
				if !truncate {
					_ = os.Truncate(logPath, startSize)
				}
				return err
			}
		}
		written = append(written, started{path: logPath, size: startSize})
	}
	return nil
}

func (s *SMSTArtifactStore) clearDirtySuffixes() {
	for _, meta := range s.suffixes {
		if meta != nil {
			meta.dirty = meta.dirty[:0]
		}
	}
}

func (s *SMSTArtifactStore) collectDirtyDeltas() []sealDelta {
	var out []sealDelta
	for prefix, meta := range s.suffixes {
		if meta == nil || len(meta.dirty) == 0 || meta.hash == nil {
			continue
		}
		out = append(out, sealDelta{
			Prefix: prefix,
			Hash:   hex.EncodeToString(meta.hash),
			Count:  meta.count,
			Last:   meta.lastSeq,
		})
	}
	return out
}

func (s *SMSTArtifactStore) appendSealJournal(count uint32, seals []sealDelta) error {
	if count == 0 || len(seals) == 0 {
		return nil
	}
	if err := os.MkdirAll(s.suffixDir(), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(sealJournalEntry{Count: count, Seals: seals})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.suffixHistoryPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *SMSTArtifactStore) hashHotSuffixes() {
	for _, h := range s.hot {
		if h == nil || h.tree == nil {
			continue
		}
		root, count := h.tree.GetRoot()
		meta := s.ensureSuffixMeta(h.prefix)
		meta.hash = append([]byte(nil), root...)
		meta.count = count
		s.smst.attachSealedCOW(fullNonce(h.prefix, 0), &smstNode{hash: root, count: count})
	}
}

func (s *SMSTArtifactStore) flushPagedSuffixes() error {
	s.hashHotSuffixes()
	deltas := s.collectDirtyDeltas()
	if err := s.writeSuffixLogs(false); err != nil {
		return err
	}
	if err := s.appendSealJournal(s.smst.Count(), deltas); err != nil {
		return err
	}
	s.clearDirtySuffixes()
	return nil
}

func (s *SMSTArtifactStore) hasDirtySuffixes() bool {
	for _, meta := range s.suffixes {
		if meta != nil && len(meta.dirty) > 0 {
			return true
		}
	}
	return false
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
	tmp := s.suffixMarkerPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0644); err != nil {
		return err
	}
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.suffixMarkerPath()); err != nil {
		return err
	}
	return syncDir(s.suffixDir())
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
	s.suffixes = make(map[uint32]*suffixMeta)
	s.bitmap = nonceBitmap{}
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
	if err := s.writeSuffixLogs(true); err != nil {
		return err
	}
	s.clearDirtySuffixes()
	s.smst.ensureHashed()
	if err := s.persistSealedCuts(); err != nil {
		return err
	}
	deltas := make([]sealDelta, 0, len(s.suffixes))
	for prefix, meta := range s.suffixes {
		if meta == nil || meta.hash == nil {
			continue
		}
		deltas = append(deltas, sealDelta{
			Prefix: prefix,
			Hash:   hex.EncodeToString(meta.hash),
			Count:  meta.count,
			Last:   meta.lastSeq,
		})
	}
	if err := s.appendSealJournal(s.smst.Count(), deltas); err != nil {
		return err
	}
	if err := s.writeSpillMarker(); err != nil {
		return err
	}
	s.smst.sealAtCut(smstSuffixHeight)
	s.spilled = true
	s.enableSpilledExistence()
	s.offsets = nil
	s.nonceToOffset = nil
	s.hot = nil
	globalSnapshotCache.purgeStore(s)
	s.retained = make(map[uint32]smstSnapshot)
	s.smst.ensureHashed()
	if err := s.recaptureCommittedUppers(); err != nil {
		return err
	}
	s.captureRetainedLocked()
	return nil
}

func (s *SMSTArtifactStore) persistSealedCuts() error {
	if s.smst.root == nil {
		return nil
	}
	cut := s.smst.cutLevel()
	if cut < 0 {
		cut = s.smst.depth - smstSuffixHeight
	}
	if cut < 0 {
		return nil
	}
	var walkErr error
	var walk func(node *smstNode, level int, prefix uint32)
	walk = func(node *smstNode, level int, prefix uint32) {
		if node == nil || walkErr != nil {
			return
		}
		if level == cut {
			meta := s.ensureSuffixMeta(prefix)
			meta.hash = append([]byte(nil), node.hash...)
			meta.count = node.count
			if err := s.writeSeal(prefix, node.hash, node.count); err != nil {
				walkErr = err
			}
			return
		}
		walk(node.left, level+1, prefix<<1)
		walk(node.right, level+1, prefix<<1|1)
	}
	walk(s.smst.root, 0, 0)
	return walkErr
}

func (s *SMSTArtifactStore) enableSpilledExistence() {
	s.smst.navExistence = true
	s.smst.hasNonce = nil
	s.smst.cutExists = func(nonce int32) bool {
		return s.bitmap.has(nonce)
	}
	s.smst.cutDenseIndex = func(nonce int32) (uint32, error) {
		return s.suffixLocalDenseIndex(nonce, s.smst.Count())
	}
}

func (s *SMSTArtifactStore) nonceInSnapshot(nonce int32, count uint32) bool {
	if !s.bitmap.has(nonce) && !s.bufferHasNonce(nonce) {
		return false
	}
	prefix := suffixPrefix(nonce)
	if h := s.findHot(prefix); h != nil {
		if seq, ok := h.nonceSeq[nonce]; ok {
			return seq < count
		}
	}
	if meta := s.suffixes[prefix]; meta != nil {
		if meta.firstSeq >= count {
			return false
		}
		if meta.lastSeq < count {
			return true
		}
	}
	seq, ok := s.seqForNonce(nonce)
	if !ok {
		return false
	}
	return seq < count
}

func (s *SMSTArtifactStore) suffixLocalDenseIndex(nonce int32, snapshotCount uint32) (uint32, error) {
	prefix := suffixPrefix(nonce)
	liveTip := snapshotCount == s.smst.Count()
	suf, _, err := s.suffixStateForProof(prefix, snapshotCount, liveTip)
	if err != nil {
		return 0, err
	}
	return suf.denseIndexForNonce(suffixLocal(nonce))
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
	info, err := s.dataFile.Stat()
	if err != nil {
		return err
	}
	if recSize <= 0 {
		return fmt.Errorf("paged recover: invalid record size %d", recSize)
	}
	n := uint32(info.Size() / int64(recSize))
	if rem := info.Size() % int64(recSize); rem != 0 {
		if err := s.dataFile.Truncate(int64(n) * int64(recSize)); err != nil {
			return fmt.Errorf("truncate orphan data: %w", err)
		}
	}
	s.pagedRecordSize = recSize
	s.flushedLeafCount = n
	s.flushedDataOffset = uint64(n) * uint64(recSize)
	s.spilled = true
	s.bitmap.chunks = make(map[uint32][]uint64)
	s.suffixes = make(map[uint32]*suffixMeta)
	s.smst = NewSMST(smstDefaultDepth)
	s.smst.deferredHash = smstDeferredHashFromEnv()
	s.smst.parallelHash = smstParallelHashFromEnv()
	s.smst.cutHeight = smstSuffixHeight
	s.enableSpilledExistence()

	if err := s.reconcileSuffixLogs(n); err != nil {
		return err
	}
	var total uint32
	for prefix, meta := range s.suffixes {
		if meta == nil || meta.count == 0 {
			continue
		}
		hash, count, sealed := s.readSeal(prefix)
		if !sealed || count != meta.count {
			tree, err := s.rebuildSuffixTree(prefix, n, nil, n)
			if err != nil {
				return err
			}
			hash, count = tree.GetRoot()
			meta.hash = append([]byte(nil), hash...)
			if err := s.writeSeal(prefix, hash, count); err != nil {
				return err
			}
		} else {
			meta.hash = hash
		}
		s.smst.attachSealedCOW(fullNonce(prefix, 0), &smstNode{hash: meta.hash, count: count})
		total += count
	}
	s.smst.leafCount = total
	s.smst.ensureHashed()
	if s.smst.root != nil {
		if prev, ok := s.flushedRoots[total]; ok && prev != nil && !bytes.Equal(prev, s.smst.root.hash) {
			log.Printf("warning: flushed root mismatch at count %d: persisted=%x recomputed=%x",
				total, prev, s.smst.root.hash)
		} else if total > 0 {
			s.flushedRoots[total] = s.smst.root.hash
		}
	}
	if err := s.replaySealJournal(); err != nil {
		return err
	}
	if err := s.recaptureCommittedUppers(); err != nil {
		return err
	}
	s.captureRetainedLocked()
	return nil
}

func (s *SMSTArtifactStore) reconcileSuffixLogs(n uint32) error {
	entries, err := os.ReadDir(s.suffixDir())
	if err != nil {
		return err
	}
	words := (uint64(n) + 63) / 64
	seen := make([]uint64, words)
	var seenN uint32
	dup := false
	for _, e := range entries {
		name := e.Name()
		hexPrefix, ok := strings.CutSuffix(name, ".log")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(hexPrefix, 16, 32)
		if err != nil {
			continue
		}
		prefix := uint32(p)
		recs, err := s.readSuffixLog(prefix, n, nil)
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
			if rec.seq >= n {
				continue
			}
			i := rec.seq / 64
			bit := uint64(1) << (rec.seq % 64)
			if seen[i]&bit != 0 {
				dup = true
			} else {
				seen[i] |= bit
				seenN++
			}
			s.bitmap.set(rec.nonce)
		}
	}
	if !dup && seenN == n {
		return nil
	}
	return s.rebuildSuffixLogsFromData(n)
}

func (s *SMSTArtifactStore) rebuildSuffixLogsFromData(n uint32) error {
	s.suffixes = make(map[uint32]*suffixMeta)
	s.bitmap = nonceBitmap{}
	if _, err := s.dataFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	for seq := uint32(0); seq < n; seq++ {
		nonce, _, _, err := readArtifact(s.dataFile)
		if err != nil {
			return fmt.Errorf("rebuild suffix logs at seq %d: %w", seq, err)
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
	}
	if err := s.writeSuffixLogs(true); err != nil {
		return err
	}
	s.clearDirtySuffixes()
	return s.removeOrphanSuffixFiles()
}

func (s *SMSTArtifactStore) removeOrphanSuffixFiles() error {
	entries, err := os.ReadDir(s.suffixDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		name := e.Name()
		hexPrefix, ok := strings.CutSuffix(name, ".log")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(hexPrefix, 16, 32)
		if err != nil {
			continue
		}
		prefix := uint32(p)
		if s.suffixes[prefix] != nil {
			continue
		}
		_ = os.Remove(s.suffixLogPath(prefix))
		_ = os.Remove(s.suffixSealPath(prefix))
		_ = os.Remove(s.suffixTreePath(prefix))
	}
	return nil
}

func (s *SMSTArtifactStore) replaySealJournal() error {
	f, err := os.Open(s.suffixHistoryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	tree := NewSMST(smstDefaultDepth)
	tree.deferredHash = s.smst.deferredHash
	tree.parallelHash = s.smst.parallelHash
	tree.cutHeight = smstSuffixHeight
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return err
		}
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var entry sealJournalEntry
			if json.Unmarshal(line, &entry) != nil || entry.Count == 0 {
				log.Printf("warning: skip corrupt seal journal line")
			} else if entry.Count > s.smst.Count() {
				log.Printf("warning: skip seal journal count %d beyond recovered %d", entry.Count, s.smst.Count())
			} else {
				for _, d := range entry.Seals {
					hash, decErr := hex.DecodeString(d.Hash)
					if decErr != nil || len(hash) == 0 {
						log.Printf("warning: skip corrupt seal journal delta prefix %d", d.Prefix)
						continue
					}
					tree.attachSealedCOW(fullNonce(d.Prefix, 0), &smstNode{hash: hash, count: d.Count})
				}
				tree.leafCount = entry.Count
				tree.ensureHashed()
				if tree.root != nil && tree.root.count == entry.Count {
					s.retained[entry.Count] = tree.snapshot()
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	return nil
}

func (s *SMSTArtifactStore) recaptureCommittedUppers() error {
	counts := make([]uint32, 0, len(s.flushedRoots)+1)
	seen := make(map[uint32]struct{}, len(s.flushedRoots)+1)
	for c := range s.flushedRoots {
		if c == 0 {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		counts = append(counts, c)
	}
	if tip := s.smst.Count(); tip > 0 {
		if _, ok := seen[tip]; !ok {
			counts = append(counts, tip)
		}
	}
	slices.Sort(counts)
	var prev uint32
	var tree *SMST
	for _, c := range counts {
		if _, ok := s.retained[c]; ok {
			prev = c
			tree = nil
			continue
		}
		if tree == nil || prev == 0 {
			built, err := s.buildPagedUpperAt(c)
			if err != nil {
				return err
			}
			tree = built
		} else {
			if err := s.updatePagedUpper(tree, prev, c); err != nil {
				return err
			}
		}
		s.retained[c] = tree.snapshot()
		prev = c
	}
	return nil
}

func (s *SMSTArtifactStore) updatePagedUpper(tree *SMST, prev, count uint32) error {
	for prefix, meta := range s.suffixes {
		if meta == nil || meta.firstSeq >= count {
			continue
		}
		if meta.lastSeq < prev {
			continue
		}
		if meta.lastSeq < count && meta.hash != nil && meta.lastSeq+1 <= count {
			tree.attachSealedCOW(fullNonce(prefix, 0), &smstNode{hash: meta.hash, count: meta.count})
			continue
		}
		suf, err := s.rebuildSuffixTree(prefix, count, meta.dirty, s.flushedLeafCount)
		if err != nil {
			return err
		}
		if suf.Count() == 0 {
			continue
		}
		root, c := suf.GetRoot()
		tree.attachSealedCOW(fullNonce(prefix, 0), &smstNode{hash: root, count: c})
	}
	tree.ensureHashed()
	got := uint32(0)
	if tree.root != nil {
		got = tree.root.count
	}
	if got != count {
		return fmt.Errorf("paged historical count %d != requested %d", got, count)
	}
	tree.leafCount = count
	return nil
}

func (s *SMSTArtifactStore) copySuffixMeta() map[uint32]suffixMeta {
	out := make(map[uint32]suffixMeta, len(s.suffixes))
	for p, m := range s.suffixes {
		if m == nil {
			continue
		}
		cp := *m
		cp.hash = append([]byte(nil), m.hash...)
		cp.dirty = append([]suffixRec(nil), m.dirty...)
		out[p] = cp
	}
	return out
}

func (s *SMSTArtifactStore) buildPagedUpperAtUnlocked(count uint32, metas map[uint32]suffixMeta, fileThrough uint32) (*SMST, error) {
	tree := NewSMST(smstDefaultDepth)
	tree.deferredHash = s.smst.deferredHash
	tree.parallelHash = s.smst.parallelHash
	tree.cutHeight = smstSuffixHeight
	var total uint32
	for prefix, meta := range metas {
		if meta.firstSeq >= count {
			continue
		}
		if meta.lastSeq < count && meta.hash != nil && meta.count > 0 && meta.lastSeq+1 <= count {
			tree.attachSealedCOW(fullNonce(prefix, 0), &smstNode{hash: meta.hash, count: meta.count})
			total += meta.count
			continue
		}
		suf, err := s.rebuildSuffixTree(prefix, count, meta.dirty, fileThrough)
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

func (s *SMSTArtifactStore) rebuildSuffixTree(prefix uint32, maxSeq uint32, dirty []suffixRec, fileThrough uint32) (*SMST, error) {
	tree, _, err := s.rebuildSuffixState(prefix, maxSeq, dirty, fileThrough)
	return tree, err
}

func (s *SMSTArtifactStore) rebuildSuffixState(prefix uint32, maxSeq uint32, dirty []suffixRec, fileThrough uint32) (*SMST, map[int32]uint32, error) {
	recs, err := s.readSuffixLog(prefix, maxSeq, dirty)
	if err != nil {
		return nil, nil, err
	}
	tree := NewSMST(smstSuffixHeight)
	tree.deferredHash = s.smst.deferredHash
	tree.parallelHash = false
	seqs := make(map[int32]uint32, len(recs))
	for _, rec := range recs {
		var leafHash []byte
		var err error
		if s.pagedRecordSize > 0 && rec.seq < fileThrough {
			leafHash, err = s.leafHashAtFileSeq(rec.seq, rec.nonce)
		} else {
			leafHash, err = s.leafHashAtSeq(rec.seq, rec.nonce)
		}
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
		suf, err := s.rebuildSuffixTree(prefix, count, meta.dirty, s.flushedLeafCount)
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

func (s *SMSTArtifactStore) leafHashAtFileSeq(seq uint32, nonce int32) ([]byte, error) {
	fileNonce, vector, _, err := readArtifactAt(s.dataFile, int64(seq)*int64(s.pagedRecordSize))
	if err != nil {
		return nil, err
	}
	if fileNonce != nonce {
		return nil, fmt.Errorf("artifact seq %d nonce %d does not match %d", seq, fileNonce, nonce)
	}
	return smstHashLeaf(encodeLeaf(nonce, vector)), nil
}

func (s *SMSTArtifactStore) leafHashAtSeq(seq uint32, nonce int32) ([]byte, error) {
	return s.leafHashAtSeqBuffered(seq, nonce, s.buffer)
}

func (s *SMSTArtifactStore) leafHashAtSeqBuffered(seq uint32, nonce int32, buffer []bufferedArtifact) ([]byte, error) {
	vec, err := s.vectorAtSeqBuffered(seq, nonce, buffer)
	if err != nil {
		return nil, err
	}
	return smstHashLeaf(encodeLeaf(nonce, vec)), nil
}

func (s *SMSTArtifactStore) vectorAtSeq(seq uint32, nonce int32) ([]byte, error) {
	return s.vectorAtSeqBuffered(seq, nonce, s.buffer)
}

func (s *SMSTArtifactStore) vectorAtSeqBuffered(seq uint32, nonce int32, buffer []bufferedArtifact) ([]byte, error) {
	if seq >= s.flushedLeafCount {
		idx := int(seq - s.flushedLeafCount)
		if idx >= 0 && idx < len(buffer) && buffer[idx].nonce == nonce {
			return buffer[idx].vector, nil
		}
		for _, art := range buffer {
			if art.nonce == nonce {
				return art.vector, nil
			}
		}
		return nil, ErrNonceNotFound
	}
	if s.pagedRecordSize > 0 {
		fileNonce, vector, _, err := readArtifactAt(s.dataFile, int64(seq)*int64(s.pagedRecordSize))
		if err != nil {
			return nil, err
		}
		if fileNonce != nonce {
			return nil, fmt.Errorf("artifact seq %d nonce %d does not match %d", seq, fileNonce, nonce)
		}
		return vector, nil
	}
	if offset, ok := s.nonceToOffset[nonce]; ok {
		fileNonce, vector, _, err := readArtifactAt(s.dataFile, int64(offset))
		if err != nil {
			return nil, err
		}
		if fileNonce != nonce {
			return nil, fmt.Errorf("artifact seq %d nonce %d does not match %d", seq, fileNonce, nonce)
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
	var dirty []suffixRec
	if meta := s.suffixes[prefix]; meta != nil {
		dirty = meta.dirty
	}
	recs, err := s.readSuffixLog(prefix, ^uint32(0), dirty)
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

func (s *SMSTArtifactStore) pagedProofsFromTree(tree *SMST, denseIndices []uint32, snapshotCount uint32) ([]ProofEntry, error) {
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

	liveTip := snapshotCount == s.smst.Count()
	entries := make([]ProofEntry, len(denseIndices))
	for prefix, group := range byPrefix {
		suf, seqs, err := s.suffixStateForProof(prefix, snapshotCount, liveTip)
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

func (s *SMSTArtifactStore) pagedProofsByNonceFromTree(tree *SMST, nonces []int32, snapshotCount uint32) ([]ProofEntry, error) {
	liveTip := snapshotCount == s.smst.Count()
	var (
		cachedPrefix uint32
		cachedTree   *SMST
		cachedSeqs   map[int32]uint32
		haveCache    bool
	)
	entries := make([]ProofEntry, 0, len(nonces))
	for _, nonce := range nonces {
		if !s.bitmap.has(nonce) && !s.bufferHasNonce(nonce) {
			continue
		}
		prefix := suffixPrefix(nonce)
		if !haveCache || cachedPrefix != prefix {
			suf, seqs, err := s.suffixStateForProof(prefix, snapshotCount, liveTip)
			if err != nil {
				return nil, err
			}
			cachedPrefix, cachedTree, cachedSeqs, haveCache = prefix, suf, seqs, true
		}
		if cachedTree.Count() == 0 {
			continue
		}
		local := suffixLocal(nonce)
		if !cachedTree.HasNonce(local) {
			continue
		}
		denseLocal, err := cachedTree.denseIndexForNonce(local)
		if err != nil {
			return nil, err
		}
		seq, ok := cachedSeqs[nonce]
		if !ok {
			return nil, ErrNonceNotFound
		}
		vector, err := s.vectorAtSeq(seq, nonce)
		if err != nil {
			return nil, err
		}
		elems := append(append([]smstProofElement(nil), tree.proofElementsToCut(nonce)...), s.buildProofWithCounts(cachedTree, local)...)
		entries = append(entries, ProofEntry{
			DenseIndex: s.upperBase(tree, nonce) + denseLocal,
			Nonce:      nonce,
			Vector:     vector,
			Proof:      encodeProofForTransport(elems),
		})
	}
	return entries, nil
}

func (s *SMSTArtifactStore) suffixStateForProof(prefix uint32, snapshotCount uint32, liveTip bool) (*SMST, map[int32]uint32, error) {
	if liveTip {
		if h := s.findHot(prefix); h != nil && h.tree != nil {
			return h.tree, h.nonceSeq, nil
		}
	}
	var dirty []suffixRec
	if meta := s.suffixes[prefix]; meta != nil {
		dirty = meta.dirty
	}
	return s.rebuildSuffixState(prefix, snapshotCount, dirty, s.flushedLeafCount)
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
