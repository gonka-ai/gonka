package rpcface_test

import (
	"testing"
	"time"

	"devshard/testenv/mockchain/rpcface"

	"github.com/stretchr/testify/require"
)

func TestIntervalForHeight_DeterministicJitter(t *testing.T) {
	base := time.Second
	delta := 250 * time.Millisecond
	a := rpcface.IntervalForHeight(base, delta, 42, 10)
	b := rpcface.IntervalForHeight(base, delta, 42, 10)
	require.Equal(t, a, b)
	require.GreaterOrEqual(t, a, base-delta)
	require.LessOrEqual(t, a, base+delta)
}

func TestIntervalForHeight_NoDelta(t *testing.T) {
	require.Equal(t, time.Second, rpcface.IntervalForHeight(time.Second, 0, 1, 5))
}
