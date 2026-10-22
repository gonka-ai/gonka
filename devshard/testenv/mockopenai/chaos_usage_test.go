package mockopenai_test

import (
	"net/http"
	"strings"
	"testing"

	"common/completionapi"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. For every streamed chaos answer, run the mock's stream through the host's response processor.
//  2. Read the usage the host derives and the journal record of the same answer.
//  3. Check the journal counts what the host derives: the promise check holds a Finish to the
//     journal, so any gap is a violation nobody committed.
func TestJournalCountMatchesWhatTheHostDerivesForEveryStreamedChaosAnswer(t *testing.T) {
	for _, action := range []string{
		mockopenai.ChaosOK, mockopenai.ChaosDelay, mockopenai.ChaosPrelude, mockopenai.ChaosPartial,
		mockopenai.ChaosSSEError, mockopenai.ChaosEmpty, mockopenai.ChaosOddUsage, mockopenai.ChaosWrongAnswer,
	} {
		t.Run(action, func(t *testing.T) {
			ts := chaosServer(t, action)
			resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(chaosStream))
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			processor := completionapi.NewExecutorResponseProcessor("devshard-e-11", true)
			require.NoError(t, completionapi.ProcessHTTPResponse(resp, processor))
			usage, err := processor.GetUsage()
			require.NoError(t, err)

			rec := lastRecord(t, ts)
			produced := rec.TokensOut
			if rec.UsageCompletion > 0 {
				produced = rec.UsageCompletion
			}
			require.Equal(t, int(usage.CompletionTokens), produced,
				"journal tokens_out=%d usage_completion_tokens=%d outcome=%s", rec.TokensOut, rec.UsageCompletion, rec.Outcome)
		})
	}
}
