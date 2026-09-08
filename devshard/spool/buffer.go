package spool

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
)

// SpillPolicy selects behaviour when Create/spill fails.
type SpillPolicy int

const (
	// FailRequest returns the spill error to the caller (logprobs / hard fail).
	FailRequest SpillPolicy = iota
	// DegradeToRAM grows past the RAM threshold up to the disk budget while a
	// Degraded slot is held; without a slot it keeps the RAM ceiling.
	DegradeToRAM
)

// BufferConfig configures a mem-first, spill-on-threshold buffer.
type BufferConfig struct {
	Dir            *Dir
	Budget         *Budget // ramLimit = spill threshold; diskLimit = total ceiling
	OnSpillFailure SpillPolicy
	Degraded       *Slots // required when OnSpillFailure == DegradeToRAM
}

// Buffer accumulates bytes in RAM until Budget.ramLimit, then spills into a
// Dir file. Single consumer after the producer finishes: OpenReader / Bytes.
type Buffer struct {
	mu sync.Mutex

	dir      *Dir
	budget   *Budget
	policy   SpillPolicy
	degraded *Slots

	mem               bytes.Buffer
	file              *File
	n                 int64
	spilled           bool
	spillDisabled     bool
	holdsDegradedSlot bool
	writeErr          error
	lastSpillErr      error
	closed            bool

	ramLimit  int64
	diskLimit int64
}

func NewBuffer(cfg BufferConfig) *Buffer {
	ram, disk := int64(0), int64(0)
	if cfg.Budget != nil {
		ram, disk = cfg.Budget.Limits()
	}
	maxBytes := ram
	if cfg.Dir != nil && cfg.Dir.Enabled() && disk > ram {
		maxBytes = disk
	}
	return &Buffer{
		dir:       cfg.Dir,
		budget:    cfg.Budget,
		policy:    cfg.OnSpillFailure,
		degraded:  cfg.Degraded,
		ramLimit:  ram,
		diskLimit: maxBytes,
	}
}

func (b *Buffer) Write(p []byte) (int, error) {
	if b == nil {
		return 0, errors.New("spool: buffer is nil")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if b.writeErr != nil {
		return 0, b.writeErr
	}
	if b.n+int64(len(p)) > b.diskLimit {
		return 0, b.failLocked(fmt.Errorf("%w: %d byte limit", ErrFileTooLarge, b.diskLimit))
	}
	if !b.spilled && !b.spillDisabled && int64(b.mem.Len())+int64(len(p)) > b.ramLimit {
		if err := b.spillLocked(); err != nil {
			b.lastSpillErr = err
			switch b.policy {
			case DegradeToRAM:
				b.spillDisabled = true
				if b.degraded != nil && b.degraded.TryAcquire() {
					b.holdsDegradedSlot = true
					// Promote ceiling to diskLimit (already set when Dir enabled).
				} else {
					b.diskLimit = b.ramLimit
				}
			default:
				return 0, b.failLocked(err)
			}
		}
	}
	if b.n+int64(len(p)) > b.diskLimit {
		return 0, b.failLocked(fmt.Errorf("%w: %d byte limit", ErrFileTooLarge, b.diskLimit))
	}
	if b.spilled {
		n, err := b.file.Write(p)
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

func (b *Buffer) spillLocked() error {
	if b.dir == nil || !b.dir.Enabled() {
		return ErrDisabled
	}
	f, err := b.dir.Create()
	if err != nil {
		return err
	}
	if b.mem.Len() > 0 {
		if _, err := f.Write(b.mem.Bytes()); err != nil {
			_ = f.Close()
			return err
		}
	}
	b.file = f
	b.mem.Reset()
	b.spilled = true
	return nil
}

func (b *Buffer) failLocked(err error) error {
	if b.writeErr == nil {
		b.writeErr = err
	}
	return err
}

// Bytes returns the full body. Prefer OpenReader for spilled buffers.
func (b *Buffer) Bytes() ([]byte, error) {
	if b == nil {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrClosed
	}
	if b.writeErr != nil {
		return nil, b.writeErr
	}
	if !b.spilled {
		return b.mem.Bytes(), nil
	}
	r, err := b.file.Reader()
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func (b *Buffer) OpenReader() (io.Reader, error) {
	if b == nil {
		return bytes.NewReader(nil), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrClosed
	}
	if b.writeErr != nil {
		return nil, b.writeErr
	}
	if !b.spilled {
		return bytes.NewReader(b.mem.Bytes()), nil
	}
	return b.file.Reader()
}

func (b *Buffer) Len() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

func (b *Buffer) Spilled() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spilled
}

// WriteErr returns the latched write failure, if any.
func (b *Buffer) WriteErr() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writeErr
}

// DiskLimit returns the effective total ceiling (may shrink after refused degrade).
func (b *Buffer) DiskLimit() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.diskLimit
}

// SpillDisabled reports whether spill was abandoned for this buffer.
func (b *Buffer) SpillDisabled() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spillDisabled
}

// HoldsDegradedSlot reports whether a DegradeToRAM slot is held.
func (b *Buffer) HoldsDegradedSlot() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.holdsDegradedSlot
}

// LastSpillErr is set when a spill attempt fails (before degrade / fail policy).
func (b *Buffer) LastSpillErr() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastSpillErr
}

func (b *Buffer) Close() error {
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
		err = b.file.Close()
		b.file = nil
	}
	if b.holdsDegradedSlot && b.degraded != nil {
		b.degraded.Release()
		b.holdsDegradedSlot = false
	}
	b.mem.Reset()
	return err
}
