package inference

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	mlnodeclient "common/nodemanager"
	nmgen "common/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	replayStreamOK        = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
	replayStreamEngineErr = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\ndata: {\"error\":{\"message\":\"EngineCore died\",\"type\":\"InternalServerError\",\"param\":null,\"code\":500}}\n\ndata: [DONE]\n\n"
	replayStreamBadReq    = "data: {\"error\":{\"message\":\"bad\",\"type\":\"BadRequestError\",\"param\":null,\"code\":400}}\n\ndata: [DONE]\n\n"
	replayStreamTruncated = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n"
	replayStreamNoUsage   = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n"
	replayStreamMalformed = "data: {\"choices\":[{\"index\":0,\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
)

func TestValidationReplayStreamFailuresRotateNodesAndRefundCredit(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("shared=%t/fallback=%t", shared, fallback), func(t *testing.T) {
				var hits atomic.Int32
				replies := make(chan string, 4)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					body := replayStreamEngineErr
					select {
					case body = <-replies:
					default:
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, body)
				}))
				defer srv.Close()
				ml := startEngineMLClient(t, &engineMockNM{
					acquireFunc: func(_ context.Context, req *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
						if fallback {
							return nil, status.Error(codes.Unavailable, "offline")
						}
						return &nmgen.AcquireMLNodeResponse{LockId: "lock", NodeId: fmt.Sprint(len(req.ExcludedNodes)), Endpoint: srv.URL}, nil
					},
				})
				mgr := mlnodeclient.NewManager(time.Hour)
				mgr.Observe("m", "one", srv.URL)
				mgr.Observe("m", "two", srv.URL)
				e, _, free := creditTestEngine(t, shared, ml, mgr)
				v := &Validator{engine: e}
				earn := func() { e.earnValidationCredit(context.Background(), "m") }

				earn()
				replies <- replayStreamEngineErr
				replies <- replayStreamOK
				resp, err := v.executeMLRequest(context.Background(), "m", "escrow-1", []byte(`{}`))
				require.NoError(t, err)
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Equal(t, replayStreamOK, string(body))
				require.Equal(t, int32(2), hits.Load(), "an in-stream engine error rotates to the next node")
				require.Zero(t, free(), "the answered validation spent its one credit")

				earn()
				replies <- replayStreamBadReq
				resp, err = v.executeMLRequest(context.Background(), "m", "escrow-2", []byte(`{}`))
				require.NoError(t, err)
				body, err = io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, http.StatusBadRequest, resp.StatusCode)
				require.JSONEq(t, `{"error":{"message":"bad","type":"BadRequestError","param":null,"code":400}}`, string(body))
				require.Zero(t, free(), "an in-stream 4xx is an answer and spends the credit")

				for i, broken := range []string{replayStreamTruncated, replayStreamNoUsage, replayStreamMalformed} {
					earn()
					before := hits.Load()
					replies <- broken
					replies <- broken
					_, err = v.executeMLRequest(context.Background(), "m", fmt.Sprintf("escrow-broken-%d", i), []byte(`{}`))
					require.Error(t, err)
					require.Greater(t, hits.Load()-before, int32(1), "a broken stream rotates to another node")
					require.Equal(t, i+1, free(), "no node completed the stream, so the credit returns")
				}
			})
		}
	}
}
