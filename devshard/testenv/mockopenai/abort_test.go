package mockopenai_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/testenv/mockopenai"
	"github.com/stretchr/testify/require"
)

func TestAbortActiveInterruptsWorkAndAllowsRecovery(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	patch := func(body string) {
		resp, err := client.Post(srv.URL+"/testenv/fault", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
	}
	stats := func() mockopenai.Stats {
		resp, err := client.Get(srv.URL + "/testenv/stats")
		require.NoError(t, err)
		defer resp.Body.Close()
		var s mockopenai.Stats
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&s))
		return s
	}
	patch(`{"latency_ms":60000}`)
	result := make(chan int, 1)
	go func() {
		resp, err := client.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"test-model","stream":false,"messages":[{"role":"user","content":"hello"}]}`))
		if err != nil {
			result <- 0
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		result <- resp.StatusCode
	}()
	require.Eventually(t, func() bool { return stats().ActiveRequests == 1 }, time.Second, 10*time.Millisecond)
	patch(`{"abort_active":true}`)
	select {
	case status := <-result:
		require.NotEqual(t, http.StatusOK, status)
	case <-time.After(time.Second):
		t.Fatal("active inference was not interrupted")
	}
	require.Eventually(t, func() bool { return stats().ActiveRequests == 0 }, time.Second, 10*time.Millisecond)
	require.Equal(t, uint64(1), stats().AbortedRequests)
	patch(`{"abort_active":false,"latency_ms":0}`)
	resp, err := client.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"test-model","stream":false,"messages":[{"role":"user","content":"recovered"}]}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
