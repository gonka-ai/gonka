package main

import (
	"bytes"
	"log/slog"
	"testing"

	commonobs "common/observability"
	"devshard/observability"

	"github.com/stretchr/testify/require"
)

func TestGatewayLoggerEmitsDebugFromEnv(t *testing.T) {
	t.Setenv("DEVSHARD_LOG_LEVEL", "debug")
	prev := slog.Default()
	prevFmt := commonobs.LogFormat()
	t.Cleanup(func() {
		observability.InstallLogger(prevFmt)
		slog.SetDefault(prev)
	})

	var buf bytes.Buffer
	opts := gatewayLoggerOptions("text")
	opts.Out = &buf
	observability.InstallLoggerWithOptions(opts)

	slog.Debug("gateway-debug-line")
	require.Contains(t, buf.String(), "gateway-debug-line")
}

func TestGatewayLoggerDefaultDropsDebug(t *testing.T) {
	t.Setenv("DEVSHARD_LOG_LEVEL", "")
	prev := slog.Default()
	prevFmt := commonobs.LogFormat()
	t.Cleanup(func() {
		observability.InstallLogger(prevFmt)
		slog.SetDefault(prev)
	})

	var buf bytes.Buffer
	opts := gatewayLoggerOptions("text")
	opts.Out = &buf
	observability.InstallLoggerWithOptions(opts)

	slog.Debug("gateway-hidden")
	slog.Info("gateway-visible")
	out := buf.String()
	require.NotContains(t, out, "gateway-hidden")
	require.Contains(t, out, "gateway-visible")
}
