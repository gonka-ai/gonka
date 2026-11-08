package engine_test

import (
	"testing"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/scheduler"
)

// Test flow:
//  1. For each table case, drive one of the exported `engine.Trace*` helpers (a committed nonce, an unfilled escalation, a crowned attempt, a finished attempt with every delivery field, a finished attempt with no outcome, a host that diverged twice, a stranded nonce, a held winner's finish landing) against a real `journal.Journal` with log capture installed.
//  2. Flush the journal.
//  3. Assert every expected log line was recorded with its exact level, message and typed fields.
func TestEveryRaceTraceLineKeepsItsLevelMessageAndTypedFields(t *testing.T) {
	testCases := []struct {
		name  string
		drive func(t *testing.T, events *journal.Journal)
		want  []logcapture.Entry
	}{
		{
			name:  "a committed nonce",
			drive: func(t *testing.T, events *journal.Journal) { engine.TraceNonceCommitted(t, events) },
			want: []logcapture.Entry{{Level: "info", Msg: "nonce committed", Fields: []any{
				"request", "request-1", "escrow", "escrow-1", "nonce", uint64(12),
				"host", "host-2", "slot", 2, "role", "primary", "reason", "primary",
			}}},
		},
		{
			name:  "an escalation nobody filled",
			drive: func(t *testing.T, events *journal.Journal) { engine.TraceEscalationUnfilled(t, events) },
			want: []logcapture.Entry{{Level: "info", Msg: "escalation unfilled", Fields: []any{
				"request", "request-1", "escrow", "escrow-1", "reason", "receipt_timeout",
				"attempts", 0, "error", scheduler.ErrNoAvailableHost,
			}}},
		},
		{
			name:  "a crowned attempt",
			drive: func(t *testing.T, events *journal.Journal) { engine.TraceAttemptCrowned(t, events) },
			want: []logcapture.Entry{{Level: "info", Msg: "attempt crowned", Fields: []any{
				"request", "request-1", "escrow", "escrow-1", "nonce", uint64(13),
				"host", "host-3", "reason", "first_claim",
			}}},
		},
		{
			name:  "a finished attempt with every delivery field",
			drive: func(t *testing.T, events *journal.Journal) { engine.TraceAttemptFinished(t, events) },
			want: []logcapture.Entry{{Level: "info", Msg: "attempt finished", Fields: []any{
				"request", "request-1", "escrow", "escrow-1", "nonce", uint64(77),
				"host", "host-3", "terminal", "lost",
				"nonce_finished", false, "state_divergent", false, "local_fault", false, "gateway_pressure", false,
				"content_chunks", int64(412), "stream_chunks", int64(415),
				"output_bytes", int64(61_440), "usage_tokens", int64(512),
				"max_gap_ms", int64(1200), "max_gap_at_chunk", int64(310), "mean_gap_ms", int64(21),
				"upstream_status", 502, "upstream_body", "upstream is down",
				"receipt_ms", int64(200), "first_token_ms", int64(900),
				"first_content_ms", int64(950), "attempt_ms", int64(9000),
			}}},
		},
		{
			name:  "a finished attempt that reported nothing",
			drive: func(t *testing.T, events *journal.Journal) { engine.TraceAttemptFinishedWithNoOutcome(t, events) },
			want: []logcapture.Entry{{Level: "info", Msg: "attempt finished with no outcome", Fields: []any{
				"request", "request-1", "escrow", "escrow-1", "nonce", uint64(78),
				"host", "host-4", "nonce_finished", false,
			}}},
		},
		{
			name:  "a host that diverged twice",
			drive: func(t *testing.T, events *journal.Journal) { engine.TraceHostDiverged(t, events) },
			want: []logcapture.Entry{
				{Level: "warn", Msg: "host rewound for state divergence", Fields: []any{
					"request", "request-1", "escrow", "escrow-1", "nonce", uint64(79),
					"host", "host-5", "rewound", true,
				}},
				{Level: "warn", Msg: "host blocked for state divergence", Fields: []any{
					"request", "request-1", "escrow", "escrow-1", "nonce", uint64(79), "host", "host-5",
				}},
			},
		},
		{
			name:  "a stranded nonce",
			drive: func(t *testing.T, events *journal.Journal) { engine.TraceNonceStranded(t, events) },
			want: []logcapture.Entry{{Level: "warn", Msg: "nonce stranded", Fields: []any{
				"request", "request-1", "escrow", "escrow-1", "nonce", uint64(91),
				"host", "host-6", "role", "speculative",
			}}},
		},
		{
			name:  "a held winner's finish landing",
			drive: func(t *testing.T, events *journal.Journal) { engine.TraceWinnerFinishLanded(t, events) },
			want: []logcapture.Entry{{Level: "info", Msg: "served winner finish landed", Fields: []any{
				"request", "request-1", "escrow", "escrow-1", "nonce", uint64(10),
				"host", "host-7", "finish_lag_ms", int64(1500),
			}}},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			logged := logcapture.Install(t)
			events := journal.New(journal.Settings{})
			t.Cleanup(func() { _ = events.Close() })

			testCase.drive(t, events)
			events.Flush()

			for _, line := range testCase.want {
				logged.RequireLine(t, line)
			}
		})
	}
}
