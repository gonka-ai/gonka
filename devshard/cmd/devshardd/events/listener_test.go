package events

import (
	"bytes"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
)

func TestParseNewBlockEvent_HashAndTime(t *testing.T) {
	block := cmttypes.MakeBlock(12, nil, nil, nil)
	block.Header.ChainID = "gonka-test"
	block.Header.Time = time.Unix(1_700_000_000, 0).UTC()
	block.Header.ValidatorsHash = bytes.Repeat([]byte{0xab}, 32)
	block.Header.ProposerAddress = bytes.Repeat([]byte{0xcd}, 20)
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
	require.Equal(t, block.Header.Time, ev.Time)
	require.Equal(t, want, ev.BlockHash)
	require.False(t, ev.Time.IsZero())
	require.NotEmpty(t, ev.BlockHash)
	raw, err := gogoproto.Marshal(block.Header.ToProto())
	require.NoError(t, err)
	require.Equal(t, raw, ev.Header)
	var pb cmtproto.Header
	require.NoError(t, gogoproto.Unmarshal(ev.Header, &pb))
	got, err := cmttypes.HeaderFromProto(&pb)
	require.NoError(t, err)
	require.Equal(t, want, got.Hash().Bytes())
}

func TestParseNewBlockEvent_NilBlock(t *testing.T) {
	_, ok := parseNewBlockEvent(ctypes.ResultEvent{Data: cmttypes.EventDataNewBlock{}})
	require.False(t, ok)
}

func TestParseNewBlockEvent_PointerData(t *testing.T) {
	block := cmttypes.MakeBlock(12, nil, nil, nil)
	block.Header.ChainID = "gonka-test"
	block.Header.Time = time.Unix(1_700_000_000, 0).UTC()
	block.Header.ValidatorsHash = bytes.Repeat([]byte{0xab}, 32)
	data := &cmttypes.EventDataNewBlock{Block: block}
	ev, ok := parseNewBlockEvent(ctypes.ResultEvent{Data: data})
	require.True(t, ok)
	require.Equal(t, int64(12), ev.BlockHeight)
	require.Equal(t, block.Header.Hash().Bytes(), ev.BlockHash)
}
