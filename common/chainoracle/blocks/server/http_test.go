package server_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/server"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

type hashOnlyOracle struct{}

func (hashOnlyOracle) Latest(context.Context) (*blocks.Header, error) {
	return blocks.HashOnlyHeader(1, time.Unix(1, 0).UTC(), "gonka-test", []byte{1}), nil
}
func (hashOnlyOracle) At(ctx context.Context, _ int64) (*blocks.Header, error) {
	return hashOnlyOracle{}.Latest(ctx)
}
func (hashOnlyOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}
func (hashOnlyOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestServer_HealthzAndUnary(t *testing.T) {
	e := echo.New()
	server.Mount(e.Group(""), hashOnlyOracle{})
	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "ok", string(body))

	resp, err = http.Get(ts.URL + "/block/1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = http.Get(ts.URL + "/block/1/prove?path=/escrow/1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotImplemented, resp.StatusCode)
}

func TestServer_NoLiveTipRoutes(t *testing.T) {
	e := echo.New()
	server.Mount(e.Group(""), hashOnlyOracle{})
	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	// Both paths hit GET /block/:height and fail parseHeight — they are
	// not live-tip aliases.
	for _, path := range []string{"/block/latest", "/block/stream"} {
		resp, err := http.Get(ts.URL + path)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, path)
	}
}

type atErrOracle struct {
	err error
}

func (atErrOracle) Latest(context.Context) (*blocks.Header, error) {
	return nil, errors.New("unused")
}
func (o atErrOracle) At(context.Context, int64) (*blocks.Header, error) {
	return nil, o.err
}
func (atErrOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}
func (atErrOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestServer_AtNotFoundVsBadGateway(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		e := echo.New()
		server.Mount(e.Group(""), atErrOracle{err: blocks.ErrHeaderNotFound})
		ts := httptest.NewServer(e)
		t.Cleanup(ts.Close)
		resp, err := http.Get(ts.URL + "/block/3")
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
	t.Run("transport", func(t *testing.T) {
		e := echo.New()
		server.Mount(e.Group(""), atErrOracle{err: errors.New("i/o timeout")})
		ts := httptest.NewServer(e)
		t.Cleanup(ts.Close)
		resp, err := http.Get(ts.URL + "/block/3")
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	})
}
