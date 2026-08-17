package longpoll_test

import (
	"context"
	"testing"
	"time"

	"decentralized-api/internal/longpoll"

	"github.com/stretchr/testify/require"
)

func TestClampMaxWait_ZeroIsImmediate(t *testing.T) {
	require.Equal(t, time.Duration(0), longpoll.ClampMaxWait(0, longpoll.DefaultMaxWaitCap))
	require.Equal(t, time.Duration(0), longpoll.ClampMaxWait(-1, longpoll.DefaultMaxWaitCap))
}

func TestClampMaxWait_PositiveCapped(t *testing.T) {
	require.Equal(t, 2*time.Second, longpoll.ClampMaxWait(600, 2*time.Second))
	require.Equal(t, time.Second, longpoll.ClampMaxWait(1, 60*time.Second))
	require.Equal(t, 60*time.Second, longpoll.ClampMaxWait(600, 0)) // default cap
}

func TestWait_Notified(t *testing.T) {
	ch := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(ch)
	}()
	out, err := longpoll.Wait(context.Background(), ch, time.Second)
	require.NoError(t, err)
	require.Equal(t, longpoll.Notified, out)
}

func TestWait_TimedOut(t *testing.T) {
	ch := make(chan struct{})
	start := time.Now()
	out, err := longpoll.Wait(context.Background(), ch, 50*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, longpoll.TimedOut, out)
	require.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond)
}

func TestWait_Canceled(t *testing.T) {
	ch := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := longpoll.Wait(ctx, ch, time.Second)
	require.ErrorIs(t, err, context.Canceled)
}
