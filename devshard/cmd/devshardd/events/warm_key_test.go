package events

import (
	"context"
	"testing"
	"time"

	"devshard/bridge"
	"devshard/hostevents"

	abci "github.com/cometbft/cometbft/abci/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

func TestListenWarmKeys_SameListenerNoSecondSocket(t *testing.T) {
	l := NewListener("http://unused")
	l.OnNewBlock(func(context.Context, NewBlockEvent) {})
	l.OnDevshardEscrowCreated(func(context.Context, DevshardEscrowCreatedEvent) {})
	l.OnDevshardEscrowSettled(func(context.Context, DevshardEscrowSettledEvent) {})
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	p := hostevents.NewPoller(hostevents.PollerConfig{Warm: warm})
	l.ListenWarmKeys(p)

	require.Len(t, l.subs, 5, "warm-key queries join the listener that already serves NewBlock and escrow")
	queries := map[string]int{}
	for _, sub := range l.subs {
		queries[sub.query] = len(sub.handlers)
	}
	require.Equal(t, 2, queries["tm.event='NewBlock'"])
	require.Equal(t, 1, queries[DevshardEscrowCreatedEvent{}.query()])
	require.Equal(t, 1, queries[DevshardEscrowSettledEvent{}.query()])
	require.Equal(t, 1, queries[hostevents.WarmKeyGrantedQuery])
	require.Equal(t, 1, queries[hostevents.WarmKeyRevokedQuery])

	var granted *subscription
	for i := range l.subs {
		if l.subs[i].query == hostevents.WarmKeyGrantedQuery {
			granted = &l.subs[i]
		}
	}
	require.NotNil(t, granted)
	granted.handlers[0](context.Background(), ctypes.ResultEvent{Data: cmttypes.EventDataTx{TxResult: abci.TxResult{
		Height: 10,
		Result: abci.ExecTxResult{Events: []abci.Event{
			warmABCI("warm_key_revoked", "cold", "old", "1", "10", "WARM_KEY_LOG_END_REVOKED"),
			warmABCI("warm_key_granted", "cold", "new", "2", "10", ""),
		}},
	}}})
	ended, found, err := warm.WarmKeyEntry("cold", 1)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, ended.Ended())
	live, found, err := warm.WarmKeyEntry("cold", 2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "new", live.Grantee)

	block := cmttypes.MakeBlock(21, nil, nil, nil)
	parsed, ok := parseNewBlockEvent(ctypes.ResultEvent{Data: cmttypes.EventDataNewBlock{
		Block: block,
		ResultFinalizeBlock: abci.ResponseFinalizeBlock{Events: []abci.Event{
			warmABCI("warm_key_revoked", "cold", "new", "2", "21", "WARM_KEY_LOG_END_EXPIRED"),
		}},
	}})
	require.True(t, ok)
	require.Len(t, parsed.FinalizeEvents, 1)
	for _, sub := range l.subs {
		if sub.query != newBlockQuery {
			continue
		}
		sub.dispatch(context.Background(), ctypes.ResultEvent{Data: cmttypes.EventDataNewBlock{
			Block:               block,
			ResultFinalizeBlock: abci.ResponseFinalizeBlock{Events: parsed.FinalizeEvents},
		}})
	}
	ended, found, err = warm.WarmKeyEntry("cold", 2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "WARM_KEY_LOG_END_EXPIRED", ended.End)
}

func warmABCI(typ, granter, grantee, id, height, end string) abci.Event {
	attrs := []abci.EventAttribute{
		{Key: "granter", Value: granter},
		{Key: "grantee", Value: grantee},
		{Key: "grant_id", Value: id},
		{Key: "height", Value: height},
	}
	if end != "" {
		attrs = append(attrs, abci.EventAttribute{Key: "end", Value: end})
	}
	return abci.Event{Type: typ, Attributes: attrs}
}
