package session

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/stub"
	"devshard/transport"
)

func TestStatsRPCOneShotTwoEscrows(t *testing.T) {
	base := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, base, "escrow-a", 7, 0)
	createStoredSession(t, base, "escrow-b", 7, 0)
	store := currentEpochStore{Storage: base, epoch: 7}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil))
	mgr.SetBinaryVersion("0.2.14-v4-r2")

	rec := requestStats(t, mgr, statsTestRoutePrefix, "/stats/rpc")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Empty(t, rec.Header().Get("Content-Encoding"))

	var snap transport.RPCStatsSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	require.Equal(t, testutil.RuntimeTestVersion, snap.ProtocolVersion)
	require.Equal(t, "0.2.14-v4-r2", snap.BinaryVersion)
	require.Equal(t, hostSigner.Address(), snap.HostAddress)
	require.NotZero(t, snap.MinuteUnix)
	require.Len(t, snap.Shards, 2)
	require.Equal(t, "escrow-a", snap.Shards[0].EscrowID)
	require.Equal(t, "escrow-b", snap.Shards[1].EscrowID)
	require.Equal(t, testutil.RuntimeTestVersion, snap.Shards[0].ProtocolVersion)
}

func TestWriteJSONMaybeGzip(t *testing.T) {
	payload := bytes.Repeat([]byte("n"), transport.MinGzipBodyBytes+32)
	e := echo.New()
	e.GET("/x", func(c echo.Context) error { return writeJSONMaybeGzip(c, payload) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	gr, err := gzip.NewReader(rec.Body)
	require.NoError(t, err)
	out, err := io.ReadAll(gr)
	require.NoError(t, err)
	require.Equal(t, payload, out)

	plain := httptest.NewRequest(http.MethodGet, "/x", nil)
	prec := httptest.NewRecorder()
	e.ServeHTTP(prec, plain)
	require.Empty(t, prec.Header().Get("Content-Encoding"))
	require.Equal(t, payload, prec.Body.Bytes())
}

func TestStatsRPCIdentityWithoutGzipHeader(t *testing.T) {
	base := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, base, "escrow-a", 7, 0)
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(base, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil))
	e := echo.New()
	mgr.Register(e.Group(""))
	req := httptest.NewRequest(http.MethodGet, "/stats/rpc", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	// Empty snapshot JSON is under the 1 KiB floor.
	require.Empty(t, rec.Header().Get("Content-Encoding"))
	require.True(t, strings.Contains(rec.Body.String(), `"host"`))
}

func TestStatsRPCJSONInvalidatesOnMinuteBoundary(t *testing.T) {
	base := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, base, "escrow-a", 7, 0)
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(base, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil))

	now1 := time.Unix(100*60+55, 0)
	body1, err := mgr.statsRPCJSON(now1)
	require.NoError(t, err)
	now2 := now1.Add(10 * time.Second)
	body2, err := mgr.statsRPCJSON(now2)
	require.NoError(t, err)

	var s1, s2 transport.RPCStatsSnapshot
	require.NoError(t, json.Unmarshal(body1, &s1))
	require.NoError(t, json.Unmarshal(body2, &s2))
	require.Equal(t, (now1.Unix()/60-1)*60, s1.MinuteUnix)
	require.Equal(t, (now2.Unix()/60-1)*60, s2.MinuteUnix)
	require.NotEqual(t, s1.MinuteUnix, s2.MinuteUnix)
}
