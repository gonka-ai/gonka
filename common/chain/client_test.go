package chain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"common/chain"
)

func TestNew_DialSuccess(t *testing.T) {
	// grpc.Dial is non-blocking by default — succeeds even with no server
	c, err := chain.New("http://localhost:26657")
	require.NoError(t, err)
	assert.NotNil(t, c)
	assert.NotNil(t, c.InferenceQueryClient())
	assert.NotNil(t, c.BLSQueryClient())
	assert.NotNil(t, c.RestrictionsQueryClient())
	assert.NotNil(t, c.CometServiceClient())
}

func TestNew_ConnIsReturned(t *testing.T) {
	c, err := chain.New("localhost:9090")
	require.NoError(t, err)
	assert.NotNil(t, c.Conn())
}

func TestNewFromConn(t *testing.T) {
	c1, err := chain.New("localhost:9090")
	require.NoError(t, err)
	c2 := chain.NewFromConn(c1.Conn())
	assert.NotNil(t, c2)
}

func TestTLSEnabled_DefaultOff(t *testing.T) {
	t.Setenv(chain.EnvChainGRPCTLS, "")
	require.False(t, chain.TLSEnabled())
}

func TestTLSEnabled_Truthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", " Yes "} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(chain.EnvChainGRPCTLS, v)
			require.True(t, chain.TLSEnabled())
		})
	}
}

func TestNew_WithTLSEnvStillDials(t *testing.T) {
	t.Setenv(chain.EnvChainGRPCTLS, "true")
	c, err := chain.New("localhost:9090")
	require.NoError(t, err)
	assert.NotNil(t, c.Conn())
}

func TestRPCURLFromGRPCURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"host and port", "node:9090", "http://node:26657"},
		{"host only", "node", "http://node:26657"},
		{"ipv4", "10.0.0.4:9090", "http://10.0.0.4:26657"},
		{"ipv6", "[::1]:9090", "http://[::1]:26657"},
		{"scheme is dropped", "http://node:9090", "http://node:26657"},
		{"grpc resolver prefix", "dns:///node:9090", "http://node:26657"},
		{"surrounding space", "  node:9090  ", "http://node:26657"},
		{"empty", "", ""},
		{"no host", "://", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, chain.RPCURLFromGRPCURL(tc.in))
		})
	}
}

func TestNewWithRPCFallback_AcceptsDerivedRPCURL(t *testing.T) {
	rpcURL := chain.RPCURLFromGRPCURL("node:9090")
	c, err := chain.NewWithRPCFallback("node:9090", rpcURL)
	require.NoError(t, err)
	assert.NotNil(t, c.CometServiceClient())
}

func TestNewWithRPCFallback_RequiresRPCURL(t *testing.T) {
	_, err := chain.NewWithRPCFallback("node:9090", "")
	require.Error(t, err, "the explicit constructor must not silently skip the fallback")
}

func TestNewWithQueryFallback_DerivesMissingRPCURL(t *testing.T) {
	c, err := chain.NewWithQueryFallback("node:9090", "")
	require.NoError(t, err)
	assert.NotEqual(t, c.Conn(), c.QueryConn(), "queries must run on the fallback conn")
}

func TestNewWithQueryFallback_KeepsExplicitRPCURL(t *testing.T) {
	c, err := chain.NewWithQueryFallback("node:9090", "http://other:36657")
	require.NoError(t, err)
	assert.NotEqual(t, c.Conn(), c.QueryConn())
}

func TestNewWithQueryFallback_WithoutAnyRPCEndpointStaysOnGRPC(t *testing.T) {
	// A gRPC target with no host leaves nothing to derive from. Adding a
	// resilience feature must not stop a service from booting, so the client
	// runs on gRPC alone.
	c, err := chain.NewWithQueryFallback("://", "")
	require.NoError(t, err)
	assert.Equal(t, c.Conn(), c.QueryConn(), "queries must fall back to the direct conn")
}
