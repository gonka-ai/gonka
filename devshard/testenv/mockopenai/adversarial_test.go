package mockopenai_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Start a server and patch empty_stream on through POST /testenv/fault.
// 2. Send a streamed chat request and collect every SSE data line.
// 3. Require a role delta, one finish_reason stop chunk, [DONE] and no content.
// 4. Require the stats count the request as failed, not successful.
func TestChatCompletions_EmptyStreamEmitsRoleStopAndDone(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/testenv/fault", "application/json", strings.NewReader(`{"empty_stream":true}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	resp, err = http.Post(srv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"test-model","stream":true,"logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"say something"}]}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")
	var data []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			data = append(data, line)
		}
	}
	require.NoError(t, sc.Err())
	_ = resp.Body.Close()

	require.Len(t, data, 3)
	require.Equal(t, "[DONE]", data[2])
	var roles, stops int
	for _, raw := range data[:2] {
		var chunk struct {
			Choices []struct {
				Delta        map[string]any `json:"delta"`
				FinishReason *string        `json:"finish_reason"`
			} `json:"choices"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
		require.Len(t, chunk.Choices, 1)
		choice := chunk.Choices[0]
		require.Empty(t, choice.Delta["content"])
		if choice.Delta["role"] == "assistant" {
			roles++
		}
		if choice.FinishReason != nil && *choice.FinishReason == "stop" {
			stops++
		}
	}
	require.Equal(t, 1, roles)
	require.Equal(t, 1, stops)

	resp, err = http.Get(srv.URL + "/testenv/stats")
	require.NoError(t, err)
	var stats mockopenai.Stats
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&stats))
	_ = resp.Body.Close()
	require.Equal(t, uint64(1), stats.FailedResponses)
	require.Zero(t, stats.SuccessfulResponses)
}

func jitterSamples(f mockopenai.FaultConfig, body []byte, chunks int) []time.Duration {
	j := f.NewStreamJitter(body)
	out := []time.Duration{j.FirstToken()}
	for i := 0; i < chunks; i++ {
		out = append(out, j.Chunk())
	}
	return out
}

// Test flow:
// 1. Draw TTFT and chunk delays twice with the same seed and body; require equal sequences.
// 2. Draw with another seed; require a different sequence.
// 3. Draw TTFT over many bodies; require the median and p99 near the configured values.
// 4. Draw with no p99 configured; require the fixed delays back.
func TestStreamJitter_ReproducibleUnderSeed(t *testing.T) {
	f := mockopenai.FaultConfig{
		StreamFirstTokenDelay: 300 * time.Millisecond,
		StreamFirstTokenP99:   1500 * time.Millisecond,
		StreamChunkDelay:      25 * time.Millisecond,
		StreamChunkP99:        80 * time.Millisecond,
		JitterSeed:            7,
	}
	body := []byte(`{"messages":[{"role":"user","content":"req-000001"}]}`)
	first := jitterSamples(f, body, 32)
	require.Equal(t, first, jitterSamples(f, body, 32))
	other := f
	other.JitterSeed = 8
	require.NotEqual(t, first, jitterSamples(other, body, 32))

	ttfts := make([]time.Duration, 0, 4000)
	for i := 0; i < 4000; i++ {
		ttfts = append(ttfts, f.NewStreamJitter([]byte(fmt.Sprintf("req-%06d", i))).FirstToken())
	}
	sort.Slice(ttfts, func(a, b int) bool { return ttfts[a] < ttfts[b] })
	require.InDelta(t, float64(300*time.Millisecond), float64(ttfts[len(ttfts)/2]), float64(30*time.Millisecond))
	require.InDelta(t, float64(1500*time.Millisecond), float64(ttfts[len(ttfts)*99/100]), float64(400*time.Millisecond))

	fixed := mockopenai.FaultConfig{StreamFirstTokenDelay: 250 * time.Millisecond, StreamChunkDelay: 20 * time.Millisecond, JitterSeed: 7}
	samples := jitterSamples(fixed, body, 3)
	require.Equal(t, []time.Duration{250 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond}, samples)
}

// Test flow:
// 1. Start a server with seeded TTFT jitter and no chunk delay.
// 2. Time the first SSE line of the same streamed request twice.
// 3. Require both waits to match the jitter draw for that body within scheduling slack.
func TestStreamJitter_ServerUsesSeededTTFT(t *testing.T) {
	f := mockopenai.FaultConfig{
		StreamFirstTokenDelay: 40 * time.Millisecond,
		StreamFirstTokenP99:   200 * time.Millisecond,
		JitterSeed:            11,
	}
	srv := httptest.NewServer(mockopenai.NewServer(mockopenai.Config{Faults: f}).Handler())
	defer srv.Close()

	body := []byte(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"jitter"}]}`)
	want := f.NewStreamJitter(body).FirstToken()
	for i := 0; i < 2; i++ {
		start := time.Now()
		resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		_, err = bufio.NewReader(resp.Body).ReadString('\n')
		require.NoError(t, err)
		elapsed := time.Since(start)
		_ = resp.Body.Close()
		require.GreaterOrEqual(t, elapsed, want)
		require.Less(t, elapsed, want+150*time.Millisecond)
	}
}
