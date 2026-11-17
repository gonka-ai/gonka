package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestRegisterLiveness_ServesVersionedHealthz(t *testing.T) {
	e := echo.New()
	registerLiveness(e, "dev")

	for _, path := range []string{"/health", "/devshard/dev/healthz", "/dev/healthz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, path)
	}

	for _, path := range []string{"/devshard/v5/healthz", "/v5/healthz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code, "other versions must not look admitted")
	}
}

func TestGroupFromKeys_DerivesCompactSlotGroup(t *testing.T) {
	group, err := groupFromKeys(defaultHostPrivateKeys())
	require.NoError(t, err)
	require.Len(t, group, 3)
	for i, slot := range group {
		require.Equal(t, uint32(i), slot.SlotID)
		require.NotEmpty(t, slot.ValidatorAddress)
	}
}

func TestSplitCSV_TrimsEmptyValues(t *testing.T) {
	require.Equal(t, []string{"a", "b", "c"}, splitCSV(" a, b ,, c "))
	require.Nil(t, splitCSV(""))
}

func TestSessionConfigFromEnv_MapsEscrowTimeouts(t *testing.T) {
	t.Setenv("DEVSHARD_REFUSAL_TIMEOUT", "5")
	t.Setenv("DEVSHARD_EXECUTION_TIMEOUT", "17")

	cfg, err := sessionConfigFromEnv(3)
	require.NoError(t, err)
	require.Equal(t, int64(5), cfg.RefusalTimeout)
	require.Equal(t, int64(17), cfg.ExecutionTimeout)
	require.False(t, cfg.HeightsyncTimeoutClock)
}

func TestSessionConfigFromEnv_MapsTimeoutClock(t *testing.T) {
	t.Setenv("DEVSHARD_HEIGHTSYNC_TIMEOUT_CLOCK", "true")
	cfg, err := sessionConfigFromEnv(3)
	require.NoError(t, err)
	require.True(t, cfg.HeightsyncTimeoutClock)

	t.Setenv("DEVSHARD_HEIGHTSYNC_TIMEOUT_CLOCK", "")
	cfg, err = sessionConfigFromEnv(3)
	require.NoError(t, err)
	require.False(t, cfg.HeightsyncTimeoutClock, "unset keeps the legacy clock")

	t.Setenv("DEVSHARD_HEIGHTSYNC_TIMEOUT_CLOCK", "nope")
	_, err = sessionConfigFromEnv(3)
	require.Error(t, err)
}

func TestTimeoutClockDebug_ReportsBoundClock(t *testing.T) {
	e := echo.New()
	e.GET("/debug/timeout-clock", func(c echo.Context) error {
		return timeoutClockDebug(c, true)
	})
	req := httptest.NewRequest(http.MethodGet, "/debug/timeout-clock", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"heightsync_timeout_clock":true}`, rec.Body.String())
}

func TestBoolEnv(t *testing.T) {
	const key = "DEVSHARD_STUB_EXECUTION_HANG"

	t.Setenv(key, "on")
	value, err := boolEnv(key, false)
	require.NoError(t, err)
	require.True(t, value)

	t.Setenv(key, "f")
	value, err = boolEnv(key, true)
	require.NoError(t, err)
	require.False(t, value)

	t.Setenv(key, "")
	value, err = boolEnv(key, true)
	require.NoError(t, err)
	require.True(t, value, "empty value must preserve the caller fallback")

	t.Setenv(key, "invalid")
	_, err = boolEnv(key, false)
	require.Error(t, err)
}
