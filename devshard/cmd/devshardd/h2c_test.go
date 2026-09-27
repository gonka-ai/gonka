package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	devshardserver "devshard/server"
)

func TestBuildServerEnablesH2C(t *testing.T) {
	e := buildServer(newLifecycleState())
	require.NotNil(t, e.Server.Protocols)
	require.True(t, e.Server.Protocols.HTTP1())
	require.True(t, e.Server.Protocols.UnencryptedHTTP2())

	errCh := make(chan error, 1)
	go func() { errCh <- devshardserver.StartH2C(e, "127.0.0.1:0") }()

	var addr string
	require.Eventually(t, func() bool {
		if e.Listener == nil || e.Listener.Addr() == nil {
			return false
		}
		addr = e.Listener.Addr().String()
		return addr != ""
	}, 2*time.Second, 10*time.Millisecond)

	plain, err := http.Get("http://" + addr + "/healthz")
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
	resp, err := h2.Get("http://" + addr + "/healthz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, 2, resp.ProtoMajor)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, e.Shutdown(shutdownCtx))
	require.ErrorIs(t, <-errCh, http.ErrServerClosed)
}
