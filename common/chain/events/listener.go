package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"google.golang.org/grpc"
)

const defaultPollInterval = 2 * time.Second

// Event carries a new-block notification.
type Event struct {
	BlockHeight int64
}

// Handler is called for each new block.
type Handler func(ctx context.Context, event Event)

// Listener polls the chain for new blocks via gRPC and dispatches events.
type Listener struct {
	conn     grpc.ClientConnInterface
	interval time.Duration
	handlers []Handler
}

// NewListener creates a Listener using the given gRPC connection.
func NewListener(conn grpc.ClientConnInterface) *Listener {
	return &Listener{conn: conn, interval: defaultPollInterval}
}

// Register adds a handler called for every new block.
func (l *Listener) Register(h Handler) {
	l.handlers = append(l.handlers, h)
}

// Start polls for new blocks until ctx is cancelled.
func (l *Listener) Start(ctx context.Context) error {
	svc := cmtservice.NewServiceClient(l.conn)
	var lastHeight int64

	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			resp, err := svc.GetLatestBlock(ctx, &cmtservice.GetLatestBlockRequest{})
			if err != nil {
				slog.Warn("chain events: GetLatestBlock failed", "err", err)
				continue
			}
			h := resp.SdkBlock.Header.Height
			if h <= lastHeight {
				continue
			}
			lastHeight = h
			e := Event{BlockHeight: h}
			for _, handler := range l.handlers {
				handler(ctx, e)
			}
		}
	}
}
