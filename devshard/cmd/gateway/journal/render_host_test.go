package journal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/perf"
)

// Test flow:
//  1. Table-driven: each case pairs a host-transition producer call with the exact log entry it must render (level, message, and every key with its type).
//  2. For each case, build a journal with a `logcapture.Recorder`, run the case's `produce` function, and flush.
//  3. Assert the recorder captured exactly the case's expected entry.
func TestHostTransitionsRenderTheLinesTheirProducersNarrate(t *testing.T) {
	testCases := []struct {
		name    string
		produce func(events *Journal)
		want    logcapture.Entry
	}{
		{
			name: "perf withholds a host",
			produce: func(events *Journal) {
				events.HostWithheld(perf.Withholding{
					Participant: hostAlpha, Model: "qwen", Reason: "failure_rate", EjectionCount: 2,
					ConsecutiveFailures: 1, FailureRate: 0.25, FailureVolume: 20, WithheldFor: time.Minute,
				})
			},
			want: logcapture.Entry{Level: "warn", Msg: "host withheld from routing", Fields: []any{
				"host", "aaaaaaaa", "model", "qwen", "reason", "failure_rate", "ejection_count", 2,
				"consecutive_failures", 1, "failure_rate", 0.25, "failure_volume", float64(20), "withheld_for_ms", int64(60000),
			}},
		},
		{
			name:    "perf returns a host",
			produce: func(events *Journal) { events.HostReturned(hostAlpha, "qwen", 1) },
			want: logcapture.Entry{Level: "info", Msg: "host back in routing", Fields: []any{
				"host", "aaaaaaaa", "model", "qwen", "ejection_count", 1,
			}},
		},
		{
			name:    "a host admits a smaller context",
			produce: func(events *Journal) { events.HostContextLimit(hostAlpha, "qwen", 4096, 8192) },
			want: logcapture.Entry{Level: "info", Msg: "host admitted a context length it will not exceed", Fields: []any{
				"host", "aaaaaaaa", "model", "qwen", "context_limit", uint64(4096), "previous_context_limit", uint64(8192),
			}},
		},
		{
			name:    "a host build refuses tool calling",
			produce: func(events *Journal) { events.HostToolsUnsupported(hostAlpha, "qwen") },
			want: logcapture.Entry{Level: "info", Msg: "host build does not implement tool calling", Fields: []any{
				"host", "aaaaaaaa", "model", "qwen",
			}},
		},
		{
			name:    "a host build refuses the protocol version",
			produce: func(events *Journal) { events.HostVersionUnsupported(hostAlpha) },
			want: logcapture.Entry{Level: "info", Msg: "host build cannot serve the escrow's protocol version", Fields: []any{
				"host", "aaaaaaaa",
			}},
		},
		{
			name: "the participant limiter cuts a host off",
			produce: func(events *Journal) {
				events.HostCutOff(hostAlpha, "qwen", "consecutive_transport_faults", 1, 5*time.Second)
			},
			want: logcapture.Entry{Level: "warn", Msg: "host cut off after transport faults", Fields: []any{
				"host", "aaaaaaaa", "model", "qwen", "reason", "consecutive_transport_faults",
				"backoff_count", 1, "cut_off_for_ms", int64(5000),
			}},
		},
		{
			name:    "a half-open probe answers",
			produce: func(events *Journal) { events.HostCutOffLifted(hostAlpha, "qwen", 0) },
			want: logcapture.Entry{Level: "info", Msg: "host back after its cut-off", Fields: []any{
				"host", "aaaaaaaa", "model", "qwen", "backoff_count", 0,
			}},
		},
		{
			name:    "a host loses the crown",
			produce: func(events *Journal) { events.HostDeniedCrown(hostAlpha, "qwen", 3) },
			want: logcapture.Entry{Level: "warn", Msg: "host denied the crown", Fields: []any{
				"host", "aaaaaaaa", "model", "qwen", "strikes", 3,
			}},
		},
		{
			name:    "a host is crowned again",
			produce: func(events *Journal) { events.HostCrownedAgain(hostAlpha, "qwen") },
			want: logcapture.Entry{Level: "info", Msg: "host crowned again", Fields: []any{
				"host", "aaaaaaaa", "model", "qwen",
			}},
		},
		{
			name:    "a nonce is spent on a host its request excluded",
			produce: func(events *Journal) { events.ExcludedHostServed("escrow-1", hostBravo) },
			want: logcapture.Entry{Level: "info", Msg: "nonce spent on a host the request excluded", Fields: []any{
				"escrow", "escrow-1", "host", "bbbbbbbb",
			}},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			lines := &logcapture.Recorder{}
			events := newJournal(t, Settings{Lines: lines})

			testCase.produce(events)
			events.Flush()

			require.Equal(t, []logcapture.Entry{testCase.want}, lines.All())
		})
	}
}
