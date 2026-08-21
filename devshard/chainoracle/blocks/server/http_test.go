package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/chainoracle/blocks/observer"
	"devshard/chainoracle/blocks/server"
	"devshard/signing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func newTestStack(t *testing.T) (*httptest.Server, *observer.Mock, func()) {
	t.Helper()

	signer, err := signing.GenerateKey()
	require.NoError(t, err)
	addr, err := blocks.AddressBytes(signer.PublicKeyBytes())
	require.NoError(t, err)
	mock, err := observer.NewMock(observer.MockConfig{
		ChainID: "gonka-test",
		Validators: []observer.MockValidator{
			{Signer: signer, Address: addr, Power: 1},
		},
		BlockInterval: time.Second,
		Seed:          99,
		Start:         time.Unix(1_700_000_000, 0).UTC(),
		InitialHeight: 1,
	})
	require.NoError(t, err)

	e := echo.New()
	server.Mount(e.Group(""), mock)
	ts := httptest.NewServer(e)
	return ts, mock, func() { ts.Close() }
}

func TestServer_Healthz(t *testing.T) {
	ts, _, cleanup := newTestStack(t)
	defer cleanup()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "ok", string(body))
}

func TestServer_BlockAt_RoundTripByteIdentical(t *testing.T) {
	ts, mock, cleanup := newTestStack(t)
	defer cleanup()

	source, err := mock.AdvanceOne()
	require.NoError(t, err)

	resp, err := http.Get(fmt.Sprintf("%s/block/%d", ts.URL, source.Height))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var decoded blocks.Header
	require.NoError(t, json.Unmarshal(body, &decoded))

	reencoded, err := json.Marshal(&decoded)
	require.NoError(t, err)
	require.Equal(t, string(body), string(reencoded))
	require.Equal(t,
		blocks.CanonicalHeaderBytes(source),
		blocks.CanonicalHeaderBytes(&decoded),
	)
}

func TestServer_BlockAt_NotFound(t *testing.T) {
	ts, _, cleanup := newTestStack(t)
	defer cleanup()

	resp, err := http.Get(ts.URL + "/block/42")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestServer_Prove_ReturnsStableBytes(t *testing.T) {
	ts, mock, cleanup := newTestStack(t)
	defer cleanup()
	_, err := mock.AdvanceOne()
	require.NoError(t, err)

	url := ts.URL + "/block/1/prove?path=" + "/escrow/1"
	r1, err := http.Get(url)
	require.NoError(t, err)
	defer r1.Body.Close()
	b1, err := io.ReadAll(r1.Body)
	require.NoError(t, err)
	r2, err := http.Get(url)
	require.NoError(t, err)
	defer r2.Body.Close()
	b2, err := io.ReadAll(r2.Body)
	require.NoError(t, err)
	require.Equal(t, b1, b2)
}

func TestServer_BadHeight(t *testing.T) {
	ts, _, cleanup := newTestStack(t)
	defer cleanup()
	resp, err := http.Get(fmt.Sprintf("%s/block/not-a-number", ts.URL))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

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

func TestServer_Prove_NotImplemented(t *testing.T) {
	e := echo.New()
	server.Mount(e.Group(""), hashOnlyOracle{})
	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/block/1/prove?path=/escrow/1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotImplemented, resp.StatusCode)
}
