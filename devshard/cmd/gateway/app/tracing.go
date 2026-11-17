package app

import (
	"context"
	"time"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/logging"
	tracing "devshard/observability"
)

const tracingFlushTimeout = 10 * time.Second

func startTracing(ctx context.Context) func() {
	shutdown, err := tracing.Init(ctx, tracing.Config{ServiceName: tracing.GatewayServiceName, ServiceVersion: Version})
	if err != nil {
		logging.Warn("tracing degraded", logkey.Error, err)
	}
	return func() {
		flushing, cancel := context.WithTimeout(context.WithoutCancel(ctx), tracingFlushTimeout)
		defer cancel()
		if err := shutdown(flushing); err != nil {
			logging.Warn("tracing flush failed", logkey.Error, err)
		}
	}
}
