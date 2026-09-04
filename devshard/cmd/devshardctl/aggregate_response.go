package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"devshard/spool"
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

	// defaultAggregateMaxConcurrentSpools caps how many agg-* files may
	// exist at once. Worst-case disk ≈ this × maxResponseBytes.
	defaultAggregateMaxConcurrentSpools = 64

	// defaultAggregateMaxDegradedRAMBytes is the process-wide ceiling on RAM
	// held by buffers that wanted to spill and could not.
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

// aggregateSlotSem wraps spool.Slots with the lowercase snapshot/restore names
// the existing tests use.
type aggregateSlotSem struct {
	*spool.Slots
}

func newAggregateSlotSem(maximum int64) aggregateSlotSem {
	return aggregateSlotSem{Slots: spool.NewSlots(maximum)}
}

func (s aggregateSlotSem) snapshot() (maximum, cur int64) {
	return s.Snapshot()
}

func (s aggregateSlotSem) restore(maximum, cur int64) {
	s.Restore(maximum, cur)
}

func (s aggregateSlotSem) tryAcquire() bool { return s.TryAcquire() }
func (s aggregateSlotSem) release()         { s.Release() }
func (s aggregateSlotSem) setMax(n int64)   { s.SetMax(n) }

var (
	aggregateConfigMu            sync.RWMutex
	aggregateMaxResponseBytes    = defaultAggregateMaxResponseBytes
	aggregateMaxMemoryBytes      = defaultAggregateMaxMemoryBytes
	aggregateMaxConcurrentSpools = defaultAggregateMaxConcurrentSpools
	aggregateMaxDegradedRAMBytes = defaultAggregateMaxDegradedRAMBytes
	aggregateSpoolDir            string
	aggregateDir                 *spool.Dir

	aggregateSpoolSem    = newAggregateSlotSem(int64(defaultAggregateMaxConcurrentSpools))
	aggregateDegradedSem = newAggregateSlotSem(defaultAggregateMaxDegradedRAMBytes / defaultAggregateMaxResponseBytes)

	aggregateSpillDegradeTotal    atomic.Uint64
	aggregateSpoolCapDegradeTotal atomic.Uint64
	aggregateDegradedRefusedTotal atomic.Uint64
)

// configureAggregateResponseFromEnv loads F4 caps and prepares the spool dir
// under the gateway storage root (or GATEWAY_AGGREGATE_SPOOL_DIR). A missing /
// unusable spool leaves mem-only mode (2 MiB ceiling).
func configureAggregateResponseFromEnv(baseStorageDir string) {
	maxResp := positiveInt64Env("GATEWAY_AGGREGATE_MAX_RESPONSE_BYTES", defaultAggregateMaxResponseBytes)
	maxMem := min(positiveInt64Env("GATEWAY_AGGREGATE_MAX_MEMORY_BYTES", defaultAggregateMaxMemoryBytes), maxResp)
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
	d, err := spool.Open(spool.Config{
		Path:         dir,
		Prefix:       "agg-",
		MaxFiles:     int64(maxConc),
		MaxFileBytes: maxResp,
		WriteBuffer:  aggregateSpoolWriteBufferBytes,
		Files:        aggregateSpoolSem.Slots,
	})
	if err != nil {
		setAggregateSpoolDir("")
		log.Printf("aggregate_response spool unavailable (%v); mem_limit_bytes=%d", err, maxMem)
		return
	}
	aggregateConfigMu.Lock()
	aggregateSpoolDir = dir
	aggregateDir = d
	aggregateConfigMu.Unlock()
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
	// Do not Reconfigure/MkdirAll here: tests deliberately point the spool at a
	// missing path to force degrade, then lower the byte ceilings.
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
	if strings.TrimSpace(dir) == "" {
		aggregateDir = nil
		return
	}
	// OpenAt: do not MkdirAll — tests point at missing paths to force degrade.
	d, err := spool.OpenAt(spool.Config{
		Path:           dir,
		Prefix:         "agg-",
		MaxFiles:       int64(aggregateMaxConcurrentSpools),
		MaxFileBytes:   aggregateMaxResponseBytes,
		WriteBuffer:    aggregateSpoolWriteBufferBytes,
		Files:          aggregateSpoolSem.Slots,
		AllowUnlimited: aggregateMaxConcurrentSpools < 1,
	})
	if err != nil {
		aggregateDir = nil
		return
	}
	aggregateDir = d
}

func currentAggregateSpoolDir() string {
	aggregateConfigMu.RLock()
	defer aggregateConfigMu.RUnlock()
	return aggregateSpoolDir
}

// currentAggregateBufferConfig returns the limits snapshotted for a new buffer
// or fold (byte ceilings + spool dir) under one lock.
func currentAggregateBufferConfig() (maxMem, maxResp int64, spoolDir string) {
	aggregateConfigMu.RLock()
	defer aggregateConfigMu.RUnlock()
	return aggregateMaxMemoryBytes, aggregateMaxResponseBytes, aggregateSpoolDir
}

func currentAggregateDir() *spool.Dir {
	aggregateConfigMu.RLock()
	defer aggregateConfigMu.RUnlock()
	return aggregateDir
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
	maximum, _ := aggregateSpoolSem.snapshot()
	return int(maximum)
}

func resetAggregateDegradedSlots(budget, perRequest int64) {
	if perRequest <= 0 {
		perRequest = defaultAggregateMaxResponseBytes
	}
	n := max(int(budget/perRequest), 1)
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

// aggregateResponseBuffer accumulates the winner SSE body for handleAggregated.
// It wraps spool.Buffer, whose accessors are all mutex-guarded. A late winner
// write can race handleNonStreaming's deferred Close, so this type keeps no
// mirrored copy of the inner state: every read goes through inner.
type aggregateResponseBuffer struct {
	inner *spool.Buffer

	maxMemory int64
	// degradeOnce keeps the spill-degrade log and its counters to a single
	// emission per buffer even when concurrent writers all observe the degrade.
	degradeOnce sync.Once
}

func newAggregateResponseBuffer() *aggregateResponseBuffer {
	memLimit, maxResp, spoolPath := currentAggregateBufferConfig()
	if memLimit <= 0 {
		memLimit = defaultAggregateMaxMemoryBytes
	}
	maxBytes := memLimit
	dir := currentAggregateDir()
	if spoolPath != "" {
		maxBytes = max(maxResp, memLimit)
		if dir == nil || !dir.Enabled() {
			// Path set but OpenAt failed — still offer a Dir so spill attempts
			// degrade rather than staying at the memory ceiling silently.
			d, err := spool.OpenAt(spool.Config{
				Path:         spoolPath,
				Prefix:       "agg-",
				MaxFiles:     int64(currentAggregateMaxConcurrentSpools()),
				MaxFileBytes: maxBytes,
				WriteBuffer:  aggregateSpoolWriteBufferBytes,
				Files:        aggregateSpoolSem.Slots,
			})
			if err == nil {
				dir = d
			}
		}
	}
	inner := spool.NewBuffer(spool.BufferConfig{
		Dir:            dir,
		Budget:         spool.NewBudget(memLimit, maxBytes),
		OnSpillFailure: spool.DegradeToRAM,
		Degraded:       aggregateDegradedSem.Slots,
	})
	return &aggregateResponseBuffer{
		inner:     inner,
		maxMemory: memLimit,
	}
}

// maxBytes is the buffer's current ceiling: the disk limit once spilling is
// possible, the memory limit otherwise.
func (b *aggregateResponseBuffer) maxBytes() int64 {
	if b == nil || b.inner == nil {
		return 0
	}
	return b.inner.DiskLimit()
}

func (b *aggregateResponseBuffer) spillDisabled() bool {
	if b == nil || b.inner == nil {
		return false
	}
	return b.inner.SpillDisabled()
}

func (b *aggregateResponseBuffer) holdsDegradedSlot() bool {
	if b == nil || b.inner == nil {
		return false
	}
	return b.inner.HoldsDegradedSlot()
}

func (b *aggregateResponseBuffer) logSpillDegrade() {
	b.degradeOnce.Do(func() {
		spillErr := b.inner.LastSpillErr()
		aggregateSpillDegradeTotal.Add(1)
		if errors.Is(spillErr, spool.ErrNoCapacity) {
			aggregateSpoolCapDegradeTotal.Add(1)
			log.Printf("aggregate_response: spool concurrency cap reached (max_concurrent_spools=%d)", currentAggregateMaxConcurrentSpools())
		}
		if b.holdsDegradedSlot() {
			log.Printf("aggregate_response: spill failed (%v); degraded to RAM up to max_bytes=%d", spillErr, b.maxBytes())
		} else {
			aggregateDegradedRefusedTotal.Add(1)
			log.Printf("aggregate_response: spill failed (%v); degraded-RAM budget exhausted, keeping max_bytes=%d", spillErr, b.maxBytes())
		}
	})
}

func (b *aggregateResponseBuffer) Write(p []byte) (int, error) {
	if b == nil || b.inner == nil {
		return 0, errors.New("aggregate response buffer is nil")
	}
	n, err := b.inner.Write(p)
	if b.spillDisabled() {
		b.logSpillDegrade()
	}
	if err == nil {
		return n, nil
	}
	if errors.Is(err, spool.ErrFileTooLarge) || errors.Is(err, ErrAggregateResponseTooLarge) {
		wrapped := fmt.Errorf("%w: %d byte limit", ErrAggregateResponseTooLarge, b.maxBytes())
		log.Printf("aggregate_response: write aborted n=%d max_bytes=%d err=%v", b.Len(), b.maxBytes(), wrapped)
		return n, wrapped
	}
	if errors.Is(err, spool.ErrClosed) {
		return n, errors.New("aggregate response buffer closed")
	}
	// Latched writeErr from a prior oversize: surface the domain sentinel.
	if we := b.inner.WriteErr(); we != nil && (errors.Is(we, spool.ErrFileTooLarge) || errors.Is(we, ErrAggregateResponseTooLarge)) {
		return n, fmt.Errorf("%w: %d byte limit", ErrAggregateResponseTooLarge, b.maxBytes())
	}
	return n, err
}

func (b *aggregateResponseBuffer) Bytes() ([]byte, error) {
	if b == nil || b.inner == nil {
		return nil, nil
	}
	got, err := b.inner.Bytes()
	if errors.Is(err, spool.ErrFileTooLarge) {
		return nil, fmt.Errorf("%w: %d byte limit", ErrAggregateResponseTooLarge, b.maxBytes())
	}
	if err != nil {
		if we := b.inner.WriteErr(); we != nil && (errors.Is(we, spool.ErrFileTooLarge) || strings.Contains(we.Error(), "byte limit")) {
			return nil, fmt.Errorf("%w: %d byte limit", ErrAggregateResponseTooLarge, b.maxBytes())
		}
		// Map latched oversize from a prior Write.
		if we := b.inner.WriteErr(); we != nil {
			if errors.Is(we, ErrAggregateResponseTooLarge) || errors.Is(we, spool.ErrFileTooLarge) {
				return nil, fmt.Errorf("%w: %d byte limit", ErrAggregateResponseTooLarge, b.maxBytes())
			}
			return nil, we
		}
	}
	return got, err
}

func (b *aggregateResponseBuffer) OpenReader() (io.Reader, error) {
	if b == nil || b.inner == nil {
		return nil, nil
	}
	r, err := b.inner.OpenReader()
	if err != nil {
		if we := b.inner.WriteErr(); we != nil {
			if errors.Is(we, spool.ErrFileTooLarge) || errors.Is(we, ErrAggregateResponseTooLarge) {
				return nil, fmt.Errorf("%w: %d byte limit", ErrAggregateResponseTooLarge, b.maxBytes())
			}
			return nil, we
		}
		if errors.Is(err, spool.ErrClosed) {
			return nil, errors.New("aggregate response buffer closed")
		}
	}
	return r, err
}

func (b *aggregateResponseBuffer) Len() int64 {
	if b == nil || b.inner == nil {
		return 0
	}
	return b.inner.Len()
}

func (b *aggregateResponseBuffer) Spilled() bool {
	if b == nil || b.inner == nil {
		return false
	}
	return b.inner.Spilled()
}

func (b *aggregateResponseBuffer) Close() error {
	if b == nil || b.inner == nil {
		return nil
	}
	return b.inner.Close()
}
