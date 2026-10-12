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
