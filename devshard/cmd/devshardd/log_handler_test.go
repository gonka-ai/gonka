package main

import (
	"bytes"
	"log/slog"
	"testing"

	commonobs "common/observability"

	"github.com/stretchr/testify/require"
)

func restoreHostLogger(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	prevFmt := commonobs.LogFormat()
	t.Cleanup(func() {
		commonobs.InstallLogger(prevFmt)
		slog.SetDefault(prev)
	})
}

func TestHostLogger_TextPrefixAndDebugFromEnv(t *testing.T) {
	restoreHostLogger(t)
	var buf bytes.Buffer
	opts := hostLoggerOptions("text", "0.2.13", "debug")
	opts.Out = &buf
	commonobs.InstallLoggerWithOptions(opts)

	slog.Debug("hello")
	slog.Info("world")

	out := buf.String()
	require.Contains(t, out, "[0.2.13] hello")
	require.Contains(t, out, "[0.2.13] world")
}

func TestHostLogger_DefaultLevelDropsDebug(t *testing.T) {
	restoreHostLogger(t)
	var buf bytes.Buffer
	opts := hostLoggerOptions("text", "0.2.13", "")
	opts.Out = &buf
	commonobs.InstallLoggerWithOptions(opts)

	slog.Debug("hidden")
	slog.Info("visible")

	out := buf.String()
	require.NotContains(t, out, "hidden")
	require.Contains(t, out, "[0.2.13] visible")
}

func TestHostLogger_JSONCarriesBinaryVersionWithoutPrefix(t *testing.T) {
	restoreHostLogger(t)
	var buf bytes.Buffer
	opts := hostLoggerOptions("json", "0.2.13", "info")
	opts.Out = &buf
	commonobs.InstallLoggerWithOptions(opts)

	slog.Info("boot")

	out := buf.String()
	require.Contains(t, out, `"binary_version":"0.2.13"`)
	require.Contains(t, out, `"msg":"boot"`)
	require.NotContains(t, out, "[0.2.13]")
}
