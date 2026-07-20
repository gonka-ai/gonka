package events

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	defaultReconnectDelay = 5 * time.Second
	subscriberID          = "devshardd"
	subscriptionBuffer    = 100
)

// subscription holds a CometBFT query and one or more type-erased dispatch
// functions. Multiple handlers for the same query share a single WS
// subscription: CometBFT's HTTP client keeps only one channel per query
// (subscriptions[query] = outc), so a second Subscribe for the same query
// would overwrite the first and silently drop its handlers.
type subscription struct {
	query    string
	handlers []func(ctx context.Context, result ctypes.ResultEvent)
}

// Subscribe registers a typed subscription on l. parse is called for every
// matching ResultEvent; if it returns false the event is silently skipped.
// handle is called with the parsed value. All registrations must happen before
// Start is called.
//
// Handlers that share the same query are coalesced onto one CometBFT
// subscription and invoked in registration order.
func Subscribe[T any](l *Listener, query string, parse func(ctypes.ResultEvent) (T, bool), handle func(context.Context, T)) {
	handler := func(ctx context.Context, result ctypes.ResultEvent) {
		v, ok := parse(result)
		if ok {
			handle(ctx, v)
		}
	}
	for i := range l.subs {
		if l.subs[i].query == query {
			l.subs[i].handlers = append(l.subs[i].handlers, handler)
			return
		}
	}
	l.subs = append(l.subs, subscription{
		query:    query,
		handlers: []func(context.Context, ctypes.ResultEvent){handler},
	})
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
	Subscribe(l, "tm.event='NewBlock'", parseNewBlockEvent, h)
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

	client, err := rpchttp.New(l.rpcURL, "/websocket")
	if err != nil {
		return fmt.Errorf("rpc client: %w", err)
	}
	if err := client.Start(); err != nil {
		return fmt.Errorf("rpc start: %w", err)
	}
	defer client.Stop() //nolint:errcheck

	errCh := make(chan error, len(l.subs))

	for _, sub := range l.subs {
		ch, err := client.Subscribe(ctx, subscriberID, sub.query, subscriptionBuffer)
		if err != nil {
			return fmt.Errorf("subscribe %q: %w", sub.query, err)
		}
		handlers := sub.handlers
		q := sub.query
		go func() {
			for {
				result, ok := <-ch
				if !ok {
					errCh <- fmt.Errorf("subscription closed: %s", q)
					return
				}
				for _, h := range handlers {
					h(ctx, result)
				}
			}
		}()
	}

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

// parseNewBlockEvent extracts height from a NewBlock ResultEvent.
func parseNewBlockEvent(result ctypes.ResultEvent) (NewBlockEvent, bool) {
	data, ok := result.Data.(cmttypes.EventDataNewBlock)
	if !ok {
		return NewBlockEvent{}, false
	}
	return NewBlockEvent{BlockHeight: data.Block.Height}, true
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
