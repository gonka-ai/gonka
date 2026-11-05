package inference

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type argsSource struct {
	args  []string
	err   error
	calls int
}

func (s *argsSource) GetModelArgs(context.Context, uint64, string) ([]string, error) {
	s.calls++
	return s.args, s.err
}

func TestMaxModelLen(t *testing.T) {
	require.Equal(t, uint64(400000), MaxModelLen([]string{"--max-model-len", "400000"}))
	require.Equal(t, uint64(240000), MaxModelLen([]string{"--max-model-len=400000", "--max-model-len=240000"}))
	require.Equal(t, uint64(0), MaxModelLen([]string{"--tensor-parallel-size", "8"}))
	require.Equal(t, uint64(0), MaxModelLen([]string{"--max-model-len", "x"}))
}

func TestChainContextLimitsCachesAReadAndRetriesAFailure(t *testing.T) {
	src := &argsSource{err: errors.New("down")}
	limits := NewChainContextLimits(src)
	_, err := limits.Resolve(context.Background(), 1, "m")
	require.Error(t, err)
	src.err, src.args = nil, []string{"--max-model-len=400000"}
	for range 2 {
		limit, err := limits.Resolve(context.Background(), 1, "m")
		require.NoError(t, err)
		require.Equal(t, uint64(400000), limit)
	}
	require.Equal(t, 2, src.calls)
}
