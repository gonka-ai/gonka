package tipcache

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/stretchr/testify/require"
)

func TestHandleCometDelivery_WarnsOnBadNewBlockWithWarmQueries(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	hooks := CometHooks{ExtraQueries: []string{
		"tm.event='Tx' AND warm_key_granted.grant_id EXISTS",
		"tm.event='Tx' AND warm_key_revoked.grant_id EXISTS",
	}}
	handleCometDelivery(ctypes.ResultEvent{Query: cometNewBlockQuery, Data: "not-a-block"}, &Cache{}, hooks)
	require.Contains(t, buf.String(), "unexpected NewBlock data type")

	buf.Reset()
	handleCometDelivery(ctypes.ResultEvent{
		Query: "tm.event='Tx' AND warm_key_granted.grant_id EXISTS",
		Data:  "not-a-block",
	}, &Cache{}, hooks)
	require.NotContains(t, buf.String(), "unexpected NewBlock data type")
}

func TestSubscribeQueries_OneClientForNewBlockAndWarmKeys(t *testing.T) {
	granted := "tm.event='Tx' AND warm_key_granted.grant_id EXISTS"
	revoked := "tm.event='Tx' AND warm_key_revoked.grant_id EXISTS"
	hooks := CometHooks{ExtraQueries: []string{granted, revoked, granted, ""}}
	require.Equal(t, []string{cometNewBlockQuery, granted, revoked}, SubscribeQueries(hooks))

	client := &fakeComet{}
	channels, err := subscribeComet(context.Background(), client, SubscribeQueries(hooks))
	require.NoError(t, err)
	require.Len(t, channels, 3)
	require.Equal(t, []string{cometNewBlockQuery, granted, revoked}, client.queries)
	require.Equal(t, []string{cometSubscriberID, cometSubscriberID, cometSubscriberID}, client.subscribers)
}

type fakeComet struct {
	queries     []string
	subscribers []string
}

func (f *fakeComet) Subscribe(_ context.Context, subscriber, query string, _ ...int) (<-chan ctypes.ResultEvent, error) {
	f.queries = append(f.queries, query)
	f.subscribers = append(f.subscribers, subscriber)
	ch := make(chan ctypes.ResultEvent)
	return ch, nil
}
