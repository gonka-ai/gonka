package spool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Config configures a scratch Dir. Path "" disables spilling (Enabled false).
// AllowUnlimited must be set when MaxFiles or MaxFileBytes is left at zero.
type Config struct {
	Path           string
	Prefix         string
	KeepNamed      bool
	MaxFiles       int64
	MaxFileBytes   int64
	WriteBuffer    int
	AllowUnlimited bool

	// Files, when non-nil, is used as the MaxFiles semaphore instead of an
	// internal one. Callers that already expose a process-wide Slots (gateway
	// tests) share it here so Create and TryAcquire stay one counter.
	Files *Slots
}

// DirStats reports live use of a Dir.
type DirStats struct {
	Enabled      bool
	Path         string
	Prefix       string
	FilesOpen    int64
	FilesMax     int64
	BytesWritten uint64
	SweepCount   int
}

// Dir is one scratch directory. It owns probe, prefix-scoped sweep, and file
// creation. Directory mode is always 0o700; sweep never RemoveAlls the tree.
type Dir struct {
	mu sync.RWMutex

	path         string
	prefix       string
	keepNamed    bool
	maxFileBytes int64
	writeBuffer  int
	enabled      bool
	files        *Slots

	bytesWritten atomic.Uint64
	sweepCount   int
	createFail   atomic.Uint64
	createRefuse atomic.Uint64
}

// Open prepares a scratch directory: MkdirAll 0o700, writability probe, and
// prefix-scoped sweep. An empty Path yields a disabled Dir (not an error).
func Open(cfg Config) (*Dir, error) {
	return open(cfg, true)
}

// OpenAt is like Open but does not create Path. If Path is missing or not
// writable, the returned Dir is still Enabled so Create fails at use time
// (callers that degrade on spill need a non-empty path with a failing Create).
func OpenAt(cfg Config) (*Dir, error) {
	return open(cfg, false)
}

func open(cfg Config, mkdir bool) (*Dir, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	d := &Dir{
		prefix:       cfg.Prefix,
		keepNamed:    cfg.KeepNamed,
		maxFileBytes: cfg.MaxFileBytes,
		writeBuffer:  cfg.WriteBuffer,
		files:        cfg.Files,
	}
	if d.files == nil {
		d.files = NewSlots(cfg.MaxFiles)
	} else if cfg.MaxFiles > 0 {
		d.files.SetMax(cfg.MaxFiles)
	}

	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		return d, nil
	}
	if mkdir {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("spool: mkdir %s: %w", path, err)
		}
		// Re-assert mode even if the directory already existed as 0o755.
		if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // 0700 on a directory, not on a file.
			return nil, fmt.Errorf("spool: chmod %s: %w", path, err)
		}
		if err := probeWritable(path, cfg.Prefix); err != nil {
			return nil, fmt.Errorf("spool: probe %s: %w", path, err)
		}
		d.sweepCount = sweepPrefix(path, cfg.Prefix)
	} else {
		// Best-effort probe; keep enabled so Create surfaces the failure.
		_ = probeWritable(path, cfg.Prefix)
	}
	d.path = path
	d.enabled = true
	return d, nil
}

func validateConfig(cfg Config) error {
	if cfg.MaxFiles == 0 && !cfg.AllowUnlimited && cfg.Files == nil {
		return fmt.Errorf("%w: MaxFiles", ErrUnlimitedRejected)
	}
	if cfg.MaxFileBytes == 0 && !cfg.AllowUnlimited {
		return fmt.Errorf("%w: MaxFileBytes", ErrUnlimitedRejected)
	}
	return nil
}

func probeWritable(dir, prefix string) error {
	pattern := "probe-*"
	if prefix != "" {
		pattern = prefix + "probe-*"
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.WriteString("ok")
	cerr := f.Close()
	_ = os.Remove(name)
	if werr != nil {
		return werr
	}
	return cerr
}

func sweepPrefix(dir, prefix string) int {
	if prefix == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			n++
		}
	}
	return n
}

func (d *Dir) Enabled() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.enabled
}

// Create opens a new scratch file. With KeepNamed false the path is unlinked
// immediately so a crash cannot leave plaintext on disk.
func (d *Dir) Create() (*File, error) {
	if d == nil || !d.Enabled() {
		return nil, ErrDisabled
	}
	d.mu.RLock()
	path := d.path
	prefix := d.prefix
	keepNamed := d.keepNamed
	maxBytes := d.maxFileBytes
	writeBuf := d.writeBuffer
	files := d.files
	d.mu.RUnlock()

	if !files.TryAcquire() {
		d.createRefuse.Add(1)
		return nil, ErrNoCapacity
	}
	pattern := "scratch-*"
	if prefix != "" {
		pattern = prefix + "*.scratch"
	}
	f, err := os.CreateTemp(path, pattern)
	if err != nil {
		files.Release()
		d.createFail.Add(1)
		return nil, err
	}
	name := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		files.Release()
		d.createFail.Add(1)
		return nil, err
	}
	keptPath := ""
	if keepNamed {
		keptPath = name
	} else if err := os.Remove(name); err != nil {
		_ = f.Close()
		files.Release()
		d.createFail.Add(1)
		return nil, err
	}
	return newFile(f, keptPath, maxBytes, writeBuf, files, &d.bytesWritten), nil
}

// CreateIndex opens a sidecar index file with the same naming / unlink policy.
// It does not consume a MaxFiles slot — slots count generations (Create), and
// an index is always paired with a data File.
func (d *Dir) CreateIndex() (*Index, error) {
	if d == nil || !d.Enabled() {
		return nil, ErrDisabled
	}
	d.mu.RLock()
	path := d.path
	prefix := d.prefix
	keepNamed := d.keepNamed
	d.mu.RUnlock()

	pattern := "idx-*"
	if prefix != "" {
		pattern = prefix + "*.idx"
	}
	f, err := os.CreateTemp(path, pattern)
	if err != nil {
		d.createFail.Add(1)
		return nil, err
	}
	name := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		d.createFail.Add(1)
		return nil, err
	}
	keptPath := ""
	if keepNamed {
		keptPath = name
	} else if err := os.Remove(name); err != nil {
		_ = f.Close()
		d.createFail.Add(1)
		return nil, err
	}
	return newIndex(f, keptPath), nil
}

func (d *Dir) Stats() DirStats {
	if d == nil {
		return DirStats{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	max, cur := d.files.Stats()
	return DirStats{
		Enabled:      d.enabled,
		Path:         d.path,
		Prefix:       d.prefix,
		FilesOpen:    cur,
		FilesMax:     max,
		BytesWritten: d.bytesWritten.Load(),
		SweepCount:   d.sweepCount,
	}
}

// FileSlots returns the MaxFiles semaphore (shared with Create).
func (d *Dir) FileSlots() *Slots {
	if d == nil {
		return nil
	}
	return d.files
}

// Reconfigure swaps limits atomically. Path/Prefix/KeepNamed changes reopen
// behaviour for new Create calls; in-flight files keep the snapshot they were
// created with.
func (d *Dir) Reconfigure(cfg Config) error {
	if d == nil {
		return ErrClosed
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	path := strings.TrimSpace(cfg.Path)
	d.prefix = cfg.Prefix
	d.keepNamed = cfg.KeepNamed
	d.maxFileBytes = cfg.MaxFileBytes
	d.writeBuffer = cfg.WriteBuffer
	if cfg.Files != nil {
		d.files = cfg.Files
	}
	if cfg.MaxFiles > 0 || cfg.AllowUnlimited {
		d.files.SetMax(cfg.MaxFiles)
	}
	if path == "" {
		d.enabled = false
		d.path = ""
		return nil
	}
	// Reconfigure never MkdirAlls: path lifetime is owned by Open / the caller.
	// Creating dirs here would turn a deliberately missing degrade-test path
	// into a writable spool.
	d.path = path
	d.enabled = true
	return nil
}

func (d *Dir) Snapshot() Config {
	if d == nil {
		return Config{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	max, _ := d.files.Stats()
	return Config{
		Path:           d.path,
		Prefix:         d.prefix,
		KeepNamed:      d.keepNamed,
		MaxFiles:       max,
		MaxFileBytes:   d.maxFileBytes,
		WriteBuffer:    d.writeBuffer,
		Files:          d.files,
		AllowUnlimited: max == 0 || d.maxFileBytes == 0,
	}
}
