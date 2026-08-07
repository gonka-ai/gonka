package observability

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Stage emits a log line via slog so TraceHandler can stamp trace_id/span_id
// (and registered context fields such as request_id) from ctx.
//
// JSON mode uses structured attrs (stage + kv). Text mode keeps the legacy
// `request=… stage=… key=val` message shape so existing greps and
// WaitLokiSubstring assertions keep matching without duplicating those keys
// as attrs (TextHandler would reprint them).
func Stage(ctx context.Context, stage string, kv ...any) {
	if IsJSONLogFormat() {
		args := make([]any, 0, 4+len(kv))
		if id, ok := RequestID(ctx); ok {
			args = append(args, "request", id)
		}
		args = append(args, "stage", stage)
		for i := 0; i < len(kv); i += 2 {
			key := fmt.Sprintf("field_%d", i)
			if s, ok := kv[i].(string); ok && s != "" {
				key = s
			}
			var value any = "<missing>"
			if i+1 < len(kv) {
				value = kv[i+1]
			}
			args = append(args, key, value)
		}
		slog.Log(ctx, slog.LevelInfo, stage, args...)
		return
	}
	slog.Log(ctx, slog.LevelInfo, formatLegacyStage(ctx, stage, kv))
}

func formatLegacyStage(ctx context.Context, stage string, kv []any) string {
	fields := make([]string, 0, 2+len(kv)/2)
	if id, ok := RequestID(ctx); ok {
		fields = append(fields, "request="+id)
	}
	fields = append(fields, "stage="+stage)
	for i := 0; i < len(kv); i += 2 {
		key := fmt.Sprintf("field_%d", i)
		if s, ok := kv[i].(string); ok && s != "" {
			key = s
		}
		value := "<missing>"
		if i+1 < len(kv) {
			value = fmt.Sprint(kv[i+1])
		}
		fields = append(fields, key+"="+sanitizeStageValue(value))
	}
	return strings.Join(fields, " ")
}

func sanitizeStageValue(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, " \t\n\r\"") {
		return fmt.Sprintf("%q", v)
	}
	return v
}
