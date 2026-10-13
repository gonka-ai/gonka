package tipcache

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"common/chainoracle/blocks/observer"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
)

const (
	cometReconnectDelay = 5 * time.Second
	cometSubscriberID   = "devshard-heightsync"
	cometSubBuffer      = 100
)

// CometHooks reports subscription liveness to failover.Oracle.
type CometHooks struct {
	OnConnected    func()
	OnDisconnected func()
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
	ch, err := client.Subscribe(subCtx, cometSubscriberID, "tm.event='NewBlock'", cometSubBuffer)
	if err != nil {
		return fmt.Errorf("subscribe NewBlock: %w", err)
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
		case result, ok := <-ch:
			if !ok {
				return fmt.Errorf("subscription closed")
			}
			data, ok := observer.AsEventDataNewBlock(result.Data)
			if !ok {
				slog.Warn("height-sync comet: unexpected NewBlock data type",
					"query", result.Query, "got", fmt.Sprintf("%T", result.Data))
				continue
			}
			hdr, ok := observer.HeaderFromNewBlock(data)
			if ok {
				c.Observe(hdr)
			}
		}
	}
}
