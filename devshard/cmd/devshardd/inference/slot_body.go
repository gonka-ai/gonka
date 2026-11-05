package inference

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	mlnodegen "common/nodemanager/gen"
)

// holdSlot keeps an ML node slot for as long as body is live. httpClient.Do
// returns at the headers, while a streamed answer is still being generated,
// so the slot is released when the body ends instead:
//
//   - at EOF, with SUCCESS, so payload storage after the read does not hold it;
//   - on a read error, with TRANSPORT_ERROR while ctx is live (the node's
//     failure) and SUCCESS once ctx is done (the request's own cut);
//   - when ctx is done, with SUCCESS, even if no one reads or closes the body,
//     as when the proxy is blocked writing to the gateway;
//   - on Close, with SUCCESS, for a body closed before it ended.
//
// The first of these wins; release runs once. On the execution path ctx is the
// detached execution context, which a gateway disconnect alone does not cancel
// (Host.ExecutionContext). An abort closes the upstream body before releasing,
// so the node has stopped before the slot is free again. An empty body
// releases at once.
func holdSlot(ctx context.Context, body io.ReadCloser, release func(mlnodegen.ReleaseOutcome)) io.ReadCloser {
	if body == nil || body == http.NoBody {
		release(mlnodegen.ReleaseOutcome_SUCCESS)
		return body
	}
	b := &slotBody{body: body, ctx: ctx, release: release, released: make(chan struct{})}
	// stopMu is held while the AfterFunc is registered: on a ctx already done
	// it fires at once, and finish must see stop set before it calls it.
	b.stopMu.Lock()
	b.stop = context.AfterFunc(ctx, func() { b.finish(true, mlnodegen.ReleaseOutcome_SUCCESS) })
	b.stopMu.Unlock()
	return b
}

type slotBody struct {
	body    io.ReadCloser
	ctx     context.Context
	release func(mlnodegen.ReleaseOutcome)
	// released is closed once release has returned.
	released chan struct{}
	// once guards finish, so the slot is released once.
	once sync.Once
	// closeOnce guards the upstream Close; closeErr is its result.
	closeOnce sync.Once
	closeErr  error
	// stop unregisters the ctx AfterFunc; stopMu guards it.
	stopMu sync.Mutex
	stop   func() bool
}

// Read ends the slot on the first error: EOF keeps the upstream body for the
// caller's Close; any other error closes it.
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

// Close ends the slot if nothing ended it yet, closes the upstream body once,
// and waits for the release, which releaseNode bounds by releaseTimeout.
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

// finish runs once, from whichever of Read, Close or the ctx AfterFunc comes
// first. It closes the upstream body first when closeBody is set, then
// releases on its own goroutine, so a slow Release RPC does not hold up the
// body's reader or the payload storage after it.
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
