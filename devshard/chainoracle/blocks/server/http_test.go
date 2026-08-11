package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestServer_BlockLatest_RoundTripByteIdentical(t *testing.T) {
	ts, mock, cleanup := newTestStack(t)
	defer cleanup()

	source, err := mock.AdvanceOne()
	require.NoError(t, err)

	resp, err := http.Get(ts.URL + "/block/latest")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var decoded blocks.Header
	require.NoError(t, json.Unmarshal(body, &decoded))

	// Re-marshal the decoded header: bytes must match the server payload.
	reencoded, err := json.Marshal(&decoded)
	require.NoError(t, err)
	require.Equal(t, string(body), string(reencoded))

	// Canonical bytes across source and decoded must also match.
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

func TestServer_Stream_Ordering(t *testing.T) {
	ts, mock, cleanup := newTestStack(t)
	defer cleanup()
	// Seed two headers so /block/stream?from=1 replays immediately.
	_, err := mock.AdvanceOne()
	require.NoError(t, err)
	_, err = mock.AdvanceOne()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/block/stream?from=1", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Read first two SSE frames; assert heights 1 and 2 in order.
	heights := make([]int64, 0, 2)
	reader := bufio.NewReader(resp.Body)
	var data strings.Builder
	for len(heights) < 2 {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			var h blocks.Header
			require.NoError(t, json.Unmarshal([]byte(data.String()), &h))
			heights = append(heights, h.Height)
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			d := strings.TrimPrefix(line, "data:")
			d = strings.TrimPrefix(d, " ")
			data.WriteString(d)
		}
	}
	require.Equal(t, []int64{1, 2}, heights)

	// Advance one more and confirm the next frame arrives in order.
	_, err = mock.AdvanceOne()
	require.NoError(t, err)

	for {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			var h blocks.Header
			require.NoError(t, json.Unmarshal([]byte(data.String()), &h))
			require.Equal(t, int64(3), h.Height)
			return
		}
		if strings.HasPrefix(line, "data:") {
			d := strings.TrimPrefix(line, "data:")
			d = strings.TrimPrefix(d, " ")
			data.WriteString(d)
		}
	}
}

// Sanity check for the bad-height route.
func TestServer_BadHeight(t *testing.T) {
	ts, _, cleanup := newTestStack(t)
	defer cleanup()
	resp, err := http.Get(fmt.Sprintf("%s/block/not-a-number", ts.URL))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
