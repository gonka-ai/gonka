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
