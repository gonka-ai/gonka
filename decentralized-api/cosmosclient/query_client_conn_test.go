package cosmosclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Exercise the real generated query -> SDK client.Context -> ABCI HTTP path,
// whose SDK implementation replaces the caller's context with Background.
func TestObservedQueryClientConn_CancelsABCIHTTP(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			started := make(chan string, 1)
			stopped := make(chan struct{})
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				started <- string(body)
				select {
				case <-r.Context().Done():
					close(stopped)
				case <-release:
				}
			}))
			t.Cleanup(srv.Close)
			t.Cleanup(func() { close(release) })
			rpc, err := rpchttp.NewWithClient(srv.URL, "/websocket", &http.Client{})
			require.NoError(t, err)
			cdc := codec.NewProtoCodec(cdctypes.NewInterfaceRegistry())
			qc := types.NewQueryClient(newObservedQueryClientConn(client.Context{}.WithCodec(cdc).WithClient(rpc)))
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 200*time.Millisecond)
				want = context.DeadlineExceeded
			}
			t.Cleanup(cancel)
			result := make(chan error, 1)
			go func() {
				_, err := qc.DevshardEscrow(ctx, &types.QueryGetDevshardEscrowRequest{Id: 42})
				result <- err
			}()
			select {
			case body := <-started:
				require.Contains(t, body, "/inference.inference.Query/DevshardEscrow")
			case err := <-result:
				t.Fatalf("query failed before reaching HTTP: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("query did not reach the ABCI server")
			}
			if !deadline {
				cancel()
			}
			select {
			case err := <-result:
				require.ErrorIs(t, err, want)
			case <-time.After(time.Second):
				t.Fatal("SDK ABCI request ignored cancellation")
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("underlying HTTP request was not cancelled")
			}
		})
	}
}
