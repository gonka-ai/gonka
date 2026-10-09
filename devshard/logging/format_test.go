package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

// A collector reads a JSON line's fields as labels; the text form carries log's own date prefix,
// which no logfmt parser gets past.
func TestStage_EmitsOneJSONObjectPerLine(t *testing.T) {
	var written bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&written, nil)))
	structuredStages.Store(true)
	t.Cleanup(func() { structuredStages.Store(false) })

	ctx, requestID := WithRequestID(context.Background())
	Stage(ctx, "send_completed", "host", "host-1", "output_chunks", 42, "ok", true)

	var line map[string]any
	require.NoError(t, json.Unmarshal(written.Bytes(), &line))
	require.Equal(t, requestID, line["request"])
	require.Equal(t, "send_completed", line["stage"])
	require.Equal(t, "send_completed", line["msg"])
	require.Equal(t, "host-1", line["host"])
	require.Equal(t, float64(42), line["output_chunks"])
	require.Equal(t, true, line["ok"])
}

func TestStage_KeepsAValueWithoutItsKey(t *testing.T) {
	var written bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&written, nil)))
	structuredStages.Store(true)
	t.Cleanup(func() { structuredStages.Store(false) })

	Stage(context.Background(), "odd", "host")

	var line map[string]any
	require.NoError(t, json.Unmarshal(written.Bytes(), &line))
	require.Equal(t, "<missing>", line["host"])
}
