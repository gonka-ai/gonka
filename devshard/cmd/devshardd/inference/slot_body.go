package inference

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	mlnodegen "common/nodemanager/gen"
)

func holdSlot(ctx context.Context, body io.ReadCloser, release func(mlnodegen.ReleaseOutcome)) io.ReadCloser {
	if body == nil || body == http.NoBody {
		release(mlnodegen.ReleaseOutcome_SUCCESS)
		return body
	}
	b := &slotBody{body: body, ctx: ctx, release: release, released: make(chan struct{})}
	b.stopMu.Lock()
	b.stop = context.AfterFunc(ctx, func() { b.finish(true, mlnodegen.ReleaseOutcome_SUCCESS) })
	b.stopMu.Unlock()
	return b
}

type slotBody struct {
	body      io.ReadCloser
	ctx       context.Context
	release   func(mlnodegen.ReleaseOutcome)
	released  chan struct{}
	once      sync.Once
	closeOnce sync.Once
	closeErr  error
	stopMu    sync.Mutex
	stop      func() bool
}

func (b *slotBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil {
		outcome := mlnodegen.ReleaseOutcome_SUCCESS
		if !errors.Is(err, io.EOF) && b.ctx.Err() == nil {
			outcome = mlnodegen.ReleaseOutcome_TRANSPORT_ERROR
		}
		b.finish(!errors.Is(err, io.EOF), outcome)
	}
	return n, err
}

func (b *slotBody) Close() error {
	b.finish(true, mlnodegen.ReleaseOutcome_SUCCESS)
	err := b.closeBody()
	<-b.released
	return err
}

func (b *slotBody) closeBody() error {
	b.closeOnce.Do(func() { b.closeErr = b.body.Close() })
	return b.closeErr
}

func (b *slotBody) finish(closeBody bool, outcome mlnodegen.ReleaseOutcome) {
	b.once.Do(func() {
		b.stopMu.Lock()
		if b.stop != nil {
			b.stop()
			b.stop = nil
		}
		b.stopMu.Unlock()
		if closeBody {
			b.closeBody()
		}
		go func() {
			defer close(b.released)
			b.release(outcome)
		}()
	})
}
