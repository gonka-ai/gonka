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
// 1. Start a mock with one worker, one queue slot and a hang fault; its base context ends at cleanup.
// 2. Send a completion that the hang holds on the worker, then a second one that waits in the queue.
// 3. Lift the hang, then cancel the held completion so the queued one gets the worker.
// 4. Require the queued completion to be served: the fault is read once a worker is free, not on arrival.
func TestChatCompletions_QueuedRequestIsServedWhenHangLiftsBeforeItsTurn(t *testing.T) {
	baseCtx, cancelBase := context.WithCancel(context.Background())
	server := httptest.NewUnstartedServer(mockopenai.NewServer(mockopenai.Config{
		Faults:  mockopenai.FaultConfig{Hang: true},
		Workers: 1,
		Queue:   1,
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

	heldCtx, cancelHeld := context.WithCancel(context.Background())
	defer cancelHeld()
	held, err := http.NewRequestWithContext(heldCtx, http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(body))
	require.NoError(t, err)
	held.Header.Set("Content-Type", "application/json")
	heldDone := make(chan struct{})
	go func() {
		defer close(heldDone)
		if response, err := http.DefaultClient.Do(held); err == nil {
			response.Body.Close()
		}
	}()
	require.Eventually(t, func() bool { return stats().Active == 1 }, time.Second, 10*time.Millisecond)

	type result struct {
		status int
		err    error
	}
	queuedDone := make(chan result, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		if err != nil {
			queuedDone <- result{err: err}
			return
		}
		response.Body.Close()
		queuedDone <- result{status: response.StatusCode}
	}()
	require.Eventually(t, func() bool { return stats().RequestsReceived == 2 }, time.Second, 10*time.Millisecond)
	// Give the queued request time to read its body and reach the worker wait.
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 1, stats().Active, "the second completion must still be waiting for the worker")

	patch, err := http.Post(server.URL+"/testenv/fault", "application/json", bytes.NewReader([]byte(`{"hang":false}`)))
	require.NoError(t, err)
	patch.Body.Close()
	cancelHeld()
	<-heldDone

	select {
	case got := <-queuedDone:
		require.NoError(t, got.err, "the queued completion must not be held by a hang lifted before its turn")
		require.Equal(t, http.StatusOK, got.status)
	case <-time.After(10 * time.Second):
		t.Fatal("the queued completion did not finish")
	}
	st := stats()
	require.EqualValues(t, 1, st.SuccessfulResponses)
	require.EqualValues(t, 1, st.Timeouts)
}
