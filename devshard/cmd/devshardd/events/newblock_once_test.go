package events

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"devshard/bridge"
	"devshard/hostevents"

	abci "github.com/cometbft/cometbft/abci/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

func TestNewBlock_ParsedOnceCarriesHeightToExpiry(t *testing.T) {
	var marshals atomic.Int32
	prev := marshalBlockHeader
	marshalBlockHeader = func(h cmttypes.Header) ([]byte, error) {
		marshals.Add(1)
		return prev(h)
	}
	t.Cleanup(func() { marshalBlockHeader = prev })

	l := NewListener("http://unused")
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	p := hostevents.NewPoller(hostevents.PollerConfig{Warm: warm})
	l.ListenWarmKeys(p)
	l.OnNewBlock(func(context.Context, NewBlockEvent) {})

	block := cmttypes.MakeBlock(21, nil, nil, nil)
	var newBlock *subscription
	for i := range l.subs {
		if l.subs[i].query == newBlockQuery {
			newBlock = &l.subs[i]
		}
	}
	require.NotNil(t, newBlock)
	require.Len(t, newBlock.handlers, 2)

	newBlock.dispatch(context.Background(), ctypes.ResultEvent{Data: cmttypes.EventDataNewBlock{
		Block: block,
		ResultFinalizeBlock: abci.ResponseFinalizeBlock{Events: []abci.Event{{
			Type: "warm_key_revoked",
			Attributes: []abci.EventAttribute{
				{Key: "granter", Value: "cold"},
				{Key: "grantee", Value: "old"},
				{Key: "grant_id", Value: "2"},
				{Key: "end", Value: "WARM_KEY_LOG_END_EXPIRED"},
			},
		}}},
	}})

	require.Equal(t, int32(1), marshals.Load())
	ended, found, err := warm.WarmKeyEntry("cold", 2)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, ended.Ended())
	require.Equal(t, int64(21), ended.RevokedAtHeight)
}
