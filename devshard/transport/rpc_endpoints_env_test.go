package transport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRPCEndpointsFromEnv_DefaultIsEveryWiredMethod(t *testing.T) {
	t.Setenv(envRPCEndpoints, "")
	set := RPCEndpointsFromEnv()
	for _, name := range attachRPCEndpoints {
		require.True(t, set.Has(name), name)
	}
}

func TestRPCEndpointsFromEnv_OffKeepsHTTPClient(t *testing.T) {
	for _, raw := range []string{"off", "none", "http", " OFF "} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(envRPCEndpoints, raw)
			require.True(t, RPCEndpointsFromEnv().Empty())
		})
	}
}

func TestRPCEndpointsFromEnv_ExplicitList(t *testing.T) {
	t.Setenv(envRPCEndpoints, "chat")
	set := RPCEndpointsFromEnv()
	require.True(t, set.Has(EndpointChat))
	require.False(t, set.Has(EndpointGossip))
}
