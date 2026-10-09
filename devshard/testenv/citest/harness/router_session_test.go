package harness

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestRouterSessionURL_StickyPath(t *testing.T) {
	got := RouterSessionURL("http://127.0.0.1:18080", "v2", "escrow-42", "/healthz")
	require.Equal(t, "http://127.0.0.1:18080/v2/sessions/escrow-42/healthz", got)
}

func TestGetSuccessfulResponseHeaderRejectsHTTPFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header:     http.Header{StickyUpstreamHeader: []string{"versiond-2"}},
			Body:       http.NoBody,
		}, nil
	})}

	value, err := GetSuccessfulResponseHeader(
		client,
		"http://router/v1/sessions/escrow/healthz",
		StickyUpstreamHeader,
	)
	require.Error(t, err)
	require.Empty(t, value)
}
