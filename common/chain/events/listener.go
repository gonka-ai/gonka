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
	subscriberID          = "subnetd"
	subscriptionBuffer    = 100

	querySubnetEscrowCreated = "tm.event='Tx' AND subnet_escrow_created.escrow_id EXISTS"
	querySubnetEscrowSettled = "tm.event='Tx' AND subnet_escrow_settled.escrow_id EXISTS"

	eventTypeCreated = "subnet_escrow_created"
	eventTypeSettled = "subnet_escrow_settled"
)

// SubnetEscrowCreatedEvent carries parsed attributes from a subnet_escrow_created chain event.
// Emitted by x/inference/keeper/msg_server_create_subnet_escrow.go.
type SubnetEscrowCreatedEvent struct {
	BlockHeight int64
	EscrowID    uint64
	Creator     string
	Amount      uint64
	EpochIndex  uint64
}

// SubnetEscrowSettledEvent carries parsed attributes from a subnet_escrow_settled chain event.
// Emitted by x/inference/keeper/msg_server_settle_subnet_escrow.go.
type SubnetEscrowSettledEvent struct {
	BlockHeight int64
	EscrowID    uint64
	Settler     string
	TotalPayout uint64
	Fees        uint64
	Remainder   uint64
}

// SubnetEscrowCreatedHandler is called for each subnet_escrow_created event received.
type SubnetEscrowCreatedHandler func(ctx context.Context, e SubnetEscrowCreatedEvent)

// SubnetEscrowSettledHandler is called for each subnet_escrow_settled event received.
type SubnetEscrowSettledHandler func(ctx context.Context, e SubnetEscrowSettledEvent)

// Listener subscribes to subnet chain events via CometBFT WebSocket and dispatches typed events.
// It reconnects automatically on disconnect.
//
// Usage:
//
//	l := events.NewListener("http://localhost:26657")
//	l.OnSubnetEscrowCreated(mgr.HandleEscrowCreated)
//	l.OnSubnetEscrowSettled(mgr.HandleEscrowSettled)
//	go l.Start(ctx)
type Listener struct {
	rpcURL         string
	createdHooks   []SubnetEscrowCreatedHandler
	settledHooks   []SubnetEscrowSettledHandler
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

// OnSubnetEscrowCreated registers a handler called for each subnet_escrow_created event.
func (l *Listener) OnSubnetEscrowCreated(h SubnetEscrowCreatedHandler) {
	l.createdHooks = append(l.createdHooks, h)
}

// OnSubnetEscrowSettled registers a handler called for each subnet_escrow_settled event.
func (l *Listener) OnSubnetEscrowSettled(h SubnetEscrowSettledHandler) {
	l.settledHooks = append(l.settledHooks, h)
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

	created, err := client.Subscribe(ctx, subscriberID, querySubnetEscrowCreated, subscriptionBuffer)
	if err != nil {
		return fmt.Errorf("subscribe subnet_escrow_created: %w", err)
	}

	settled, err := client.Subscribe(ctx, subscriberID, querySubnetEscrowSettled, subscriptionBuffer)
	if err != nil {
		return fmt.Errorf("subscribe subnet_escrow_settled: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result, ok := <-created:
			if !ok {
				return fmt.Errorf("subnet_escrow_created subscription closed")
			}
			l.dispatchCreated(ctx, result)
		case result, ok := <-settled:
			if !ok {
				return fmt.Errorf("subnet_escrow_settled subscription closed")
			}
			l.dispatchSettled(ctx, result)
		}
	}
}

func (l *Listener) dispatchCreated(ctx context.Context, result ctypes.ResultEvent) {
	data, ok := result.Data.(cmttypes.EventDataTx)
	if !ok {
		slog.Warn("chain events: unexpected data type for subnet_escrow_created", "type", fmt.Sprintf("%T", result.Data))
		return
	}
	height := data.TxResult.Height
	for _, ev := range data.TxResult.Result.Events {
		if ev.Type != eventTypeCreated {
			continue
		}
		attrs := attrsMap(ev.Attributes)
		e := SubnetEscrowCreatedEvent{
			BlockHeight: height,
			EscrowID:    parseUint64(attrs["escrow_id"]),
			Creator:     attrs["creator"],
			Amount:      parseUint64(attrs["amount"]),
			EpochIndex:  parseUint64(attrs["epoch_index"]),
		}
		for _, h := range l.createdHooks {
			h(ctx, e)
		}
	}
}

func (l *Listener) dispatchSettled(ctx context.Context, result ctypes.ResultEvent) {
	data, ok := result.Data.(cmttypes.EventDataTx)
	if !ok {
		slog.Warn("chain events: unexpected data type for subnet_escrow_settled", "type", fmt.Sprintf("%T", result.Data))
		return
	}
	height := data.TxResult.Height
	for _, ev := range data.TxResult.Result.Events {
		if ev.Type != eventTypeSettled {
			continue
		}
		attrs := attrsMap(ev.Attributes)
		e := SubnetEscrowSettledEvent{
			BlockHeight: height,
			EscrowID:    parseUint64(attrs["escrow_id"]),
			Settler:     attrs["settler"],
			TotalPayout: parseUint64(attrs["total_payout"]),
			Fees:        parseUint64(attrs["fees"]),
			Remainder:   parseUint64(attrs["remainder"]),
		}
		for _, h := range l.settledHooks {
			h(ctx, e)
		}
	}
}

// attrsMap converts a slice of ABCI event attributes into a key→value map.
func attrsMap(attrs []abci.EventAttribute) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Value
	}
	return m
}

func parseUint64(s string) uint64 {
	var n uint64
	fmt.Sscanf(s, "%d", &n)
	return n
}
