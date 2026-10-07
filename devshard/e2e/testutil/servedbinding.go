package testutil

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const servedBindingsMetricPrefix = `devshard_gateway_served_bindings_total{verdict="`

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
		time.Sleep(250 * time.Millisecond)
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
		verdict, value, found := strings.Cut(strings.TrimPrefix(line, servedBindingsMetricPrefix), `"} `)
		require.True(t, found, "unexpected served binding sample %q", line)
		count, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		require.NoError(t, err, "served binding sample %q", line)
		bindings[verdict] = count
	}
	return bindings
}
