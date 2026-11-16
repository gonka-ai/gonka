package journal

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/shadow"
)

// Test flow:
//  1. Table-driven: a served streamed probe, a served probe with no streamed byte, and a failed probe.
//  2. For each case, build a journal with a `logcapture.Recorder`, record the result via `ShadowProbed` and flush.
//  3. Assert the exact entry: info for served, warn with the error for failed, and a first-token field only when a byte streamed.
func TestShadowProbeLinesCarryTheOutcomeAndOnlyTheFactsThatExist(t *testing.T) {
	failure := errors.New("connection refused")
	base := shadow.Result{Escrow: "escrow-1", Participant: "gonka1abcdefghijkl", Model: "qwen", Nonce: 7, Total: 1500 * time.Millisecond, OutputTokens: 9}

	served := base
	served.Outcome, served.Streamed, served.FirstToken = shadow.OutcomeServed, true, 400*time.Millisecond
	unstreamed := base
	unstreamed.Outcome = shadow.OutcomeServed
	failed := base
	failed.Outcome, failed.Err, failed.OutputTokens = shadow.OutcomeFailed, failure, 0

	testCases := []struct {
		name   string
		result shadow.Result
		want   logcapture.Entry
	}{
		{"served and streamed", served, logcapture.Entry{Level: "info", Msg: "shadow probe served", Fields: []any{
			"escrow", "escrow-1", "nonce", uint64(7), "host", "efghijkl", "model", "qwen", "outcome", "served",
			"duration_ms", int64(1500), "output_tokens", uint64(9), "first_token_ms", int64(400),
		}}},
		{"served without a streamed byte", unstreamed, logcapture.Entry{Level: "info", Msg: "shadow probe served", Fields: []any{
			"escrow", "escrow-1", "nonce", uint64(7), "host", "efghijkl", "model", "qwen", "outcome", "served",
			"duration_ms", int64(1500), "output_tokens", uint64(9),
		}}},
		{"failed", failed, logcapture.Entry{Level: "warn", Msg: "shadow probe failed", Fields: []any{
			"escrow", "escrow-1", "nonce", uint64(7), "host", "efghijkl", "model", "qwen", "outcome", "failed",
			"duration_ms", int64(1500), "output_tokens", uint64(0), "error", failure,
		}}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			lines := &logcapture.Recorder{}
			events := newJournal(t, Settings{Lines: lines})

			events.ShadowProbed(tc.result)
			events.Flush()

			require.Equal(t, []logcapture.Entry{tc.want}, lines.All())
		})
	}
}
