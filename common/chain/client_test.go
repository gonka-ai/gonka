package chain_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"common/chain"
)

func newTestClient() *chain.Client {
	return chain.New("http://localhost:26657")
}

func TestGetRandomExecutor_ReturnsNotImplemented(t *testing.T) {
	dest, err := newTestClient().GetRandomExecutor(context.Background(), "some-model")
	require.Error(t, err)
	assert.Nil(t, dest)
}

func TestGetInference_ReturnsNotImplemented(t *testing.T) {
	inf, err := newTestClient().GetInference(context.Background(), "inf-123")
	require.Error(t, err)
	assert.Nil(t, inf)
}

func TestGetEpochSeed_ReturnsNotImplemented(t *testing.T) {
	seed, err := newTestClient().GetEpochSeed(context.Background(), 42)
	require.Error(t, err)
	assert.Nil(t, seed)
}

func TestGetEpochInfo_ReturnsNotImplemented(t *testing.T) {
	epochID, blockHeight, err := newTestClient().GetEpochInfo(context.Background())
	require.Error(t, err)
	assert.Equal(t, uint64(0), epochID)
	assert.Equal(t, int64(0), blockHeight)
}
