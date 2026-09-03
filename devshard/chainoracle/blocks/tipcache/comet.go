package tipcache

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"common/chainoracle/blocks/observer"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	cometReconnectDelay = 5 * time.Second
	cometSubscriberID   = "devshard-heightsync"
	cometSubBuffer      = 100
)

// StartComet feeds c from CometBFT tm.event='NewBlock' until ctx is cancelled.
// Same subscription hosts already use; gateway has no other NewBlock listener.
func StartComet(ctx context.Context, rpcURL string, c *Cache) error {
	if c == nil {
		return fmt.Errorf("tipcache: nil cache")
	}
	if rpcURL == "" {
		return fmt.Errorf("tipcache: empty comet rpc url")
	}
	go runComet(ctx, rpcURL, c)
	return nil
}

func runComet(ctx context.Context, rpcURL string, c *Cache) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := consumeComet(ctx, rpcURL, c)
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

func consumeComet(ctx context.Context, rpcURL string, c *Cache) error {
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
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result, ok := <-ch:
			if !ok {
				return fmt.Errorf("subscription closed")
			}
			data, ok := result.Data.(cmttypes.EventDataNewBlock)
			if !ok {
				continue
			}
			hdr, ok := observer.HeaderFromNewBlock(data)
			if ok {
				c.Observe(hdr)
			}
		}
	}
}
