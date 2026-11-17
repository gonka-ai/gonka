package mockheights

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChain_ProgressStampsTheWallClock(t *testing.T) {
	c := New()
	before := time.Now()
	b100, fresh := c.Progress(100)
	require.True(t, fresh)
	time.Sleep(5 * time.Millisecond)
	mid := time.Now()
	b101, fresh := c.Progress(101)
	after := time.Now()
	require.True(t, fresh)

	require.Equal(t, int64(100), b100.Height)
	require.Equal(t, int64(101), b101.Height)
	require.Equal(t, DefaultHash, b100.Hash)
	require.Equal(t, DefaultHash, b101.Hash)
	require.GreaterOrEqual(t, b100.TimeMs, before.UnixMilli())
	require.LessOrEqual(t, b100.TimeMs, mid.UnixMilli())
	require.GreaterOrEqual(t, b101.TimeMs, mid.UnixMilli())
	require.LessOrEqual(t, b101.TimeMs, after.UnixMilli())
	gap := b101.TimeMs - b100.TimeMs
	require.Greater(t, gap, int64(0))
	require.Less(t, gap, int64(500), "adjacent heights are not 1000 ms apart")
}

func TestChain_LaterHeightDoesNotMoveAnEarlierStamp(t *testing.T) {
	c := New()
	b100, _ := c.Progress(100)
	time.Sleep(5 * time.Millisecond)
	before := time.Now()
	bHi, fresh := c.Progress(100000)
	after := time.Now()
	require.True(t, fresh)

	got, ok := c.At(100)
	require.True(t, ok)
	require.Equal(t, b100.TimeMs, got.TimeMs)
	require.GreaterOrEqual(t, bHi.TimeMs, before.UnixMilli())
	require.LessOrEqual(t, bHi.TimeMs, after.UnixMilli())
	require.Less(t, bHi.TimeMs-got.TimeMs, int64(1000))
}

func TestChain_RepeatKeepsTheFirstStamp(t *testing.T) {
	c := New()
	first, fresh := c.Progress(100000)
	require.True(t, fresh)
	time.Sleep(5 * time.Millisecond)
	second, fresh := c.Progress(100000)
	require.False(t, fresh)
	require.Equal(t, first.TimeMs, second.TimeMs)
	require.Equal(t, first.Hash, second.Hash)
}

func TestChain_LowerHeightStartsANewSeries(t *testing.T) {
	c := New()
	_, _ = c.Progress(100000)
	before := time.Now()
	b50, fresh := c.Progress(50)
	after := time.Now()
	require.True(t, fresh)
	_, ok := c.At(100000)
	require.False(t, ok)
	got, ok := c.At(50)
	require.True(t, ok)
	require.Equal(t, b50.TimeMs, got.TimeMs)
	require.GreaterOrEqual(t, b50.TimeMs, before.UnixMilli())
	require.LessOrEqual(t, b50.TimeMs, after.UnixMilli())
}
