package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/transport"
)

func TestUndeclaredVersionErrorFromAttemptsSkipsProbesAndHost503(t *testing.T) {
	catalog := &transport.UpstreamStatusError{
		Path:          "/sessions/1/chat/completions",
		StatusCode:    http.StatusServiceUnavailable,
		Body:          "version v2 is not present in the governance routing catalog",
		DevshardError: transport.DevshardErrorUndeclaredVersion,
	}
	hostBusy := &transport.UpstreamStatusError{
		Path:       "/sessions/1/chat/completions",
		StatusCode: http.StatusServiceUnavailable,
		Body:       "nginx limit",
	}

	got := undeclaredVersionErrorFromAttempts([]*inflight{
		{probe: true, err: catalog},
		{err: hostBusy},
		{err: catalog},
	})
	require.Equal(t, catalog, got)

	require.Nil(t, undeclaredVersionErrorFromAttempts([]*inflight{
		{err: hostBusy},
	}))
}

func TestClientVisibleAllAttemptsFailedErrorSurfacesCatalogMiss(t *testing.T) {
	catalog := &transport.UpstreamStatusError{
		StatusCode:    http.StatusServiceUnavailable,
		DevshardError: transport.DevshardErrorUndeclaredVersion,
	}
	require.Equal(t, catalog, clientVisibleAllAttemptsFailedError([]*inflight{{err: catalog}}, 0))
	require.NoError(t, clientVisibleAllAttemptsFailedError([]*inflight{{
		err: &transport.UpstreamStatusError{StatusCode: http.StatusServiceUnavailable, Body: "nginx limit"},
	}}, 0))
}
