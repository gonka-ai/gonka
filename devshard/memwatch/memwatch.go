// Package memwatch records what a process's memory is doing.
//
// Byte totals come from runtime/metrics, which sums per-processor counters
// without stopping the world. The heap profile is a sample. The counts a
// process adds through Inventory are map lengths, taken by the caller.
package memwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultInterval is how often a logger records a snapshot.
	DefaultInterval = 10 * time.Minute
	// DefaultHeapTopN is how many allocating stacks a snapshot keeps.
	DefaultHeapTopN = 12
	// DefaultStackDepth is how many functions each of those stacks keeps.
	DefaultStackDepth = 8
)

// Process is the whole-process portion of a snapshot. The byte fields match
// the MemStats names the log already uses:
//
//	heap_alloc    live heap objects, including objects not yet marked free
//	heap_inuse    spans that hold heap objects
//	heap_idle     heap spans with no objects, including memory returned to the OS
//	heap_released idle heap already returned to the OS
//	heap_objects  live heap objects
//	stack_inuse   goroutine stacks allocated from the heap
//	sys           bytes obtained from the OS
//
// These are read from runtime/metrics. That path sums counters the runtime
// already keeps and does not stop the world. Bytes still sitting in a
// processor's cache may be missing until the cache is flushed.
//
// NextGC is the heap goal, and GCCPUFraction is GC CPU time divided by total
// CPU time over the life of the process. Neither is written to the log line.
type Process struct {
	HeapAlloc     uint64
	HeapInuse     uint64
	HeapIdle      uint64
	HeapReleased  uint64
	HeapObjects   uint64
	StackInuse    uint64
	Sys           uint64
	Goroutines    int
	NumGC         uint64
	NextGC        uint64
	GCCPUFraction float64
}

const (
	metricHeapObjectsBytes = "/memory/classes/heap/objects:bytes"
	metricHeapUnusedBytes  = "/memory/classes/heap/unused:bytes"
	metricHeapFreeBytes    = "/memory/classes/heap/free:bytes"
	metricHeapReleased     = "/memory/classes/heap/released:bytes"
	metricHeapObjects      = "/gc/heap/objects:objects"
	metricHeapStacks       = "/memory/classes/heap/stacks:bytes"
	metricMemoryTotal      = "/memory/classes/total:bytes"
	metricGoroutines       = "/sched/goroutines:goroutines"
	metricGCCycles         = "/gc/cycles/total:gc-cycles"
	metricHeapGoal         = "/gc/heap/goal:bytes"
	metricGCCPUSeconds     = "/cpu/classes/gc/total:cpu-seconds"
	metricCPUSeconds       = "/cpu/classes/total:cpu-seconds"
)

// ReadProcess reads the current process memory counters.
func ReadProcess() (Process, error) {
	samples := []metrics.Sample{
		{Name: metricHeapObjectsBytes},
		{Name: metricHeapUnusedBytes},
		{Name: metricHeapFreeBytes},
		{Name: metricHeapReleased},
		{Name: metricHeapObjects},
		{Name: metricHeapStacks},
		{Name: metricMemoryTotal},
		{Name: metricGoroutines},
		{Name: metricGCCycles},
		{Name: metricHeapGoal},
		{Name: metricGCCPUSeconds},
		{Name: metricCPUSeconds},
	}
	metrics.Read(samples)
	for _, sample := range samples[:10] {
		if sample.Value.Kind() != metrics.KindUint64 {
			return Process{}, fmt.Errorf("metric %s kind %v", sample.Name, sample.Value.Kind())
		}
	}
	for _, sample := range samples[10:] {
		if sample.Value.Kind() != metrics.KindFloat64 {
			return Process{}, fmt.Errorf("metric %s kind %v", sample.Name, sample.Value.Kind())
		}
	}
	objects := samples[0].Value.Uint64()
	unused := samples[1].Value.Uint64()
	free := samples[2].Value.Uint64()
	released := samples[3].Value.Uint64()
	var gcFraction float64
	if total := samples[11].Value.Float64(); total > 0 {
		gcFraction = samples[10].Value.Float64() / total
	}
	return Process{
		HeapAlloc:     objects,
		HeapInuse:     objects + unused,
		HeapIdle:      free + released,
		HeapReleased:  released,
		HeapObjects:   samples[4].Value.Uint64(),
		StackInuse:    samples[5].Value.Uint64(),
		Sys:           samples[6].Value.Uint64(),
		Goroutines:    int(samples[7].Value.Uint64()),
		NumGC:         samples[8].Value.Uint64(),
		NextGC:        samples[9].Value.Uint64(),
		GCCPUFraction: gcFraction,
	}, nil
}

// HeapTop is one allocating stack, largest first.
type HeapTop struct {
	Rank         int    `json:"rank"`
	InUseBytes   int64  `json:"inuse_bytes"`
	InUseObjects int64  `json:"inuse_objects"`
	Stack        string `json:"stack"`
}

// TopHeap ranks sampled heap by the stack that allocated it, so retained
// escrow state shows up under the apply path that wrote it. limit is how many
// stacks to keep. depth is how many functions each stack keeps. A non-positive
// depth uses DefaultStackDepth.
//
// inuse_bytes is an estimate. The profile samples about once per 512 KiB, so a
// stack of small objects can be missing.
func TopHeap(limit, depth int) []HeapTop {
	if limit <= 0 {
		return nil
	}
	if depth <= 0 {
		depth = DefaultStackDepth
	}
	n, _ := runtime.MemProfile(nil, false)
	if n == 0 {
		return nil
	}
	records := make([]runtime.MemProfileRecord, n)
	n, ok := runtime.MemProfile(records, false)
	if !ok {
		records = make([]runtime.MemProfileRecord, n)
		n, ok = runtime.MemProfile(records, false)
		if !ok {
			return nil
		}
	}
	records = records[:n]
	sort.Slice(records, func(i, j int) bool {
		return records[i].InUseBytes() > records[j].InUseBytes()
	})
	if len(records) > limit {
		records = records[:limit]
	}
	out := make([]HeapTop, 0, len(records))
	for i, rec := range records {
		if rec.InUseBytes() == 0 {
			break
		}
		out = append(out, HeapTop{
			Rank:         i + 1,
			InUseBytes:   rec.InUseBytes(),
			InUseObjects: rec.InUseObjects(),
			Stack:        stackLine(rec.Stack(), depth),
		})
	}
	return out
}

func stackLine(pcs []uintptr, depth int) string {
	frames := runtime.CallersFrames(pcs)
	var b strings.Builder
	for written := 0; written < depth; written++ {
		frame, more := frames.Next()
		if frame.Function == "" {
			if !more {
				break
			}
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(';')
		}
		b.WriteString(frame.Function)
		if !more {
			break
		}
	}
	return b.String()
}

// Inventory is the process-specific half of a snapshot. LogAttrs are appended
// to the memory snapshot log line. The value must encode as a JSON object;
// Snapshot merges that object into the top level of its own JSON.
type Inventory interface {
	LogAttrs() []any
}

// Snapshot is one sample. JSON is flat: the process fields, then the
// inventory fields, then heap.
type Snapshot struct {
	At      time.Time
	Process Process
	Heap    []HeapTop
	inv     Inventory
}

// Inventory returns the process-specific counts stored with this sample.
func (s Snapshot) Inventory() Inventory { return s.inv }

// MarshalJSON merges the inventory object into the process fields. A scrape
// sees one flat object, the shape /stats/memory already serves.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(struct {
		At           time.Time `json:"sampled_at"`
		HeapAlloc    uint64    `json:"heap_alloc"`
		HeapInuse    uint64    `json:"heap_inuse"`
		HeapIdle     uint64    `json:"heap_idle"`
		HeapReleased uint64    `json:"heap_released"`
		HeapObjects  uint64    `json:"heap_objects"`
		StackInuse   uint64    `json:"stack_inuse"`
		Sys          uint64    `json:"sys"`
		Goroutines   int       `json:"goroutines"`
		NumGC        uint64    `json:"num_gc"`
		Heap         []HeapTop `json:"heap"`
	}{
		At:           s.At,
		HeapAlloc:    s.Process.HeapAlloc,
		HeapInuse:    s.Process.HeapInuse,
		HeapIdle:     s.Process.HeapIdle,
		HeapReleased: s.Process.HeapReleased,
		HeapObjects:  s.Process.HeapObjects,
		StackInuse:   s.Process.StackInuse,
		Sys:          s.Process.Sys,
		Goroutines:   s.Process.Goroutines,
		NumGC:        s.Process.NumGC,
		Heap:         s.Heap,
	})
	if err != nil || s.inv == nil {
		return base, err
	}
	extra, err := json.Marshal(s.inv)
	if err != nil {
		return nil, err
	}
	return mergeJSONObjects(base, extra)
}

func mergeJSONObjects(base, extra []byte) ([]byte, error) {
	var a, b map[string]json.RawMessage
	if err := json.Unmarshal(base, &a); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(extra, &b); err != nil {
		return nil, fmt.Errorf("memory inventory must encode as a JSON object: %w", err)
	}
	if a == nil {
		a = map[string]json.RawMessage{}
	}
	for k, v := range b {
		a[k] = v
	}
	return json.Marshal(a)
}

// Config builds a Logger. Zero Interval, HeapTopN, and StackDepth use the defaults.
type Config struct {
	Interval   time.Duration
	HeapTopN   int
	StackDepth int
	// Inventory supplies the process-specific counts. It must take lengths
	// only. Nil leaves that half of the snapshot empty.
	Inventory func() Inventory
	// OnSample runs after a sample is stored, for example to publish gauges.
	OnSample func(Snapshot)
}

// Logger takes snapshots and serves the latest one.
type Logger struct {
	cfg Config

	mu     sync.Mutex
	latest Snapshot
	wg     sync.WaitGroup
}

// New returns a logger. It does not start sampling.
func New(cfg Config) *Logger {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.HeapTopN <= 0 {
		cfg.HeapTopN = DefaultHeapTopN
	}
	if cfg.StackDepth <= 0 {
		cfg.StackDepth = DefaultStackDepth
	}
	return &Logger{cfg: cfg}
}

// Start takes one sample, then one per interval, until ctx is done.
func (l *Logger) Start(ctx context.Context) {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.Sample()
		ticker := time.NewTicker(l.cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l.Sample()
			}
		}
	}()
}

// Wait blocks until the goroutine started by Start returns.
func (l *Logger) Wait() {
	if l == nil {
		return
	}
	l.wg.Wait()
}

// Sample reads the counters, the heap profile, and the inventory, stores the
// snapshot, and logs it.
func (l *Logger) Sample() {
	if l == nil {
		return
	}
	proc, err := ReadProcess()
	if err != nil {
		slog.Error("memory snapshot", "error", err)
		return
	}
	var inv Inventory
	if l.cfg.Inventory != nil {
		inv = l.cfg.Inventory()
	}
	snap := Snapshot{
		At:      time.Now().UTC(),
		Process: proc,
		Heap:    TopHeap(l.cfg.HeapTopN, l.cfg.StackDepth),
		inv:     inv,
	}
	l.mu.Lock()
	l.latest = snap
	l.mu.Unlock()
	if l.cfg.OnSample != nil {
		l.cfg.OnSample(snap)
	}
	attrs := []any{
		"heap_alloc", proc.HeapAlloc,
		"heap_inuse", proc.HeapInuse,
		"heap_idle", proc.HeapIdle,
		"heap_released", proc.HeapReleased,
		"heap_objects", proc.HeapObjects,
		"stack_inuse", proc.StackInuse,
		"sys", proc.Sys,
		"goroutines", proc.Goroutines,
		"num_gc", proc.NumGC,
	}
	if inv != nil {
		attrs = append(attrs, inv.LogAttrs()...)
	}
	if stats, ok := TakeChallengeStats(); ok {
		attrs = append(attrs,
			"challenge_requests", stats.Requests,
			"challenge_bytes_min", stats.BytesMin,
			"challenge_bytes_max", stats.BytesMax,
			"challenge_bytes_avg", stats.BytesAvg,
		)
	}
	slog.Info("memory snapshot", attrs...)
	for _, line := range snap.Heap {
		slog.Info("memory heap",
			"rank", line.Rank,
			"inuse_bytes", line.InUseBytes,
			"inuse_objects", line.InUseObjects,
			"stack", line.Stack,
		)
	}
}

// Latest returns the stored snapshot. It does not take a new one.
func (l *Logger) Latest() Snapshot {
	if l == nil {
		return Snapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.latest
}

// ServeHTTP serves the latest snapshot. It does not take a new one: a scrape
// must not stop the world.
func (l *Logger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := json.Marshal(l.Latest())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteByte('\n')
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buf.Bytes())
}
