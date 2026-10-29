package tipcache

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
)

const (
	cometReconnectDelay = 5 * time.Second
	cometSubscriberID   = "devshard-heightsync"
	cometSubBuffer      = 100
)

// CometHooks reports subscription liveness to failover.Oracle.
// OnHeader receives each NewBlock header after it is stored in the tip cache.
// ExtraQueries are subscribed on the same client as NewBlock. OnEvent receives
// every delivery, including NewBlock, so warm-key handlers can read it.
type CometHooks struct {
	OnConnected    func()
	OnDisconnected func()
	OnHeader       func(*blocks.Header)
	OnEvent        func(ctypes.ResultEvent)
	ExtraQueries   []string
}

const cometNewBlockQuery = "tm.event='NewBlock'"

// SubscribeQueries is the query list one rpchttp client subscribes. NewBlock
// is first. Duplicate and empty extras are dropped.
func SubscribeQueries(hooks CometHooks) []string {
	qs := []string{cometNewBlockQuery}
	seen := map[string]bool{cometNewBlockQuery: true}
	for _, q := range hooks.ExtraQueries {
		if q == "" || seen[q] {
			continue
		}
		seen[q] = true
		qs = append(qs, q)
	}
	return qs
}

type cometSubscriber interface {
	Subscribe(ctx context.Context, subscriber, query string, outCapacity ...int) (<-chan ctypes.ResultEvent, error)
}

func subscribeComet(ctx context.Context, client cometSubscriber, queries []string) ([]<-chan ctypes.ResultEvent, error) {
	out := make([]<-chan ctypes.ResultEvent, 0, len(queries))
	for _, q := range queries {
		ch, err := client.Subscribe(ctx, cometSubscriberID, q, cometSubBuffer)
		if err != nil {
			return nil, fmt.Errorf("subscribe %q: %w", q, err)
		}
		out = append(out, ch)
	}
	return out, nil
}

// StartComet feeds c from CometBFT tm.event='NewBlock' until ctx is cancelled.
// Same subscription hosts already use; gateway has no other NewBlock listener.
func StartComet(ctx context.Context, rpcURL string, c *Cache) error {
	return StartCometWithHooks(ctx, rpcURL, c, CometHooks{})
}

// StartCometWithHooks is StartComet plus connect/disconnect callbacks.
func StartCometWithHooks(ctx context.Context, rpcURL string, c *Cache, hooks CometHooks) error {
	if c == nil {
		return fmt.Errorf("tipcache: nil cache")
	}
	if rpcURL == "" {
		return fmt.Errorf("tipcache: empty comet rpc url")
	}
	go runComet(ctx, rpcURL, c, hooks)
	return nil
}

func runComet(ctx context.Context, rpcURL string, c *Cache, hooks CometHooks) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := consumeComet(ctx, rpcURL, c, hooks)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("height-sync comet: disconnected, reconnecting",
			"err", err, "delay", cometReconnectDelay, "rpc", rpcURL)
		select {
		case <-ctx.Done():
			return
		case <-time.After(cometReconnectDelay):
		}
	}
}

// handleCometDelivery applies one socket message. A warm-key transaction is
// left to OnEvent. A NewBlock whose payload is not a block is logged.
func handleCometDelivery(result ctypes.ResultEvent, c *Cache, hooks CometHooks) {
	if hooks.OnEvent != nil {
		hooks.OnEvent(result)
	}
	data, ok := observer.AsEventDataNewBlock(result.Data)
	if !ok {
		if result.Query == cometNewBlockQuery {
			slog.Warn("height-sync comet: unexpected NewBlock data type",
				"query", result.Query, "got", fmt.Sprintf("%T", result.Data))
		}
		return
	}
	hdr, ok := observer.HeaderFromNewBlock(data)
	if !ok {
		return
	}
	c.Observe(hdr)
	if hooks.OnHeader != nil {
		hooks.OnHeader(hdr)
	}
}

func consumeComet(ctx context.Context, rpcURL string, c *Cache, hooks CometHooks) error {
	client, err := rpchttp.New(rpcURL, "/websocket")
	if err != nil {
		return fmt.Errorf("rpc client: %w", err)
	}
	if err := client.Start(); err != nil {
		return fmt.Errorf("rpc start: %w", err)
	}
	defer client.Stop() //nolint:errcheck

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	channels, err := subscribeComet(subCtx, client, SubscribeQueries(hooks))
	if err != nil {
		return err
	}
	merged := make(chan ctypes.ResultEvent, cometSubBuffer)
	errCh := make(chan error, 1)
	for _, ch := range channels {
		go func(ch <-chan ctypes.ResultEvent) {
			for {
				select {
				case <-subCtx.Done():
					return
				case result, ok := <-ch:
					if !ok {
						select {
						case errCh <- fmt.Errorf("subscription closed"):
						default:
						}
						return
					}
					select {
					case merged <- result:
					case <-subCtx.Done():
						return
					}
				}
			}
		}(ch)
	}
	if hooks.OnConnected != nil {
		hooks.OnConnected()
	}
	defer func() {
		if hooks.OnDisconnected != nil {
			hooks.OnDisconnected()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case result := <-merged:
			handleCometDelivery(result, c, hooks)
		}
	}
}
