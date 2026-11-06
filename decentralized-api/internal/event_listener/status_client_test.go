package event_listener

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetStatusReusesConnection(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"node_info":{},"sync_info":{"latest_block_height":"7","catching_up":false},"validator_info":{}}}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	for i := 0; i < 5; i++ {
		st, err := getStatus(srv.URL)
		require.NoError(t, err)
		require.False(t, st.SyncInfo.CatchingUp)
		require.EqualValues(t, 7, st.SyncInfo.LatestBlockHeight)
	}
	require.EqualValues(t, 1, conns.Load())
}
