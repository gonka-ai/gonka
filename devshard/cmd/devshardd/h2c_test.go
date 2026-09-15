package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestBuildServerEnablesH2C(t *testing.T) {
	e := buildServer(newLifecycleState())
	require.NotNil(t, e.Server.Handler)
	require.NotEqual(t, http.Handler(e), e.Server.Handler)

	srv := httptest.NewServer(e.Server.Handler)
	t.Cleanup(srv.Close)

	plain, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = plain.Body.Close() })
	require.Equal(t, 1, plain.ProtoMajor)
	require.Equal(t, http.StatusOK, plain.StatusCode)
	body, err := io.ReadAll(plain.Body)
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))

	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	h2 := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	resp, err := h2.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, 2, resp.ProtoMajor)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
