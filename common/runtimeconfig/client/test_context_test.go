package client

import (
	"context"
	"testing"
	"time"
)

// testContext returns a context cancelled on test cleanup so background
// grpcRunner / chainRunner loops exit before goleak runs.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		waitForRunnersExit()
	})
	return ctx
}

func waitForRunnersExit() {
	// Poll/backoff loops observe ctx.Done() asynchronously.
	time.Sleep(100 * time.Millisecond)
}
