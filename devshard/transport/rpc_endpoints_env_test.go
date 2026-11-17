package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
)

func TestRPCEndpointsFromEnv_DefaultIsEveryWiredMethod(t *testing.T) {
	t.Setenv(envRPCEndpoints, "")
	set := RPCEndpointsFromEnv()
	for _, name := range attachRPCEndpoints {
		require.True(t, set.Has(name), name)
	}
}

func TestResolveRPCEndpoints_PartialListUsesEveryMethod(t *testing.T) {
	for _, raw := range []string{"off", "gossip,diffs", "echo-http", "chat"} {
		set := ResolveRPCEndpoints(ParseRPCEndpoints(raw))
		for _, name := range attachRPCEndpoints {
			require.True(t, set.Has(name), "%s missing from %q", name, raw)
		}
	}
	full := ResolveRPCEndpoints(AllRPCEndpoints())
	require.Equal(t, len(attachRPCEndpoints), len(full))
}

func TestHTTPClient_RetiredSessionDoesNotPost(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)
	c := NewHTTPClient(srv.URL, "escrow-1", nil)
	ok, err := c.SeedHeightSync(context.Background())
	require.False(t, ok)
	require.ErrorIs(t, err, ErrHTTPSessionRetired)
	_, err = c.Send(context.Background(), host.HostRequest{}, nil, nil)
	require.ErrorIs(t, err, ErrHTTPSessionRetired)
	require.Zero(t, hits.Load())
}

func TestRPCEndpointsFromEnv_IgnoresOptOut(t *testing.T) {
	for _, raw := range []string{"off", "none", "http", " OFF ", "chat", ","} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(envRPCEndpoints, raw)
			set := RPCEndpointsFromEnv()
			for _, name := range attachRPCEndpoints {
				require.True(t, set.Has(name), name)
			}
		})
	}
}
