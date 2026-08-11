package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"devshard/cmd/devshardd/events"
)

func TestListener_StartWithUnreachableNode(t *testing.T) {
	// Listener pointed at an unreachable endpoint — run() errors and Start reconnects,
	// then the context timeout cancels it. No handlers should be called.
	l := events.NewListener("http://localhost:26657")

	var created []events.DevshardEscrowCreatedEvent
	l.OnDevshardEscrowCreated(func(_ context.Context, e events.DevshardEscrowCreatedEvent) {
		created = append(created, e)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = l.Start(ctx)

	assert.Empty(t, created)
}

func TestListener_OnNewBlock_NotCalledWhenUnreachable(t *testing.T) {
	l := events.NewListener("http://localhost:26657")

	var blocks []events.NewBlockEvent
	l.OnNewBlock(func(_ context.Context, e events.NewBlockEvent) {
		blocks = append(blocks, e)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = l.Start(ctx)

	assert.Empty(t, blocks)
}
