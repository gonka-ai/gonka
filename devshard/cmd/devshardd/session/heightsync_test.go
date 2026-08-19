package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/internal/testutil"
	"devshard/stub"

	"github.com/stretchr/testify/require"
)

func TestSetHeightSyncFromEnv_EmptyIsNoop(t *testing.T) {
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "")
	t.Setenv("DEVSHARD_HEIGHTSYNC", "")
	mgr := NewHostManager(newManagerTestStore(t), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil))
	require.Nil(t, mgr.heightSync)
	require.Nil(t, mgr.chainOracle)
	require.Len(t, mgr.transportServerOpts(), 2)
}

func TestSetHeightSyncFromEnv_InvalidK(t *testing.T) {
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "http://127.0.0.1:9")
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "nope")
	mgr := NewHostManager(newManagerTestStore(t), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	err := mgr.SetHeightSyncFromEnv(context.Background(), nil)
	require.Error(t, err)
	require.Nil(t, mgr.heightSync)
}

func TestSetHeightSyncFromEnv_WiresScheduler(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/block/latest":
			hdr := blocks.Header{
				Height:    7,
				Time:      time.Unix(1_700_000_000, 0).UTC(),
				ChainID:   "gonka-test",
				BlockHash: []byte{1, 2, 3, 4},
			}
			_ = json.NewEncoder(w).Encode(hdr)
		case "/block/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	t.Setenv("DEVSHARD_CHAINORACLE_URL", ts.URL)
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "10")
	t.Setenv("DEVSHARD_HEIGHTSYNC_SLOTS", "1")

	mgr := NewHostManager(newManagerTestStore(t), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil))
	require.NotNil(t, mgr.heightSync)
	require.NotNil(t, mgr.chainOracle)
	require.Equal(t, uint64(10), mgr.heightSync.K())
	require.Equal(t, uint64(1), mgr.heightSync.SlotsNum())
	require.Len(t, mgr.transportServerOpts(), 3)
	mgr.CloseHeightSync()
	require.Nil(t, mgr.heightSync)
}

func TestSetHeightSyncFromEnv_FlagWithoutOracleErrors(t *testing.T) {
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "")
	t.Setenv("DEVSHARD_HEIGHTSYNC", "1")
	mgr := NewHostManager(newManagerTestStore(t), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	err := mgr.SetHeightSyncFromEnv(context.Background(), nil)
	require.Error(t, err)
	require.Nil(t, mgr.heightSync)
}
