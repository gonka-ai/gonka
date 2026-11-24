package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const listenerTestShortRequest = 200 * time.Millisecond

type fakeListener struct {
	requests       sync.WaitGroup
	requestContext context.Context
	cancelRequests context.CancelFunc
	ended          atomic.Int32
}

func newFakeListener() *fakeListener {
	requestContext, cancelRequests := context.WithCancel(context.Background())
	return &fakeListener{requestContext: requestContext, cancelRequests: cancelRequests}
}

func (l *fakeListener) serve(handler func(ctx context.Context)) {
	l.requests.Go(func() { handler(l.requestContext) })
}

func (l *fakeListener) endRequests() {
	l.ended.Add(1)
	l.cancelRequests()
}

func (l *fakeListener) Shutdown(ctx context.Context) error {
	idle := make(chan struct{})
	go func() {
		l.requests.Wait()
		close(idle)
	}()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Test flow:
//  1. Serve, on fake time, a request that holds its stream open until its request context ends, noting when that happens.
//  2. Run step 1 of the shutdown inside the full grace period.
//  3. Assert the step returns nil, the request context was cancelled exactly when the graceful share passed, and the requests were ended exactly once.
func TestStopListenerEndsAStreamThatWouldNeverEndAfterTheShare(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := newFakeListener()
		var cancelledAt time.Time
		listener.serve(func(ctx context.Context) {
			<-ctx.Done()
			cancelledAt = time.Now()
		})
		grace, cancelGrace := context.WithTimeout(t.Context(), shutdownGracePeriod)
		defer cancelGrace()
		begun := time.Now()

		err := stopListener(listener, listener.endRequests)(grace)
		if err != nil {
			t.Fatalf("stopListener() = %v, want nil once the cancelled stream returned", err)
		}
		if waited := cancelledAt.Sub(begun); waited != httpGracefulShare {
			t.Fatalf("the stream was cancelled %v into the shutdown, want exactly the graceful share of %v", waited, httpGracefulShare)
		}
		if ended := listener.ended.Load(); ended != 1 {
			t.Fatalf("requests were ended %d times, want once", ended)
		}
	})
}

// Test flow:
//  1. Serve, on fake time, a request that works for less than the graceful share and notes whether its request context ended.
//  2. Run step 1 of the shutdown inside the full grace period.
//  3. Assert the step returns nil when the request finished, its context was never cancelled, and the requests were never ended.
func TestStopListenerLetsAShortRequestFinishUncancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := newFakeListener()
		var contextErr error
		listener.serve(func(ctx context.Context) {
			select {
			case <-time.After(listenerTestShortRequest):
			case <-ctx.Done():
			}
			contextErr = ctx.Err()
		})
		grace, cancelGrace := context.WithTimeout(t.Context(), shutdownGracePeriod)
		defer cancelGrace()
		begun := time.Now()

		err := stopListener(listener, listener.endRequests)(grace)
		if err != nil {
			t.Fatalf("stopListener() = %v, want nil", err)
		}
		if took := time.Since(begun); took != listenerTestShortRequest {
			t.Fatalf("step 1 took %v, want the %v the request worked", took, listenerTestShortRequest)
		}
		if contextErr != nil {
			t.Fatalf("the short request's context ended with %v, want it never cancelled", contextErr)
		}
		if ended := listener.ended.Load(); ended != 0 {
			t.Fatalf("requests were ended %d times, want never", ended)
		}
	})
}

// Test flow:
//  1. Serve, on fake time, a request that keeps running after its request context is cancelled.
//  2. Run step 1 of the shutdown inside the full grace period.
//  3. Assert the step gives up with context.DeadlineExceeded exactly the cancelled wait after the graceful share, and the requests were ended once.
//  4. Release the request.
func TestStopListenerWaitsAtMostTheCancelledWaitForARequestThatIgnoresItsCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := newFakeListener()
		release := make(chan struct{})
		listener.serve(func(context.Context) { <-release })
		grace, cancelGrace := context.WithTimeout(t.Context(), shutdownGracePeriod)
		defer cancelGrace()
		begun := time.Now()

		err := stopListener(listener, listener.endRequests)(grace)
		took := time.Since(begun)
		close(release)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stopListener() = %v, want context.DeadlineExceeded", err)
		}
		if want := httpGracefulShare + httpCancelledWait; took != want {
			t.Fatalf("step 1 took %v, want exactly %v", took, want)
		}
		if ended := listener.ended.Load(); ended != 1 {
			t.Fatalf("requests were ended %d times, want once", ended)
		}
	})
}
