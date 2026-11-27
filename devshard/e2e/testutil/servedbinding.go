package testutil

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	servedBindingsMetricPrefix = `devshard_gateway_served_bindings_total{`
	servedBindingsVerdictLabel = `verdict="`
	servedBindingsPollInterval = 250 * time.Millisecond
)

// ServedBindings counts the gateway's checks of a stream against the executor's signed Finish, by verdict.
type ServedBindings map[string]float64

// WaitServedBindings polls the gateway metrics until they satisfy ready or timeout expires, and reports what it last read.
func WaitServedBindings(t *testing.T, client *http.Client, clientURL string, timeout time.Duration, ready func(ServedBindings) bool) ServedBindings {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last ServedBindings
	for time.Now().Before(deadline) {
		last = readServedBindings(t, client, clientURL)
		if ready(last) {
			return last
		}
		time.Sleep(servedBindingsPollInterval)
	}
	return last
}

// WaitServedBindingsWhileServing sends one streaming completion per poll, since a host's Finish rides its next answer, until the bindings satisfy ready or timeout expires.
func WaitServedBindingsWhileServing(t *testing.T, client *http.Client, clientURL string, timeout time.Duration, ready func(ServedBindings) bool) ServedBindings {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last ServedBindings
	for round := 0; time.Now().Before(deadline); round++ {
		last = readServedBindings(t, client, clientURL)
		if ready(last) {
			return last
		}
		carrier, err := PostJSONRawE(client, clientURL+"/v1/chat/completions",
			ChatCompletionBody(fmt.Sprintf("carry the pending finish %d", round), true), AdminAPIKey)
		DebugLogf(t, "finish carrier %d status=%d err=%v", round, carrier.StatusCode, err)
		time.Sleep(servedBindingsPollInterval)
	}
	return last
}

func readServedBindings(t *testing.T, client *http.Client, clientURL string) ServedBindings {
	t.Helper()
	response, err := client.Get(clientURL + "/metrics")
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode, "gateway metrics should be readable")
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	bindings := ServedBindings{}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, servedBindingsMetricPrefix) {
			continue
		}
		labels, value, found := strings.Cut(strings.TrimPrefix(line, servedBindingsMetricPrefix), "} ")
		require.True(t, found, "unexpected served binding sample %q", line)
		_, verdictOnward, found := strings.Cut(labels, servedBindingsVerdictLabel)
		require.True(t, found, "served binding sample %q carries no verdict", line)
		verdict, _, _ := strings.Cut(verdictOnward, `"`)
		count, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		require.NoError(t, err, "served binding sample %q", line)
		bindings[verdict] += count
	}
	return bindings
}
