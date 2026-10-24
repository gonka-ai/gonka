package inference

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mlnodeclient "common/nodemanager"
	nmgen "common/nodemanager/gen"

	"github.com/stretchr/testify/require"
)

func TestTokenizeMLRequestPostsToTheNodeRoot(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"count":7,"max_model_len":400000,"tokens":[1,2,3,4,5,6,7]}`)
	}))
	defer srv.Close()
	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return &nmgen.AcquireMLNodeResponse{LockId: "lock", NodeId: "one", Endpoint: srv.URL}, nil
		},
	})
	mgr := mlnodeclient.NewManager(time.Hour)
	mgr.Observe("m", "one", srv.URL)
	e, _, free := creditTestEngine(t, false, ml, mgr)
	v := &Validator{engine: e}
	e.earnValidationCredit(context.Background(), "m")

	resp, err := v.tokenizeMLRequest(context.Background(), "m", "escrow-1", []byte(`{"model":"m","messages":[]}`))
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/tokenize", gotPath)
	require.JSONEq(t, `{"model":"m","messages":[]}`, gotBody)
	require.Contains(t, string(body), `"count":7`)
	require.Zero(t, free(), "the answered tokenize spent its credit")
}
