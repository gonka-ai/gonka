package rpcface

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/testenv/mockchain/seed"
)

func TestHeaderChain_LinksTimesAndPruning(t *testing.T) {
	st := seed.Defaults()
	c := newHeaderChain(st, time.Second)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	tip := st.GetBlockHeight()
	head, err := c.at(tip)
	require.NoError(t, err)
	require.Equal(t, now, head.block.Time, "the tip at first lookup is dated now")
	first, err := c.at(1)
	require.NoError(t, err)
	require.Equal(t, now.Add(-time.Duration(tip-1)*time.Second), first.block.Time)

	// A frozen clock still yields strictly increasing times.
	top := int64(retainedBlocks + pruneChunk)
	_, err = c.at(top)
	require.NoError(t, err)
	for h := c.lowest() + 1; h <= top; h++ {
		prev, err := c.at(h - 1)
		require.NoError(t, err)
		cur, err := c.at(h)
		require.NoError(t, err)
		require.True(t, bytes.Equal(prev.id.Hash, cur.block.LastBlockID.Hash), "height %d does not link to %d", h, h-1)
		require.True(t, prev.block.Time.Before(cur.block.Time), "height %d is not newer than %d", h, h-1)
		require.NoError(t, c.vals.VerifyCommitLight(st.GetChainID(), cur.id, h, cur.commit))
	}

	require.Equal(t, top-retainedBlocks+1, c.lowest())
	_, err = c.at(1)
	require.ErrorContains(t, err, "is not available")
	_, err = c.at(0)
	require.Error(t, err)
}
