package runtimeconfig

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// Adaptive tests cancel the supervisor on cleanup; runner goroutines may
		// still be exiting their last poll/backoff when goleak runs.
		goleak.IgnoreAnyFunction("devshard/runtimeconfig.(*grpcRunner).run"),
		goleak.IgnoreAnyFunction("devshard/runtimeconfig.(*chainRunner).run"),
	)
}
