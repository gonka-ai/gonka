package apiconfig_test

import (
	"sync"
	"testing"
	"time"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestRuntimeConfigNotifier_NotifyClosesAndReplaces(t *testing.T) {
	n := apiconfig.NewRuntimeConfigNotifier()
	ch1 := n.NotifyChan()
	require.NotNil(t, ch1)

	n.Notify()

	select {
	case <-ch1:
	default:
		t.Fatal("expected ch1 to be closed after Notify")
	}

	ch2 := n.NotifyChan()
	require.NotNil(t, ch2)

	// Fresh channel after Notify: open (ch1 was closed above).
	select {
	case <-ch2:
		t.Fatal("expected new NotifyChan to be open after Notify")
	default:
	}
}

func TestRuntimeConfigNotifier_ConcurrentNotifyWait(t *testing.T) {
	n := apiconfig.NewRuntimeConfigNotifier()
	const waiters = 32
	const notifies = 16

	var wg sync.WaitGroup
	wg.Add(waiters + notifies)

	for i := 0; i < waiters; i++ {
		go func() {
			defer wg.Done()
			ch := n.NotifyChan()
			<-ch
		}()
	}

	time.Sleep(10 * time.Millisecond)

	for i := 0; i < notifies; i++ {
		go func() {
			defer wg.Done()
			n.Notify()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock in concurrent notify/wait")
	}
}

