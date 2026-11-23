package metrics

import "testing"

type fixedStreamLogs struct{ replays, live int64 }

func (f fixedStreamLogs) StreamLogStats() (replays, live int64) { return f.replays, f.live }

// Test flow:
//  1. Register a `StreamLogCollector` backed by a source that reports fixed replay and live counts.
//  2. Assert the replay counter and the live gauge report those values.
func TestTheStreamLogCollectorReportsReplaysAndLiveLogs(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewStreamLogCollector(fixedStreamLogs{replays: 4, live: 2}))

	expectCounter(t, telemetry, "devshard_gateway_stream_log_replays_total", labels{}, 4)
	expectGauge(t, telemetry, "devshard_gateway_stream_logs", labels{}, 2)
}

// Test flow:
//  1. Register a `StreamLogCollector` with no source.
//  2. Assert the replay counter series count is zero.
func TestAStreamLogCollectorWithNoSourceReportsNothing(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewStreamLogCollector(nil))

	expectSeriesCount(t, telemetry, "devshard_gateway_stream_log_replays_total", 0)
}
