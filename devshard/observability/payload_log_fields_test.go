package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClipPayloadFields(t *testing.T) {
	long := strings.Repeat("a", 10<<20)
	kv := []any{"inference_id", long, "validator_address", "gonka1short", "requested_epoch", uint64(7)}

	out := clipPayloadFields(kv)

	require.Len(t, out, len(kv))
	require.Len(t, out[1].(string), payloadLogFieldMaxBytes)
	require.Equal(t, "gonka1short", out[3])
	require.Equal(t, uint64(7), out[5])
	require.Equal(t, long, kv[1], "caller slice must not be modified")
}

func TestClipPayloadFields_ShortFieldsReturnSameSlice(t *testing.T) {
	kv := []any{"inference_id", "1", "validator_address", "gonka1short"}
	out := clipPayloadFields(kv)
	require.Equal(t, kv, out)
	require.Same(t, &kv[0], &out[0])
}

func TestClipPayloadFields_KeepsRuneBoundary(t *testing.T) {
	s := strings.Repeat("a", payloadLogFieldMaxBytes-1) + "é"
	out := clipPayloadFields([]any{"inference_id", s})
	require.Equal(t, strings.Repeat("a", payloadLogFieldMaxBytes-1), out[1])
}

func TestLogPayloadRequest_ClipsFields(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	long := strings.Repeat("a", 10<<20)
	LogPayloadRequest(context.Background(), LevelWarn, "1", MetricStatusError, ReasonNotGroupMember, "payload request auth failed", nil,
		"inference_id", long, "validator_address", long)

	require.NotZero(t, buf.Len())
	require.Less(t, buf.Len(), 4096)
	require.Contains(t, buf.String(), strings.Repeat("a", payloadLogFieldMaxBytes))
}
