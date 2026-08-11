package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	// defaultAggregateMaxResponseBytes is the per-request wire-body ceiling when
	// a spool directory is available (disk-backed accumulation). It is also the
	// real peak-RAM budget for the accumulated body when folding still materializes
	// via Bytes(); prefer OpenReader + aggregateSSEStreamReader so spilled bodies
	// are folded line-by-line without a full ReadAll.
	defaultAggregateMaxResponseBytes = int64(16 << 20) // 16 MiB

	// defaultAggregateMaxMemoryBytes is the in-process RAM budget for one
	// aggregated request before spilling. Used as the hard ceiling when the
	// spool is unavailable (or at concurrent spool-file capacity), and as the
	// spill threshold when disk is available.
	defaultAggregateMaxMemoryBytes = int64(2 << 20) // 2 MiB

	// defaultAggregateMaxConcurrentSpools caps how many agg-*.sse files may
	// exist at once. Worst-case disk ≈ this × maxResponseBytes.
	defaultAggregateMaxConcurrentSpools = 64

	// defaultAggregateMaxDegradedRAMBytes is the process-wide ceiling on RAM
	// held by buffers that wanted to spill and could not. Without it, spool
	// exhaustion (which needs no disk fault — just maxConcurrentSpools busy
	// requests) silently promotes every further aggregated request from the
	// 2 MiB memory budget to the 16 MiB wire budget, with nothing bounding
	// concurrency × 16 MiB. Requests that cannot claim a share keep the memory
	// budget instead, so the degrade stays bounded rather than unbounded.
	defaultAggregateMaxDegradedRAMBytes = int64(128 << 20) // 128 MiB

	aggregateSpoolDirName = "aggregate-spool"

	// aggregateSpoolWriteBufferBytes buffers spooled writes so a long stream
	// costs one write(2) per bufferful instead of one per SSE chunk.
	aggregateSpoolWriteBufferBytes = 64 << 10
)

// ErrAggregateResponseTooLarge is returned when an aggregated (non-stream
// client) response exceeds the configured byte ceiling. Never truncate — abort
// so redundancy can treat it as an attempt failure.
var ErrAggregateResponseTooLarge = errors.New("aggregate response exceeds size limit")

// ErrAggregateFoldTooLarge is returned when accumulated fold state (text
// builders + logprobs) exceeds the fold RAM/disk budget (R4). Distinct from
// ErrAggregateResponseTooLarge, which caps the raw SSE body buffer.
var ErrAggregateFoldTooLarge = errors.New("aggregate fold exceeds size limit")

// aggregateSlotSem is a counting semaphore. reset updates max without zeroing
// cur, so a reset while slots are held cannot inflate available capacity (R6).
type aggregateSlotSem struct {
	mu  sync.Mutex
	max int64
	cur int64
}

func (s *aggregateSlotSem) tryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.max < 1 {
		return true
	}
	if s.cur >= s.max {
		return false
	}
	s.cur++
	return true
}

func (s *aggregateSlotSem) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur > 0 {
		s.cur--
	}
}

func (s *aggregateSlotSem) setMax(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 1 {
		n = 1
	}
	s.max = n
}

func (s *aggregateSlotSem) snapshot() (max, cur int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.max, s.cur
}

func (s *aggregateSlotSem) restore(max, cur int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if max < 1 {
		max = 1
	}
	if cur < 0 {
		cur = 0
	}
	s.max = max
	s.cur = cur
}

var (
	// aggregateConfigMu guards byte limits + spool dir. Slot semaphores carry
	// their own locks so acquire/release stay off the config critical path.
	aggregateConfigMu            sync.RWMutex
	aggregateMaxResponseBytes    = defaultAggregateMaxResponseBytes
	aggregateMaxMemoryBytes      = defaultAggregateMaxMemoryBytes
	aggregateMaxConcurrentSpools = defaultAggregateMaxConcurrentSpools
	aggregateMaxDegradedRAMBytes = defaultAggregateMaxDegradedRAMBytes
	aggregateSpoolDir            string

	aggregateSpoolSem = aggregateSlotSem{max: int64(defaultAggregateMaxConcurrentSpools)}
	// Sized before configure runs so the degraded-RAM bound holds even on code
	// paths that never call configureAggregateResponseFromEnv.
	aggregateDegradedSem = aggregateSlotSem{max: defaultAggregateMaxDegradedRAMBytes / defaultAggregateMaxResponseBytes}

	aggregateSpillDegradeTotal    atomic.Uint64
	aggregateSpoolCapDegradeTotal atomic.Uint64
	// aggregateDegradedRefusedTotal counts buffers that degraded to RAM while the
	// degraded-RAM budget was already fully claimed, so they kept the memory
	// ceiling. A non-zero value means aggregated requests are failing at
	// maxMemory rather than maxBytes.
	aggregateDegradedRefusedTotal atomic.Uint64
)

// configureAggregateResponseFromEnv loads F4 caps and prepares the spool dir
// under the gateway storage root (or GATEWAY_AGGREGATE_SPOOL_DIR). A missing /
// unusable spool leaves mem-only mode (2 MiB ceiling).
func configureAggregateResponseFromEnv(baseStorageDir string) {
	maxResp := positiveInt64Env("GATEWAY_AGGREGATE_MAX_RESPONSE_BYTES", defaultAggregateMaxResponseBytes)
	maxMem := positiveInt64Env("GATEWAY_AGGREGATE_MAX_MEMORY_BYTES", defaultAggregateMaxMemoryBytes)
	if maxMem > maxResp {
		maxMem = maxResp
	}
	maxConc := int(positiveInt64Env("GATEWAY_AGGREGATE_MAX_CONCURRENT_SPOOLS", int64(defaultAggregateMaxConcurrentSpools)))
	if maxConc < 1 {
		maxConc = defaultAggregateMaxConcurrentSpools
	}
	maxDegraded := positiveInt64Env("GATEWAY_AGGREGATE_MAX_DEGRADED_RAM_BYTES", defaultAggregateMaxDegradedRAMBytes)

	setAggregateByteLimits(maxMem, maxResp, maxConc, maxDegraded)
	resetAggregateSpoolSlots(maxConc)
	resetAggregateDegradedSlots(maxDegraded, maxResp)

	dir := strings.TrimSpace(os.Getenv("GATEWAY_AGGREGATE_SPOOL_DIR"))
	if dir == "" && strings.TrimSpace(baseStorageDir) != "" {
		dir = filepath.Join(baseStorageDir, aggregateSpoolDirName)
	}
	if dir == "" {
		setAggregateSpoolDir("")
		log.Printf("aggregate_response spool disabled; mem_limit_bytes=%d", maxMem)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		setAggregateSpoolDir("")
		log.Printf("aggregate_response spool unavailable (%v); mem_limit_bytes=%d", err, maxMem)
		return
	}
	if err := probeAggregateSpoolWritable(dir); err != nil {
		setAggregateSpoolDir("")
		log.Printf("aggregate_response spool not writable (%v); mem_limit_bytes=%d", err, maxMem)
		return
	}
	clearAggregateSpool(dir)
	setAggregateSpoolDir(dir)
	log.Printf("aggregate_response spool=%s max_response_bytes=%d max_memory_bytes=%d max_concurrent_spools=%d max_degraded_ram_bytes=%d",
		dir, maxResp, maxMem, maxConc, maxDegraded)
}

func positiveInt64Env(name string, fallback int64) int64 {
	v := readInt64Env(name, fallback)
	if v <= 0 {
		return fallback
	}
	return v
}

func setAggregateByteLimits(maxMem, maxResp int64, maxConc int, maxDegraded int64) {
	aggregateConfigMu.Lock()
	defer aggregateConfigMu.Unlock()
	aggregateMaxMemoryBytes = maxMem
	aggregateMaxResponseBytes = maxResp
	aggregateMaxConcurrentSpools = maxConc
	aggregateMaxDegradedRAMBytes = maxDegraded
}

// setAggregateByteLimitsForTest updates the per-request byte ceilings (tests).
func setAggregateByteLimitsForTest(maxMem, maxResp int64) {
	aggregateConfigMu.Lock()
	defer aggregateConfigMu.Unlock()
	aggregateMaxMemoryBytes = maxMem
	aggregateMaxResponseBytes = maxResp
}

func currentAggregateByteLimits() (maxMem, maxResp int64) {
	aggregateConfigMu.RLock()
	defer aggregateConfigMu.RUnlock()
	return aggregateMaxMemoryBytes, aggregateMaxResponseBytes
}

func currentAggregateMaxConcurrentSpools() int {
	aggregateConfigMu.RLock()
	defer aggregateConfigMu.RUnlock()
	return aggregateMaxConcurrentSpools
}

func currentAggregateMaxDegradedRAMBytes() int64 {
	aggregateConfigMu.RLock()
	defer aggregateConfigMu.RUnlock()
	return aggregateMaxDegradedRAMBytes
}

func setAggregateSpoolDir(dir string) {
	aggregateConfigMu.Lock()
	defer aggregateConfigMu.Unlock()
	aggregateSpoolDir = dir
}

func currentAggregateSpoolDir() string {
	aggregateConfigMu.RLock()
	defer aggregateConfigMu.RUnlock()
	return aggregateSpoolDir
}

// currentAggregateBufferConfig returns the limits snapshotted for a new buffer
// or fold (byte ceilings + spool dir) under one lock.
func currentAggregateBufferConfig() (maxMem, maxResp int64, spool string) {
	aggregateConfigMu.RLock()
	defer aggregateConfigMu.RUnlock()
	return aggregateMaxMemoryBytes, aggregateMaxResponseBytes, aggregateSpoolDir
}

func resetAggregateSpoolSlots(n int) {
	if n < 1 {
		n = defaultAggregateMaxConcurrentSpools
	}
	aggregateConfigMu.Lock()
	aggregateMaxConcurrentSpools = n
	aggregateConfigMu.Unlock()
	aggregateSpoolSem.setMax(int64(n))
}

func tryAcquireAggregateSpoolSlot() bool {
	return aggregateSpoolSem.tryAcquire()
}

func releaseAggregateSpoolSlot() {
	aggregateSpoolSem.release()
}

func aggregateSpoolSlotCapacity() int {
	max, _ := aggregateSpoolSem.snapshot()
	return int(max)
}

// resetAggregateDegradedSlots sizes the degraded-RAM budget as a slot count:
// each slot lets one buffer grow from maxMemory to perRequest bytes in RAM, so
// worst-case degraded RAM is budget rounded down to a whole number of requests.
// In-flight holders keep counting against the new max (no capacity inflation).
func resetAggregateDegradedSlots(budget, perRequest int64) {
	if perRequest <= 0 {
		perRequest = defaultAggregateMaxResponseBytes
	}
	n := int(budget / perRequest)
	if n < 1 {
		n = 1
	}
	aggregateConfigMu.Lock()
	aggregateMaxDegradedRAMBytes = budget
	aggregateConfigMu.Unlock()
	aggregateDegradedSem.setMax(int64(n))
}

func tryAcquireAggregateDegradedSlot() bool {
	return aggregateDegradedSem.tryAcquire()
}

func releaseAggregateDegradedSlot() {
	aggregateDegradedSem.release()
}

func aggregateDegradedSlotCapacity() int {
	max, _ := aggregateDegradedSem.snapshot()
	return int(max)
}

func probeAggregateSpoolWritable(dir string) error {
	f, err := os.CreateTemp(dir, "agg-probe-*.sse")
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.Write([]byte("ok"))
	cerr := f.Close()
	_ = os.Remove(name)
	if werr != nil {
		return werr
	}
	return cerr
}

func clearAggregateSpool(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "agg-") {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// aggregateResponseBuffer accumulates the winner SSE body for handleAggregated.
// It keeps at most maxMemory bytes in RAM before spilling; when a spool dir is
// configured it allows up to maxBytes total on disk. Fold via OpenReader to
// avoid re-loading a spilled body with ReadAll.
//
// When spilling is impossible the buffer degrades to RAM, but only while the
// process-wide degraded-RAM budget has a free share; otherwise it keeps the
// maxMemory ceiling. Worst-case degraded RAM across all in-flight requests is
// therefore aggregateMaxDegradedRAMBytes, not concurrency × maxBytes.
//
// All methods are safe for concurrent Write vs Close (late winner races): a
// mutex serializes buffer state, and RunInference also calls detachClient
// before returning so further client writes are fenced.
type aggregateResponseBuffer struct {
	mu sync.Mutex

	maxMemory int64
	maxBytes  int64
	spoolDir  string
	mem       bytes.Buffer
	file      *os.File
	fileBuf   *bufio.Writer
	path      string
	n         int64
	spilled   bool
	// spillDisabled is set after a spill/cap failure so further writes stay in
	// RAM up to maxBytes instead of hard-failing every retry at maxMemory.
	spillDisabled bool
	holdsSlot     bool
	// holdsDegradedSlot means this buffer claimed a share of the process-wide
	// degraded-RAM budget and may therefore grow past maxMemory without disk.
	holdsDegradedSlot bool
	// writeErr latches the first rejected/failed write. The accumulated bytes
	// are a prefix of the real response from that point on, so Bytes/OpenReader
	// must refuse rather than let a truncated body be folded and served as a
	// complete answer.
	writeErr error
	closed   bool
}

func newAggregateResponseBuffer() *aggregateResponseBuffer {
	memLimit, maxResp, spool := currentAggregateBufferConfig()
	if memLimit <= 0 {
		memLimit = defaultAggregateMaxMemoryBytes
	}
	maxBytes := memLimit
	if spool != "" {
		maxBytes = maxResp
		if maxBytes < memLimit {
			maxBytes = memLimit
		}
	}
	return &aggregateResponseBuffer{
		maxMemory: memLimit,
		maxBytes:  maxBytes,
		spoolDir:  spool,
	}
}

func (b *aggregateResponseBuffer) Write(p []byte) (int, error) {
	if b == nil {
		return 0, errors.New("aggregate response buffer is nil")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, errors.New("aggregate response buffer closed")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if b.writeErr != nil {
		return 0, b.writeErr
	}
	if b.n+int64(len(p)) > b.maxBytes {
		return 0, b.failLocked(fmt.Errorf("%w: %d byte limit", ErrAggregateResponseTooLarge, b.maxBytes))
	}
	if !b.spilled && !b.spillDisabled && int64(b.mem.Len())+int64(len(p)) > b.maxMemory {
		if err := b.spillToDiskLocked(); err != nil {
			// Disk unavailable or spool-file cap reached: degrade to RAM so
			// redundancy does not cascade identical attempt failures. The
			// promotion past maxMemory costs a share of the process-wide
			// degraded-RAM budget; without one this request keeps the memory
			// ceiling rather than silently taking maxBytes of RAM.
			b.spillDisabled = true
			b.spoolDir = ""
			aggregateSpillDegradeTotal.Add(1)
			if tryAcquireAggregateDegradedSlot() {
				b.holdsDegradedSlot = true
				log.Printf("aggregate_response: spill failed (%v); degraded to RAM up to max_bytes=%d", err, b.maxBytes)
			} else {
				aggregateDegradedRefusedTotal.Add(1)
				b.maxBytes = b.maxMemory
				log.Printf("aggregate_response: spill failed (%v); degraded-RAM budget exhausted, keeping max_bytes=%d", err, b.maxBytes)
			}
		}
	}
	// The degrade above may have lowered maxBytes.
	if b.n+int64(len(p)) > b.maxBytes {
		return 0, b.failLocked(fmt.Errorf("%w: %d byte limit", ErrAggregateResponseTooLarge, b.maxBytes))
	}
	if b.spilled {
		n, err := b.fileBuf.Write(p)
		b.n += int64(n)
		if err != nil {
			return n, b.failLocked(err)
		}
		return len(p), nil
	}
	n, err := b.mem.Write(p)
	b.n += int64(n)
	if err != nil {
		return n, b.failLocked(err)
	}
	return n, nil
}

// failLocked latches err so the buffer can never hand back a truncated body.
func (b *aggregateResponseBuffer) failLocked(err error) error {
	if b.writeErr == nil {
		b.writeErr = err
		log.Printf("aggregate_response: write aborted n=%d max_bytes=%d err=%v", b.n, b.maxBytes, err)
	}
	return err
}

func (b *aggregateResponseBuffer) spillToDiskLocked() error {
	if b.spoolDir == "" {
		return errors.New("no spool dir")
	}
	if !tryAcquireAggregateSpoolSlot() {
		aggregateSpoolCapDegradeTotal.Add(1)
		log.Printf("aggregate_response: spool concurrency cap reached (max_concurrent_spools=%d)", currentAggregateMaxConcurrentSpools())
		return errors.New("aggregate spool concurrency limit")
	}
	f, err := os.CreateTemp(b.spoolDir, "agg-*.sse")
	if err != nil {
		releaseAggregateSpoolSlot()
		return err
	}
	path := f.Name()
	// Unlink immediately so a crash cannot leave plaintext responses on disk;
	// the fd keeps the inode alive until Close. clearAggregateSpool still runs
	// at startup for older builds that left named files behind.
	if err := os.Remove(path); err != nil {
		_ = f.Close()
		releaseAggregateSpoolSlot()
		return err
	}
	fileBuf := bufio.NewWriterSize(f, aggregateSpoolWriteBufferBytes)
	if b.mem.Len() > 0 {
		if _, err := fileBuf.Write(b.mem.Bytes()); err != nil {
			_ = f.Close()
			releaseAggregateSpoolSlot()
			return err
		}
	}
	b.file = f
	b.fileBuf = fileBuf
	b.path = "" // already unlinked
	b.holdsSlot = true
	b.mem.Reset()
	b.spilled = true
	return nil
}

// Bytes returns the full accumulated body. Prefer OpenReader for spilled
// buffers so the fold need not hold a second full copy. The in-memory result
// aliases the buffer's own storage: no write happens after RunInference
// returns, and aggregateSSEStream copies (marshals) everything it emits.
// Callers must not retain the slice past Close.
func (b *aggregateResponseBuffer) Bytes() ([]byte, error) {
	if b == nil {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("aggregate response buffer closed")
	}
	if b.writeErr != nil {
		return nil, b.writeErr
	}
	if !b.spilled {
		return b.mem.Bytes(), nil
	}
	if b.file == nil {
		return nil, errors.New("aggregate spool file missing")
	}
	if err := b.fileBuf.Flush(); err != nil {
		return nil, err
	}
	if _, err := b.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(b.file)
}

// OpenReader returns a view of the accumulated body without necessarily
// copying a spilled file into a new []byte. The reader is valid until Close.
// For the in-memory path the returned reader aliases mem; do not Write after
// opening.
func (b *aggregateResponseBuffer) OpenReader() (io.Reader, error) {
	if b == nil {
		return bytes.NewReader(nil), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("aggregate response buffer closed")
	}
	if b.writeErr != nil {
		return nil, b.writeErr
	}
	if !b.spilled {
		return bytes.NewReader(b.mem.Bytes()), nil
	}
	if b.file == nil {
		return nil, errors.New("aggregate spool file missing")
	}
	if err := b.fileBuf.Flush(); err != nil {
		return nil, err
	}
	if _, err := b.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return b.file, nil
}

// Len returns bytes accepted so far.
func (b *aggregateResponseBuffer) Len() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

// Spilled reports whether the body lives on disk.
func (b *aggregateResponseBuffer) Spilled() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spilled
}

// Close removes any spool resources. Safe to call multiple times; concurrent
// with Write (late writes see closed and no-op with error).
func (b *aggregateResponseBuffer) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	var err error
	if b.file != nil {
		// Any bytes still buffered are discarded with the spool inode.
		b.fileBuf = nil
		err = b.file.Close()
		b.file = nil
	}
	if b.path != "" {
		if rmErr := os.Remove(b.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) && err == nil {
			err = rmErr
		}
		b.path = ""
	}
	if b.holdsSlot {
		releaseAggregateSpoolSlot()
		b.holdsSlot = false
	}
	if b.holdsDegradedSlot {
		releaseAggregateDegradedSlot()
		b.holdsDegradedSlot = false
	}
	b.mem.Reset()
	return err
}
