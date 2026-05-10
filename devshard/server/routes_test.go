package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/logging"
	"devshard/observability"
	"devshard/storage"
	"devshard/transport"
)

type failingResolver struct {
	err error
}

func (r failingResolver) SessionServer(string) (*transport.Server, error) {
	return nil, r.err
}

type captureLogger struct {
	infos []string
	warns []string
}

func (l *captureLogger) Info(msg string, _ ...any) { l.infos = append(l.infos, msg) }
func (l *captureLogger) Warn(msg string, _ ...any) { l.warns = append(l.warns, msg) }
func (l *captureLogger) Error(string, ...any)      {}
func (l *captureLogger) Debug(string, ...any)      {}

func TestSessionHTTPErrorConflicts(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("wrapped: %w", storage.ErrSessionVersionConflict),
		fmt.Errorf("wrapped: %w", storage.ErrSessionEpochConflict),
	} {
		httpErr, ok := sessionHTTPError(err).(*echo.HTTPError)
		require.True(t, ok)
		require.Equal(t, http.StatusConflict, httpErr.Code)
		require.Contains(t, fmt.Sprint(httpErr.Message), "wrapped")
	}
}

func TestSessionHTTPErrorInitializing(t *testing.T) {
	httpErr, ok := sessionHTTPError(fmt.Errorf("wrapped: %w", ErrInitializing)).(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, httpErr.Code)
	require.Contains(t, fmt.Sprint(httpErr.Message), "wrapped")
}

func TestSessionHTTPErrorDefault(t *testing.T) {
	httpErr, ok := sessionHTTPError(fmt.Errorf("boom")).(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusInternalServerError, httpErr.Code)
}

func TestLazyRoutesBindRequestID(t *testing.T) {
	e := echo.New()
	RegisterLazySessionRoutes(e.Group("/v1/devshard"), failingResolver{err: ErrInitializing}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/devshard/sessions/escrow-1/diffs", nil)
	req.Header.Set(observability.RequestIDHeader, "req-lazy")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "req-lazy", rec.Header().Get(observability.RequestIDHeader))
}

func TestLazyRoutesRecordTerminalOnlyForChatResolutionFailures(t *testing.T) {
	logger := &captureLogger{}
	logging.SetLogger(logger)
	e := echo.New()
	RegisterLazySessionRoutes(e.Group("/v1/devshard"), failingResolver{err: ErrInitializing}, nil)

	chatReq := httptest.NewRequest(http.MethodPost, "/v1/devshard/sessions/escrow-1/chat/completions", nil)
	e.ServeHTTP(httptest.NewRecorder(), chatReq)

	require.Contains(t, logger.infos, "devshard request terminal")

	logger.infos = nil
	statsReq := httptest.NewRequest(http.MethodGet, "/v1/devshard/sessions/escrow-1/diffs", nil)
	e.ServeHTTP(httptest.NewRecorder(), statsReq)

	require.NotContains(t, logger.infos, "devshard request terminal")
}
