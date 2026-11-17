package spool

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// File is one scratch file. With WriteBuffer > 0, ReadableLen is the flushed
// length and ReadAt/Reader flush first. With WriteBuffer == 0, ReadableLen ==
// Len and concurrent ReadAt is safe without flushing under the write lock.
type File struct {
	mu sync.Mutex

	file     *os.File
	fileBuf  *bufio.Writer
	path     string // non-empty only when KeepNamed
	maxBytes int64
	n        int64 // bytes accepted
	readable int64 // bytes readers may see
	closed   bool
	slots    *Slots
	bytesOut *atomic.Uint64
	bufSize  int
}

func newFile(f *os.File, path string, maxBytes int64, writeBuf int, slots *Slots, bytesOut *atomic.Uint64) *File {
	sf := &File{
		file:     f,
		path:     path,
		maxBytes: maxBytes,
		slots:    slots,
		bytesOut: bytesOut,
		bufSize:  writeBuf,
	}
	if writeBuf > 0 {
		sf.fileBuf = bufio.NewWriterSize(f, writeBuf)
	}
	return sf
}

func (f *File) Write(p []byte) (int, error) {
	if f == nil {
		return 0, ErrClosed
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if f.maxBytes > 0 && f.n+int64(len(p)) > f.maxBytes {
		return 0, fmt.Errorf("%w: %d+%d > %d", ErrFileTooLarge, f.n, len(p), f.maxBytes)
	}
	var (
		n   int
		err error
	)
	if f.fileBuf != nil {
		n, err = f.fileBuf.Write(p)
	} else {
		n, err = f.file.Write(p)
		if n > 0 {
			f.readable += int64(n)
		}
	}
	f.n += int64(n)
	if f.bytesOut != nil && n > 0 {
		f.bytesOut.Add(uint64(n))
	}
	if err != nil {
		return n, err
	}
	return len(p), nil
}

func (f *File) Flush() error {
	if f == nil {
		return ErrClosed
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushLocked()
}

func (f *File) flushLocked() error {
	if f.closed {
		return ErrClosed
	}
	if f.fileBuf == nil {
		return nil
	}
	if err := f.fileBuf.Flush(); err != nil {
		return err
	}
	f.readable = f.n
	return nil
}

func (f *File) Len() int64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *File) ReadableLen() int64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fileBuf == nil {
		return f.n
	}
	return f.readable
}

func (f *File) ReadAt(p []byte, off int64) (int, error) {
	if f == nil {
		return 0, ErrClosed
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, ErrClosed
	}
	if f.fileBuf != nil {
		if err := f.flushLocked(); err != nil {
			f.mu.Unlock()
			return 0, err
		}
	}
	size := f.readable
	if f.fileBuf == nil {
		size = f.n
	}
	file := f.file
	f.mu.Unlock()

	if off >= size {
		return 0, nil
	}
	if int64(len(p)) > size-off {
		p = p[:size-off]
	}
	n, err := file.ReadAt(p, off)
	if err != nil && n > 0 {
		// Short read against a growing file is not an error for the caller.
		return n, nil
	}
	if err != nil {
		return n, err
	}
	return n, nil
}

func (f *File) Reader() (io.Reader, error) {
	if f == nil {
		return nil, ErrClosed
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, ErrClosed
	}
	if err := f.flushLocked(); err != nil {
		return nil, err
	}
	if _, err := f.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return f.file, nil
}

// CorruptForTest closes the underlying fd so later reads fail, without
// releasing the MaxFiles slot or clearing spilled state. Tests only.
func (f *File) CorruptForTest() error {
	if f == nil {
		return ErrClosed
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return ErrClosed
	}
	_ = f.flushLocked()
	err := f.file.Close()
	f.file = nil
	f.fileBuf = nil
	return err
}

func (f *File) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	f.fileBuf = nil
	var err error
	if f.file != nil {
		err = f.file.Close()
		f.file = nil
	}
	if f.path != "" {
		if rmErr := os.Remove(f.path); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
			err = rmErr
		}
		f.path = ""
	}
	if f.slots != nil {
		f.slots.Release()
		f.slots = nil
	}
	return err
}
