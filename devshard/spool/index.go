package spool

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
)

const indexEntryBytes = 8

// Index is a fixed-width int64 sidecar: entry n is the absolute byte offset
// where record n starts. Write log bytes before Append so a torn append can
// only under-report.
type Index struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	events int64
	closed bool
}

func newIndex(f *os.File, path string) *Index {
	return &Index{file: f, path: path}
}

func (i *Index) Append(offsets []int64) error {
	if i == nil {
		return ErrClosed
	}
	if len(offsets) == 0 {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return ErrClosed
	}
	buf := make([]byte, len(offsets)*indexEntryBytes)
	for j, off := range offsets {
		binary.LittleEndian.PutUint64(buf[j*indexEntryBytes:], uint64(off))
	}
	if _, err := i.file.Write(buf); err != nil {
		return fmt.Errorf("spool: append index: %w", err)
	}
	i.events += int64(len(offsets))
	return nil
}

func (i *Index) At(n int64) (int64, error) {
	if i == nil {
		return 0, ErrClosed
	}
	i.mu.Lock()
	closed, events := i.closed, i.events
	file := i.file
	i.mu.Unlock()
	if closed {
		return 0, ErrClosed
	}
	if n < 0 || n >= events {
		return 0, ErrIndexPast
	}
	var buf [indexEntryBytes]byte
	if _, err := file.ReadAt(buf[:], n*indexEntryBytes); err != nil {
		return 0, fmt.Errorf("spool: read index %d: %w", n, err)
	}
	return int64(binary.LittleEndian.Uint64(buf[:])), nil
}

func (i *Index) Len() int64 {
	if i == nil {
		return 0
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.events
}

func (i *Index) Close() error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil
	}
	i.closed = true
	var err error
	if i.file != nil {
		err = i.file.Close()
		i.file = nil
	}
	if i.path != "" {
		if rmErr := os.Remove(i.path); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
			err = rmErr
		}
		i.path = ""
	}
	return err
}
