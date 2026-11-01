package observability

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStageLevel_UsesTheGivenLevel(t *testing.T) {
	restoreInstalledLogger(t)
	var buf bytes.Buffer
	InstallLoggerWithOptions(LoggerOptions{Format: "json", Level: slog.LevelInfo, Out: &buf})

	Stage(context.Background(), "mlnode_acquire", "outcome", "acquired")
	StageLevel(context.Background(), slog.LevelError, "mlnode_acquire", "outcome", "no_nodes_available", "subsystem", "Nodes")

	out := buf.String()
	require.Contains(t, out, `"level":"INFO"`)
	require.Contains(t, out, `"outcome":"acquired"`)
	require.Contains(t, out, `"level":"ERROR"`)
	require.Contains(t, out, `"outcome":"no_nodes_available"`)
	require.Contains(t, out, `"subsystem":"Nodes"`)
	require.Contains(t, out, `"stage":"mlnode_acquire"`)
}

// Text format still attaches a structured stage attr so a JSON handler (or
// Loki `| json | stage=`) can join even if IsJSONLogFormat drifted false.
func TestStageLevel_TextModeStillEmitsStageAttr(t *testing.T) {
	restoreInstalledLogger(t)
	var buf bytes.Buffer
	InstallLoggerWithOptions(LoggerOptions{Format: "text", Level: slog.LevelInfo, Out: &buf})
	// Force the JSON handler while leaving the format flag on text — the
	// drift citests hit when compose ships json lines without stage attrs.
	slog.SetDefault(slog.New(NewTraceHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))))

	Stage(context.Background(), "mlnode_acquire", "outcome", "acquired", "node_id", "n1")

	out := buf.String()
	require.Contains(t, out, `"stage":"mlnode_acquire"`)
	require.Contains(t, out, `"outcome":"acquired"`)
	require.Contains(t, out, `"node_id":"n1"`)
}
