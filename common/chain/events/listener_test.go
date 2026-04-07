package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"common/chain/events"
)

func TestListener_RegisterAndDispatch(t *testing.T) {
	// Listener with an unreachable endpoint — GetLatestBlock will error, but no panic.
	conn, err := grpc.NewClient("localhost:9090", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	l := events.NewListener(conn)

	var received []events.Event
	l.Register(func(_ context.Context, e events.Event) {
		received = append(received, e)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = l.Start(ctx)

	assert.Empty(t, received)
}
