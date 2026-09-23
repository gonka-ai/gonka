package server

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/observability"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"
)

func testEchoContext(t *testing.T) echo.Context {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/sessions/x/chat/completions", nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}

func TestSessionHTTPErrorConflicts(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("wrapped: %w", storage.ErrSessionVersionConflict),
		fmt.Errorf("wrapped: %w", storage.ErrSessionEpochConflict),
	} {
		c := testEchoContext(t)
		httpErr, ok := sessionHTTPError(c, err).(*echo.HTTPError)
		require.True(t, ok)
		require.Equal(t, http.StatusConflict, httpErr.Code)
		require.Contains(t, fmt.Sprint(httpErr.Message), "wrapped")
	}
}

func TestSessionHTTPErrorInitializing(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/sessions/x/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := sessionHTTPError(c, fmt.Errorf("wrapped: %w", ErrInitializing))
	require.Error(t, err)
	e.HTTPErrorHandler(err, c)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, transport.DevshardErrorInitializing, rec.Header().Get(transport.HeaderDevshardError))
}

func TestSessionHTTPErrorIndexRebuilding(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/sessions/x/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := sessionHTTPError(c, fmt.Errorf("init storage session: %w", storage.ErrStorageIndexRebuilding))
	require.Error(t, err)
	e.HTTPErrorHandler(err, c)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, transport.DevshardErrorInitializing, rec.Header().Get(transport.HeaderDevshardError))

	status, reason := sessionResolutionStatus(fmt.Errorf("wrap: %w", storage.ErrStorageIndexRebuilding))
	require.Equal(t, observability.ReasonInitializing, reason)
	_ = status
}

func TestSessionHTTPErrorNotFound(t *testing.T) {
	c := testEchoContext(t)
	httpErr, ok := sessionHTTPError(c, storage.ErrSessionNotFound).(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusNotFound, httpErr.Code)
}

func TestSessionHTTPErrorPassthroughHTTPError(t *testing.T) {
	c := testEchoContext(t)
	orig := echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner")
	got := sessionHTTPError(c, orig)
	require.Equal(t, orig, got)
}

func TestSessionHTTPErrorChainUnavailable(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/sessions/x/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := sessionHTTPError(c, fmt.Errorf("get escrow: %w", bridge.ErrChainUnavailable))
	require.Error(t, err)
	e.HTTPErrorHandler(err, c)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, transport.DevshardErrorChainUnavailable, rec.Header().Get(transport.HeaderDevshardError))
}

func TestSessionHTTPErrorEscrowNotFoundStill500(t *testing.T) {
	c := testEchoContext(t)
	httpErr, ok := sessionHTTPError(c, fmt.Errorf("get escrow: %w", bridge.ErrEscrowNotFound)).(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusInternalServerError, httpErr.Code)
}

func TestSessionHTTPErrorDefault(t *testing.T) {
	c := testEchoContext(t)
	httpErr, ok := sessionHTTPError(c, fmt.Errorf("boom")).(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusInternalServerError, httpErr.Code)
}

// payloadsOnlyResolver refuses every escrow but one, so a route can be exercised without a real session.
type payloadsOnlyResolver struct{ resolves string }

func (r payloadsOnlyResolver) SessionServerExisting(escrowID string) (*transport.Server, error) {
	if escrowID != r.resolves {
		return nil, ErrInitializing
	}
	return nil, nil
}

// writingBinder writes to the response itself, as the inference route does.
type writingBinder struct{ body []byte }

func (b writingBinder) BindOwnerChat(c echo.Context) (*transport.Server, error) {
	if _, err := c.Response().Write(b.body); err != nil {
		return nil, err
	}
	return nil, ErrInitializing
}

type staticPayloadHandler struct{ body []byte }

func (h staticPayloadHandler) HandlePayloads(c echo.Context, _ *transport.Server) error {
	return c.JSONBlob(http.StatusOK, h.body)
}

func TestPayloadsRouteCompresses(t *testing.T) {
	body := []byte(`{"inference_id":"1","response_payload":"` + strings.Repeat("A", 8192) + `"}`)

	e := echo.New()
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: compressedRequestEscrowID}, writingBinder{body: body}, staticPayloadHandler{body: body})

	request := httptest.NewRequest(http.MethodGet, "/sessions/"+compressedRequestEscrowID+"/payloads", nil)
	request.Header.Set("Accept-Encoding", gzipEncodingName)
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, gzipEncodingName, recorder.Header().Get("Content-Encoding"))
	require.Less(t, recorder.Body.Len(), len(body)/4, "the compressed body should be a fraction of the payload")

	reader, err := gzip.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	require.NoError(t, err)
	defer reader.Close()
	decompressed, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(decompressed), "the payload must survive the wire unchanged")

	plain := httptest.NewRequest(http.MethodGet, "/sessions/"+compressedRequestEscrowID+"/payloads", nil)
	plainRecorder := httptest.NewRecorder()
	e.ServeHTTP(plainRecorder, plain)
	require.Equal(t, http.StatusOK, plainRecorder.Code)
	require.Empty(t, plainRecorder.Header().Get("Content-Encoding"))
	require.JSONEq(t, string(body), plainRecorder.Body.String())
}

// streamingBinder writes one frame per flush, as the handler does.
type streamingBinder struct{ frames []string }

func (b streamingBinder) BindOwnerChat(c echo.Context) (*transport.Server, error) {
	for _, frame := range b.frames {
		if _, err := c.Response().Write([]byte(frame)); err != nil {
			return nil, err
		}
		c.Response().Flush()
	}
	return nil, ErrInitializing
}

// Each frame must reach the caller before the next one is written.
func TestInferenceRouteStreamsEachFrameAsItIsFlushed(t *testing.T) {
	first := "data: {\"delta\":\"" + strings.Repeat("alpha ", 200) + "\"}\n\n"
	second := "data: {\"delta\":\"" + strings.Repeat("bravo ", 200) + "\"}\n\n"

	e := echo.New()
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: compressedRequestEscrowID},
		streamingBinder{frames: []string{first, second}}, nil)

	request := httptest.NewRequest(http.MethodPost, "/sessions/"+compressedRequestEscrowID+"/chat/completions", nil)
	request.Header.Set("Accept-Encoding", gzipEncodingName)
	recorder := testutil.NewFlushRecorder()
	e.ServeHTTP(recorder, request)

	require.Equal(t, gzipEncodingName, recorder.Header().Get("Content-Encoding"))
	require.GreaterOrEqual(t, len(recorder.Flushes()), 2, "one flush per frame must reach the wire")

	var sawFirstAlone bool
	for _, snapshot := range recorder.Flushes() {
		decoded := testutil.GzipDecodeSoFar(t, snapshot)
		if strings.Contains(decoded, "alpha") && !strings.Contains(decoded, "bravo") {
			sawFirstAlone = true
			break
		}
	}
	require.True(t, sawFirstAlone, "the first frame must reach the wire before the second is written")

	require.Less(t, len(recorder.Body()), len(first+second)/4, "the whole stream should still compress")
}

func TestInferenceRouteLeavesAPlainClientAlone(t *testing.T) {
	body := []byte("data: " + strings.Repeat("A", 8192) + "\n\n")

	e := echo.New()
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: compressedRequestEscrowID}, writingBinder{body: body}, nil)

	request := httptest.NewRequest(http.MethodPost, "/sessions/"+compressedRequestEscrowID+"/chat/completions", nil)
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)

	require.Empty(t, recorder.Header().Get("Content-Encoding"),
		"compression is negotiated: a client that does not ask keeps the bytes it expects")
	require.Equal(t, string(body), recorder.Body.String())
}

type countingBinder struct{ n *int }

func (b countingBinder) BindOwnerChat(c echo.Context) (*transport.Server, error) {
	*b.n++
	return nil, ErrInitializing
}

func TestHeightSyncSeedUsesOwnerBind(t *testing.T) {
	var n int
	e := echo.New()
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: "1"}, countingBinder{n: &n}, nil)

	req := httptest.NewRequest(http.MethodPost, "/sessions/1/height-sync", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, 1, n, "seed RPC must bind like owner chat so a host without a session can answer")
}

type fakeStaleReloader struct {
	reloads    int
	remembered int
	next       *transport.Server
	reloadErr  error
}

func (f *fakeStaleReloader) ReloadStaleSession(string, *transport.Server) (*transport.Server, error) {
	f.reloads++
	return f.next, f.reloadErr
}

func (f *fakeStaleReloader) RememberStaleNonce(string) { f.remembered++ }

func staleRetryContext(t *testing.T) echo.Context {
	t.Helper()
	c := testEchoContext(t)
	c.SetParamNames("id")
	c.SetParamValues("escrow-1")
	return c
}

func TestRetryIfStale_ReloadsAndSucceeds(t *testing.T) {
	stale, next := &transport.Server{}, &transport.Server{}
	f := &fakeStaleReloader{next: next}
	var saw *transport.Server
	err := retryIfStale(staleRetryContext(t), f, stale, fmt.Errorf("wrap: %w", types.ErrInvalidNonce), func(s *transport.Server) error {
		saw = s
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, f.reloads)
	require.Equal(t, next, saw)
	require.Zero(t, f.remembered)
}

func TestRetryIfStale_RemembersBogusNonce(t *testing.T) {
	stale, next := &transport.Server{}, &transport.Server{}
	f := &fakeStaleReloader{next: next}
	err := retryIfStale(staleRetryContext(t), f, stale, fmt.Errorf("wrap: %w", types.ErrInvalidNonce), func(*transport.Server) error {
		return fmt.Errorf("still: %w", types.ErrInvalidNonce)
	})
	require.ErrorIs(t, err, types.ErrInvalidNonce)
	require.Equal(t, 1, f.reloads)
	require.Equal(t, 1, f.remembered)
}

func TestRetryIfStale_IgnoresOtherErrors(t *testing.T) {
	f := &fakeStaleReloader{next: &transport.Server{}}
	orig := errors.New("not a nonce")
	err := retryIfStale(staleRetryContext(t), f, &transport.Server{}, orig, func(*transport.Server) error {
		t.Fatal("retry must not run for other errors")
		return nil
	})
	require.Equal(t, orig, err)
	require.Zero(t, f.reloads)
}

func TestRetryIfStale_SkipsWithoutReloader(t *testing.T) {
	orig := fmt.Errorf("wrap: %w", types.ErrInvalidNonce)
	err := retryIfStale(staleRetryContext(t), struct{}{}, &transport.Server{}, orig, func(*transport.Server) error {
		t.Fatal("retry must not run without a reloader")
		return nil
	})
	require.Equal(t, orig, err)
}
