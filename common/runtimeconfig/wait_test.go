package runtimeconfig

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWait_Notified(t *testing.T) {
	wake := make(chan struct{})
	close(wake)
	outcome, err := Wait(context.Background(), wake, time.Second)
	require.NoError(t, err)
	require.Equal(t, Notified, outcome)
}

func TestWait_TimedOut(t *testing.T) {
	wake := make(chan struct{})
	outcome, err := Wait(context.Background(), wake, 20*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, TimedOut, outcome)
}

func TestWait_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wake := make(chan struct{})
	outcome, err := Wait(ctx, wake, time.Second)
	require.Error(t, err)
	require.Equal(t, TimedOut, outcome)
}
