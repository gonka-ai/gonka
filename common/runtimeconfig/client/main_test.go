package client

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// Adaptive tests cancel the supervisor on cleanup; runner goroutines may
		// still be exiting their last poll/backoff when goleak runs.
		goleak.IgnoreAnyFunction("common/runtimeconfig/client.(*grpcRunner).run"),
		goleak.IgnoreAnyFunction("common/runtimeconfig/client.(*chainRunner).run"),
		goleak.IgnoreAnyFunction("github.com/desertbit/timer.timerRoutine"),
	)
}
