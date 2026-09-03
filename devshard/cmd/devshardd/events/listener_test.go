package events

import (
	"bytes"
	"testing"
	"time"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

func TestParseNewBlockEvent_HashAndTime(t *testing.T) {
	block := cmttypes.MakeBlock(12, nil, nil, nil)
	block.ChainID = "gonka-test"
	block.Time = time.Unix(1_700_000_000, 0).UTC()
	block.ValidatorsHash = bytes.Repeat([]byte{0xab}, 32)
	want := block.Header.Hash().Bytes()
	ev, ok := parseNewBlockEvent(ctypes.ResultEvent{
		Data: cmttypes.EventDataNewBlock{
			Block:   block,
			BlockID: cmttypes.BlockID{Hash: want},
		},
	})
	require.True(t, ok)
	require.Equal(t, int64(12), ev.BlockHeight)
	require.Equal(t, "gonka-test", ev.ChainID)
	require.Equal(t, block.Time, ev.Time)
	require.Equal(t, want, ev.BlockHash)
	require.False(t, ev.Time.IsZero())
	require.NotEmpty(t, ev.BlockHash)
}

func TestParseNewBlockEvent_NilBlock(t *testing.T) {
	_, ok := parseNewBlockEvent(ctypes.ResultEvent{Data: cmttypes.EventDataNewBlock{}})
	require.False(t, ok)
}
