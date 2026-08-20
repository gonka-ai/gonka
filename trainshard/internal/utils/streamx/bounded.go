package streamx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

// Bounded holds at most a fixed number of bytes on their way to a reader. Writing never waits:
// once it is full the oldest bytes go and the reader is told how many it lost, rather than
// quietly getting a shorter stream
type Bounded struct {
	dst   io.Writer
	limit int

	mu      sync.Mutex
	chunks  [][]byte
	held    int
	dropped int
	closed  bool

	wake chan struct{}
	done chan struct{}
	err  error
}

func NewBounded(ctx context.Context, dst io.Writer, limit int) *Bounded {
	b := &Bounded{
		dst:   dst,
		limit: limit,
		wake:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	go b.drain(ctx)
	return b
}

func (b *Bounded) Write(p []byte) (int, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0, io.ErrClosedPipe
	}

	chunk := bytes.Clone(p)
	b.chunks = append(b.chunks, chunk)
	b.held += len(chunk)
	for b.held > b.limit && len(b.chunks) > 1 {
		oldest := b.chunks[0]
		b.chunks = b.chunks[1:]
		b.held -= len(oldest)
		b.dropped += len(oldest)
	}
	b.mu.Unlock()

	b.signal()
	return len(p), nil
}

func (b *Bounded) Close() error {
	b.mu.Lock()
	first := !b.closed
	b.closed = true
	b.mu.Unlock()

	if first {
		b.signal()
	}
	<-b.done
	return b.err
}

func (b *Bounded) drain(ctx context.Context) {
	defer close(b.done)

	for {
		chunk, gap, open := b.next()
		if gap > 0 {
			if _, err := fmt.Fprintf(b.dst, "\n[%d bytes dropped: the reader fell behind]\n", gap); err != nil {
				b.err = err
				return
			}
		}
		if len(chunk) > 0 {
			if _, err := b.dst.Write(chunk); err != nil {
				b.err = err
				return
			}
			continue
		}
		if gap > 0 {
			continue
		}
		if !open {
			return
		}

		select {
		case <-ctx.Done():
			b.err = ctx.Err()
			return
		case <-b.wake:
		}
	}
}

func (b *Bounded) next() (chunk []byte, gap int, open bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	gap, b.dropped = b.dropped, 0
	if len(b.chunks) > 0 {
		chunk, b.chunks = b.chunks[0], b.chunks[1:]
		b.held -= len(chunk)
	}
	return chunk, gap, !b.closed
}

func (b *Bounded) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}
