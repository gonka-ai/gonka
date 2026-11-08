package mockopenai_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// A caller that hangs up mid-stream got no full answer: the log must not say "ok", or a check that
// reads the log takes a cut execution for one the ML answered in full.
func TestRequestLogMarksAStreamTheCallerHungUpOn(t *testing.T) {
	_, ts := journalServer(t, func(c *mockopenai.Config) { c.Faults.StreamChunkDelay = 50 * time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(streamBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "data: "), "the stream started")
	cancel()
	_ = resp.Body.Close()

	var recs []mockopenai.RequestRecord
	require.Eventually(t, func() bool {
		recs = getLog(t, ts).Records
		return len(recs) == 1
	}, 5*time.Second, 20*time.Millisecond)
	rec := recs[0]
	require.Equal(t, mockopenai.OutcomeClientGone, rec.Outcome)
	require.Equal(t, http.StatusOK, rec.Status, "the status went out before the caller left")
	require.Less(t, rec.TokensOut, 28, "the answer was cut")
}

func TestRequestLogKeepsOkForAStreamReadToTheEnd(t *testing.T) {
	_, ts := journalServer(t, nil)
	streamRequest(t, ts, streamBody)
	recs := getLog(t, ts).Records
	require.Len(t, recs, 1)
	require.Equal(t, "ok", recs[0].Outcome)
}
