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
