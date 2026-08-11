package host

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"devshard/spool"
)

// ErrSpoolClosed is returned when a spool read races the release of a finished
// generation. Callers treat it like any other resume failure.
var ErrSpoolClosed = errors.New("live stream spool closed")

// Live-stream spool process knobs. Defaults match today's behaviour: unlimited
// disk and concurrent files (AllowUnlimited). Env overrides are applied in
// PrepareLiveStreamSpoolDir / OpenLiveStreamSpoolDir.
var (
	liveStreamMaxSpoolBytes       int64 // 0 = unlimited
	liveStreamMaxConcurrentSpools int64 // 0 = unlimited
	liveStreamSpoolKeepNamed      bool
)

var (
	liveDirsMu sync.Mutex
	liveDirs   = map[string]*spool.Dir{}
)

// streamSpool is the per-generation scratch log that backs live-stream resume.
// It wraps spool.File + spool.Index: anonymous by default, no fsync, removed on
// close. Losing it costs at most one reconnect.
type streamSpool struct {
	mu     sync.Mutex
	file   *spool.File
	idx    *spool.Index
	closed bool
}

// PrepareLiveStreamSpoolDir opens (and sweeps) a process-wide scratch Dir for
// live-stream resume. Leftover ls-* files are removed; sibling files are left
// alone. The directory is never RemoveAll'd.
func PrepareLiveStreamSpoolDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("spool: empty dir")
	}
	loadLiveStreamSpoolEnv()
	d, err := openLiveSpoolDir(dir, true)
	if err != nil {
		return err
	}
	_ = d
	return nil
}

func loadLiveStreamSpoolEnv() {
	if v := strings.TrimSpace(os.Getenv("DEVSHARDD_LIVESTREAM_MAX_SPOOL_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			liveStreamMaxSpoolBytes = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("DEVSHARDD_LIVESTREAM_MAX_CONCURRENT_SPOOLS")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			liveStreamMaxConcurrentSpools = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("DEVSHARDD_LIVESTREAM_SPOOL_KEEP_NAMED")); v == "1" || strings.EqualFold(v, "true") {
		liveStreamSpoolKeepNamed = true
	}
	if v := strings.TrimSpace(os.Getenv("DEVSHARDD_LIVESTREAM_RING_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			setLiveStreamRingBytes(n)
		}
	}
}

func liveSpoolConfig(path string) spool.Config {
	cfg := spool.Config{
		Path:         path,
		Prefix:       "ls-",
		KeepNamed:    liveStreamSpoolKeepNamed,
		WriteBuffer:  0, // concurrent ReadAt while appending
		MaxFiles:     liveStreamMaxConcurrentSpools,
		MaxFileBytes: liveStreamMaxSpoolBytes,
	}
	if cfg.MaxFiles == 0 || cfg.MaxFileBytes == 0 {
		cfg.AllowUnlimited = true
	}
	return cfg
}

func openLiveSpoolDir(path string, prepare bool) (*spool.Dir, error) {
	liveDirsMu.Lock()
	defer liveDirsMu.Unlock()
	if d, ok := liveDirs[path]; ok && d.Enabled() && !prepare {
		return d, nil
	}
	// Open MkdirAlls, sets 0o700, probes, and sweeps ls-* (never RemoveAll).
	d, err := spool.Open(liveSpoolConfig(path))
	if err != nil {
		return nil, err
	}
	liveDirs[path] = d
	return d, nil
}

// LiveSpoolDirStats returns Stats for a previously opened live spool dir.
func LiveSpoolDirStats(path string) (spool.DirStats, bool) {
	liveDirsMu.Lock()
	defer liveDirsMu.Unlock()
	d, ok := liveDirs[path]
	if !ok || d == nil {
		return spool.DirStats{}, false
	}
	return d.Stats(), true
}

func newStreamSpool(dir, escrowID string, inferenceID uint64) (*streamSpool, error) {
	_ = escrowID
	_ = inferenceID
	d, err := openLiveSpoolDir(dir, false)
	if err != nil {
		return nil, err
	}
	if !d.Enabled() {
		return nil, spool.ErrDisabled
	}
	f, err := d.Create()
	if err != nil {
		return nil, err
	}
	idx, err := d.CreateIndex()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &streamSpool{file: f, idx: idx}, nil
}

func (sp *streamSpool) append(payload []byte, eventStarts []int64) error {
	if len(payload) == 0 {
		return nil
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.closed {
		return ErrSpoolClosed
	}
	if _, err := sp.file.Write(payload); err != nil {
		if errors.Is(err, spool.ErrFileTooLarge) {
			return err
		}
		if errors.Is(err, spool.ErrClosed) {
			return ErrSpoolClosed
		}
		return fmt.Errorf("spool: append log: %w", err)
	}
	if err := sp.idx.Append(eventStarts); err != nil {
		if errors.Is(err, spool.ErrClosed) {
			return ErrSpoolClosed
		}
		return fmt.Errorf("spool: append index: %w", err)
	}
	return nil
}

func (sp *streamSpool) readAt(dst []byte, off int64) (int, error) {
	sp.mu.Lock()
	closed := sp.closed
	file := sp.file
	sp.mu.Unlock()
	if closed || file == nil {
		return 0, ErrSpoolClosed
	}
	n, err := file.ReadAt(dst, off)
	if errors.Is(err, spool.ErrClosed) {
		return n, ErrSpoolClosed
	}
	return n, err
}

func (sp *streamSpool) eventOffset(i int64) (int64, error) {
	sp.mu.Lock()
	closed := sp.closed
	idx := sp.idx
	sp.mu.Unlock()
	if closed || idx == nil {
		return 0, ErrSpoolClosed
	}
	off, err := idx.At(i)
	if errors.Is(err, spool.ErrClosed) {
		return 0, ErrSpoolClosed
	}
	if errors.Is(err, spool.ErrIndexPast) {
		return 0, ErrResumeCursorPast
	}
	return off, err
}

func (sp *streamSpool) close() {
	sp.mu.Lock()
	if sp.closed {
		sp.mu.Unlock()
		return
	}
	sp.closed = true
	file, idx := sp.file, sp.idx
	sp.file, sp.idx = nil, nil
	sp.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
	if idx != nil {
		_ = idx.Close()
	}
}
