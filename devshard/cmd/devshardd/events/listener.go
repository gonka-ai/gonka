package events

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	gogoproto "github.com/cosmos/gogoproto/proto"

	"common/chainoracle/blocks/observer"
	"devshard/hostevents"
)

const (
	defaultReconnectDelay = 5 * time.Second
	subscriberID          = "devshardd"
	subscriptionBuffer    = 100
	newBlockQuery         = "tm.event='NewBlock'"
)

// subscription holds a CometBFT query, one parse, and one or more handlers.
// Multiple handlers for the same query share a single WS subscription:
// CometBFT's HTTP client keeps only one channel per query
// (subscriptions[query] = outc), so a second Subscribe for the same query
// would overwrite the first and silently drop its handlers. A delivery is
// parsed once and that value is passed to every handler.
type subscription struct {
	query    string
	parse    func(ctypes.ResultEvent) (any, bool)
	handlers []func(ctx context.Context, parsed any)
}

// Subscribe registers a typed subscription on l. parse is called once per
// matching ResultEvent; if it returns false the event is silently skipped.
// handle is called with the parsed value. All registrations must happen before
// Start is called.
//
// Handlers that share the same query are coalesced onto one CometBFT
// subscription. They use the parse from the first registration and run in
// registration order.
func Subscribe[T any](l *Listener, query string, parse func(ctypes.ResultEvent) (T, bool), handle func(context.Context, T)) {
	handler := func(ctx context.Context, parsed any) {
		handle(ctx, parsed.(T))
	}
	for i := range l.subs {
		if l.subs[i].query == query {
			l.subs[i].handlers = append(l.subs[i].handlers, handler)
			return
		}
	}
	l.subs = append(l.subs, subscription{
		query: query,
		parse: func(result ctypes.ResultEvent) (any, bool) {
			v, ok := parse(result)
			if !ok {
				return nil, false
			}
			return v, true
		},
		handlers: []func(context.Context, any){handler},
	})
}

// dispatch parses result once and calls every handler with that value.
func (s subscription) dispatch(ctx context.Context, result ctypes.ResultEvent) {
	if s.parse == nil {
		return
	}
	parsed, ok := s.parse(result)
	if !ok {
		return
	}
	for _, h := range s.handlers {
		h(ctx, parsed)
	}
}

// Listener subscribes to chain events via CometBFT WebSocket and dispatches
// typed events to registered handlers. It reconnects automatically on disconnect.
//
// Usage:
//
//	l := events.NewListener("http://localhost:26657")
//	l.OnDevshardEscrowCreated(mgr.HandleEscrowCreated)
//	l.OnDevshardEscrowSettled(mgr.HandleEscrowSettled)
//	go l.Start(ctx)
type Listener struct {
	rpcURL         string
	subs           []subscription
	reconnectDelay time.Duration
	readyMu        sync.RWMutex
	ready          func(bool)
}

// NewListener creates a Listener that connects to the given CometBFT RPC URL.
// rpcURL is the CometBFT RPC HTTP endpoint, e.g. "http://localhost:26657".
func NewListener(rpcURL string) *Listener {
	return &Listener{
		rpcURL:         rpcURL,
		reconnectDelay: defaultReconnectDelay,
	}
}

// OnDevshardEscrowCreated registers a handler called for each devshard_escrow_created event.
func (l *Listener) OnDevshardEscrowCreated(h DevshardEscrowCreatedHandler) {
	Subscribe(l, DevshardEscrowCreatedEvent{}.query(), parseTxEvent[DevshardEscrowCreatedEvent], h)
}

// OnDevshardEscrowSettled registers a handler called for each devshard_escrow_settled event.
func (l *Listener) OnDevshardEscrowSettled(h DevshardEscrowSettledHandler) {
	Subscribe(l, DevshardEscrowSettledEvent{}.query(), parseTxEvent[DevshardEscrowSettledEvent], h)
}

// OnNewBlock registers a handler called for each newly committed block.
// Multiple OnNewBlock registrations share one CometBFT subscription.
func (l *Listener) OnNewBlock(h NewBlockHandler) {
	Subscribe(l, newBlockQuery, parseNewBlockEvent, h)
}

// OnWarmKeyTx registers both warm-key queries on this listener. CometBFT
// keeps one channel per query, and run dials one WebSocket for every query
// on the listener.
func (l *Listener) OnWarmKeyTx(h func(context.Context, ctypes.ResultEvent)) {
	if l == nil || h == nil {
		return
	}
	pass := func(result ctypes.ResultEvent) (ctypes.ResultEvent, bool) {
		return result, true
	}
	for _, q := range hostevents.WarmKeyQueries() {
		Subscribe(l, q, pass, h)
	}
}

// ListenWarmKeys registers the warm-key queries and a NewBlock handler that
// reads EndBlock expiries. Both share the listener's single socket.
func (l *Listener) ListenWarmKeys(p *hostevents.Poller) {
	if l == nil || p == nil {
		return
	}
	l.OnWarmKeyTx(func(_ context.Context, result ctypes.ResultEvent) {
		p.OfferCometWarm(hostevents.WarmKeysFromResult(result))
	})
	l.OnNewBlock(func(_ context.Context, e NewBlockEvent) {
		p.NoteCometHeight(e.BlockHeight)
		p.OfferCometWarm(hostevents.WarmKeysFromABCI(e.FinalizeEvents, e.BlockHeight))
	})
}

// OnReady registers a callback invoked with true after all subscriptions are
// active, and false when the listener disconnects or stops.
func (l *Listener) OnReady(h func(bool)) {
	l.readyMu.Lock()
	l.ready = h
	l.readyMu.Unlock()
}

func (l *Listener) setReady(ready bool) {
	l.readyMu.RLock()
	h := l.ready
	l.readyMu.RUnlock()
	if h != nil {
		h(ready)
	}
}

// Start connects to the chain and listens for events until ctx is cancelled.
// Reconnects automatically on connection failure or subscription drop.
func (l *Listener) Start(ctx context.Context) error {
	for {
		if err := l.run(ctx); ctx.Err() != nil {
			return ctx.Err()
		} else {
			slog.Warn("chain events: disconnected, reconnecting",
				"err", err, "delay", l.reconnectDelay)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(l.reconnectDelay):
		}
	}
}

func (l *Listener) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	slog.Info("chain events: connecting", "rpc", l.rpcURL, "subscriptions", len(l.subs))
	client, err := rpchttp.New(l.rpcURL, "/websocket")
	if err != nil {
		return fmt.Errorf("rpc client: %w", err)
	}
	client.SetLogger(cometSlog{})
	if err := client.Start(); err != nil {
		slog.Warn("chain events: websocket start failed", "rpc", l.rpcURL, "err", err)
		return fmt.Errorf("rpc start: %w", err)
	}
	defer client.Stop() //nolint:errcheck

	return l.serve(ctx, client)
}

// eventSubscriber is the CometBFT subscribe call. Tests pass a fake; run passes the HTTP client.
type eventSubscriber interface {
	Subscribe(ctx context.Context, subscriber, query string, outCapacity ...int) (<-chan ctypes.ResultEvent, error)
}

// subscribeOrder puts NewBlock and the escrow queries ahead of the warm-key
// queries. A later refusal then cannot drop a subscription that already succeeded.
func (l *Listener) subscribeOrder() []subscription {
	block := -1
	var required, warm []subscription
	for i := range l.subs {
		sub := l.subs[i]
		switch {
		case sub.query == newBlockQuery:
			block = i
		case warmKeyQuery(sub.query):
			warm = append(warm, sub)
		default:
			required = append(required, sub)
		}
	}
	out := make([]subscription, 0, len(l.subs))
	if block >= 0 {
		out = append(out, l.subs[block])
	}
	out = append(out, required...)
	out = append(out, warm...)
	return out
}

func warmKeyQuery(query string) bool {
	for _, q := range hostevents.WarmKeyQueries() {
		if q == query {
			return true
		}
	}
	return false
}

// serve subscribes in subscribeOrder. A refused warm-key query is logged and
// skipped. A refused NewBlock or escrow query returns, and Start dials again.
func (l *Listener) serve(ctx context.Context, client eventSubscriber) error {
	errCh := make(chan error, len(l.subs))
	queries := make([]string, 0, len(l.subs))
	for _, sub := range l.subscribeOrder() {
		ch, err := client.Subscribe(ctx, subscriberID, sub.query, subscriptionBuffer)
		if err != nil {
			if warmKeyQuery(sub.query) {
				slog.Warn("chain events: warm-key subscribe refused, continuing",
					"rpc", l.rpcURL, "query", sub.query, "err", err)
				continue
			}
			slog.Warn("chain events: subscribe failed", "rpc", l.rpcURL, "query", sub.query, "err", err)
			return fmt.Errorf("subscribe %q: %w", sub.query, err)
		}
		queries = append(queries, sub.query)
		sub := sub
		go func() {
			for {
				result, ok := <-ch
				if !ok {
					errCh <- fmt.Errorf("subscription closed: %s", sub.query)
					return
				}
				sub.dispatch(ctx, result)
			}
		}()
	}
	slog.Info("chain events: subscribed", "rpc", l.rpcURL, "queries", queries)
	l.setReady(true)
	defer l.setReady(false)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// txEventParser is implemented by event types that can build themselves from
// a block height and a raw ABCI event.
type txEventParser[T any] interface {
	fromEvent(height int64, ev abci.Event) T
	eventType() string
	query() string
}

// parseTxEvent finds the first ABCI event of eventType in a Tx ResultEvent and
// calls T.fromEvent on it.
func parseTxEvent[T txEventParser[T]](result ctypes.ResultEvent) (out T, ok bool) {
	data, isTx := result.Data.(cmttypes.EventDataTx)
	if !isTx {
		slog.Warn("chain events: unexpected data type", "event_type", out.eventType(), "got", fmt.Sprintf("%T", result.Data))
		return
	}
	for _, ev := range data.TxResult.Result.Events {
		if ev.Type == out.eventType() {
			return out.fromEvent(data.TxResult.Height, ev), true
		}
	}
	return
}

// marshalBlockHeader encodes a Comet header. Tests replace it to count calls.
var marshalBlockHeader = func(h cmttypes.Header) ([]byte, error) {
	return gogoproto.Marshal(h.ToProto())
}

// parseNewBlockEvent extracts height, hash, time, and chain id from a
// NewBlock ResultEvent.
func parseNewBlockEvent(result ctypes.ResultEvent) (NewBlockEvent, bool) {
	data, ok := observer.AsEventDataNewBlock(result.Data)
	if !ok {
		slog.Warn("chain events: unexpected NewBlock data type",
			"query", result.Query, "got", fmt.Sprintf("%T", result.Data))
		return NewBlockEvent{}, false
	}
	hdr, ok := observer.HeaderFromNewBlock(data)
	if !ok {
		slog.Warn("chain events: NewBlock missing block payload", "query", result.Query)
		return NewBlockEvent{}, false
	}
	raw, err := marshalBlockHeader(data.Block.Header)
	if err != nil {
		slog.Warn("chain events: NewBlock header marshal failed", "query", result.Query, "err", err)
	}
	return NewBlockEvent{
		BlockHeight:    hdr.Height,
		BlockHash:      hdr.BlockHash,
		Time:           hdr.Time,
		ChainID:        hdr.ChainID,
		Header:         raw,
		FinalizeEvents: append([]abci.Event(nil), data.ResultFinalizeBlock.Events...),
	}, true
}

func attr(ev abci.Event, key string) string {
	for _, a := range ev.Attributes {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

func parseUint64(s string) uint64 {
	var n uint64
	fmt.Sscanf(s, "%d", &n)
	return n
}
