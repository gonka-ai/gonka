package inference

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaxModelLen(t *testing.T) {
	require.EqualValues(t, 400000, MaxModelLen([]string{"--max-model-len", "400000", "--kv-cache-dtype", "fp8"}))
	require.EqualValues(t, 180000, MaxModelLen([]string{"--max-model-len=180000"}))
	require.EqualValues(t, 600000, MaxModelLen([]string{"--max-model-len", "400000", "--max-model-len=600000"}), "vLLM keeps the last value")
	require.Zero(t, MaxModelLen([]string{"--max-model-len", "400k"}))
	require.Zero(t, MaxModelLen([]string{"--max-model-len"}))
	require.Zero(t, MaxModelLen(nil))
}

type stubModelArgs struct {
	args  []string
	err   error
	calls int
}

func (s *stubModelArgs) GetModelArgs(context.Context, uint64, string) ([]string, error) {
	s.calls++
	return s.args, s.err
}

func TestChainContextLimits_CachesAFoundLimitAndRetriesAFailure(t *testing.T) {
	source := &stubModelArgs{err: errors.New("chain down")}
	limits := NewChainContextLimits(source)

	require.Zero(t, limits.Resolve(context.Background(), 7, "m"))
	source.err, source.args = nil, []string{"--max-model-len", "400000"}
	require.EqualValues(t, 400000, limits.Resolve(context.Background(), 7, "m"))
	require.EqualValues(t, 400000, limits.Resolve(context.Background(), 7, "m"))
	require.Equal(t, 2, source.calls)

	var nilLimits *ChainContextLimits
	require.Zero(t, nilLimits.Resolve(context.Background(), 7, "m"))
}
