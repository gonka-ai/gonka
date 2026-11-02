package mockopenai_test

import (
	"bytes"
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
