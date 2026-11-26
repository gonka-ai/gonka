package mockopenai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

func TestChatCompletions_RejectsWhenCapacityIsExhausted(t *testing.T) {
	server := httptest.NewServer(mockopenai.NewServer(mockopenai.Config{
		Faults:  mockopenai.FaultConfig{Latency: 100 * time.Millisecond},
		Workers: 1,
		Queue:   0,
	}).Handler())
	defer server.Close()

	body := []byte(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`)
	type responseResult struct {
		response *http.Response
		err      error
	}
	firstDone := make(chan responseResult, 1)
	go func() {
		response, err := http.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		firstDone <- responseResult{response: response, err: err}
	}()

	time.Sleep(20 * time.Millisecond)
	response, err := http.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)

	first := <-firstDone
	require.NoError(t, first.err)
	defer first.response.Body.Close()
	require.Equal(t, http.StatusOK, first.response.StatusCode)
}

// Test flow:
// 1. Start a mock with one worker slot and a hang fault; its base context ends at cleanup so a held request cannot block Close.
// 2. Send a completion and cancel it on the client side while the mock holds it, as a host does at its deadline.
// 3. Require the mock to count the held request as a timeout, so its slot is free again.
// 4. Lift the hang and require the next completion to be served, not refused as capacity exhausted.
func TestChatCompletions_HangReleasesSlotWhenClientGivesUp(t *testing.T) {
	baseCtx, cancelBase := context.WithCancel(context.Background())
	server := httptest.NewUnstartedServer(mockopenai.NewServer(mockopenai.Config{
		Faults:  mockopenai.FaultConfig{Hang: true},
		Workers: 1,
		Queue:   0,
	}).Handler())
	server.Config.BaseContext = func(net.Listener) context.Context { return baseCtx }
	server.Start()
	defer server.Close()
	defer cancelBase()

	body := []byte(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`)
	stats := func() mockopenai.Stats {
		response, err := http.Get(server.URL + "/testenv/stats")
		require.NoError(t, err)
		defer response.Body.Close()
		var out mockopenai.Stats
		require.NoError(t, json.NewDecoder(response.Body).Decode(&out))
		return out
	}

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		if response, err := http.DefaultClient.Do(request); err == nil {
			response.Body.Close()
		}
	}()
	require.Eventually(t, func() bool { return stats().RequestsReceived == 1 }, time.Second, 10*time.Millisecond)
	cancel()
	<-done
	require.Eventually(t, func() bool { return stats().Timeouts == 1 }, time.Second, 10*time.Millisecond)

	patch, err := http.Post(server.URL+"/testenv/fault", "application/json", bytes.NewReader([]byte(`{"hang":false}`)))
	require.NoError(t, err)
	patch.Body.Close()
	response, err := http.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
}
