package apiconfig_test

import (
	"sync"
	"testing"
	"time"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestHostEventRing_SeqMonotonic(t *testing.T) {
	r := apiconfig.NewHostEventRing(16, 7)
	require.Equal(t, uint64(7), r.Generation())
	require.Equal(t, uint64(0), r.Head())

	var prev uint64
	for i := 0; i < 10; i++ {
		ev := r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
		require.Greater(t, ev.Seq, prev)
		require.Equal(t, ev.Seq, r.Head())
		require.NotZero(t, ev.ObservedAtUnix)
		prev = ev.Seq
	}
}

func TestHostEventRing_CursorZeroReplaysRetained(t *testing.T) {
	r := apiconfig.NewHostEventRing(32, 1)
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowSettled})

	got := r.Since(0, 1, []apiconfig.HostEventKind{
		apiconfig.HostEventKindEscrowCreated,
		apiconfig.HostEventKindEscrowSettled,
	})
	require.False(t, got.Reset)
	require.Len(t, got.Events, 2)
	require.Equal(t, uint64(2), got.NextCursor)
}

func TestHostEventRing_LiveFromNowViaHeadCursor(t *testing.T) {
	r := apiconfig.NewHostEventRing(32, 1)
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowSettled})

	// RPC live-from-now: bump to Head() before Since — no retained replay.
	got := r.Since(r.Head(), 1, []apiconfig.HostEventKind{
		apiconfig.HostEventKindEscrowCreated,
		apiconfig.HostEventKindEscrowSettled,
	})
	require.False(t, got.Reset)
	require.Empty(t, got.Events)
	require.Equal(t, uint64(2), got.NextCursor)
}

func TestHostEventRing_SubscribeFilter(t *testing.T) {
	r := apiconfig.NewHostEventRing(32, 1)
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: 1}})
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindMaintenanceScheduled, Maintenance: &apiconfig.MaintenancePayload{ReservationID: 9}})
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowSettled, Escrow: &apiconfig.EscrowPayload{EscrowID: 1}})

	// Catch up from after the first event: only subscribed kinds, next_cursor covers skips.
	got := r.Since(1, 1, []apiconfig.HostEventKind{
		apiconfig.HostEventKindEscrowCreated,
		apiconfig.HostEventKindEscrowSettled,
	})
	require.False(t, got.Reset)
	require.Equal(t, uint64(3), got.NextCursor)
	require.Len(t, got.Events, 1)
	require.Equal(t, apiconfig.HostEventKindEscrowSettled, got.Events[0].Kind)
	require.Equal(t, uint64(3), got.Events[0].Seq)

	got = r.Since(1, 1, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	require.Empty(t, got.Events)
	require.Equal(t, uint64(3), got.NextCursor, "next_cursor covers skipped unsubscribed seqs")
}

func TestHostEventRing_GenerationMismatchReset(t *testing.T) {
	r := apiconfig.NewHostEventRing(16, 42)
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})

	got := r.Since(1, 41, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	require.True(t, got.Reset)
	require.Empty(t, got.Events)
	require.Equal(t, uint64(42), got.Generation)
	require.Equal(t, uint64(1), got.NextCursor)

	got = r.Since(1, 42, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	require.False(t, got.Reset)
}

func TestHostEventRing_CursorAheadOfHeadReset(t *testing.T) {
	r := apiconfig.NewHostEventRing(16, 1)
	got := r.Since(5, 0, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	require.True(t, got.Reset)
	require.Equal(t, uint64(0), got.NextCursor)

	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
	got = r.Since(9, 1, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	require.True(t, got.Reset)
	require.Equal(t, uint64(1), got.NextCursor)
}

func TestHostEventRing_WraparoundDropsOldestWithReset(t *testing.T) {
	r := apiconfig.NewHostEventRing(3, 1)
	for i := 0; i < 5; i++ {
		r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
	}
	// Retained seqs: 3,4,5. Client at cursor=1 missed seq 2 → gap → reset.
	got := r.Since(1, 1, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	require.True(t, got.Reset)
	require.Empty(t, got.Events)
	require.Equal(t, uint64(5), got.NextCursor)

	// Client that already applied through seq 2 can continue from 3.
	got = r.Since(2, 1, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	require.False(t, got.Reset)
	require.Len(t, got.Events, 3)
	require.Equal(t, uint64(3), got.Events[0].Seq)
	require.Equal(t, uint64(5), got.NextCursor)
}

func TestHostEventRing_ManyWrapsRetainWindowInOrder(t *testing.T) {
	const capacity = 4
	r := apiconfig.NewHostEventRing(capacity, 1)
	// Append far more than capacity to rotate the ring many times.
	const total = 50
	for i := 0; i < total; i++ {
		r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
	}

	// Only the last `capacity` seqs are retained, still oldest→newest.
	got := r.Since(uint64(total-capacity), 1, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	require.False(t, got.Reset)
	require.Len(t, got.Events, capacity)
	require.Equal(t, uint64(total), got.NextCursor)
	for i, ev := range got.Events {
		require.Equal(t, uint64(total-capacity+1+i), ev.Seq, "events must be contiguous and in order")
	}

	// Anything older than the retained window is a gap → reset.
	old := r.Since(uint64(total-capacity-1), 1, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	require.True(t, old.Reset)
}

func TestHostEventRing_SubscribeWakesOnMatchingKind(t *testing.T) {
	r := apiconfig.NewHostEventRing(8, 1)
	ch, release := r.Subscribe([]apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	defer release()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Error("Subscribe channel did not close after matching Append")
		}
	}()

	time.Sleep(10 * time.Millisecond)
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
	wg.Wait()

	// A fresh subscription starts open.
	ch2, release2 := r.Subscribe([]apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	defer release2()
	select {
	case <-ch2:
		t.Fatal("fresh Subscribe channel should be open")
	default:
	}
}

func TestHostEventRing_SubscribeIgnoresUnsubscribedKind(t *testing.T) {
	r := apiconfig.NewHostEventRing(8, 1)
	ch, release := r.Subscribe([]apiconfig.HostEventKind{
		apiconfig.HostEventKindEscrowCreated,
		apiconfig.HostEventKindEscrowSettled,
	})
	defer release()

	// Unsubscribed kinds must NOT wake an escrow-only waiter, no matter how many.
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindMaintenanceScheduled})
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindMaintenanceCanceled})
	select {
	case <-ch:
		t.Fatal("maintenance Append must not wake an escrow-only waiter")
	case <-time.After(50 * time.Millisecond):
	}

	// A subscribed kind still wakes it.
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowSettled})
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("subscribed Append must wake the waiter")
	}
}

func TestHostEventRing_ReleaseStopsWaking(t *testing.T) {
	r := apiconfig.NewHostEventRing(8, 1)
	ch, release := r.Subscribe([]apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated})
	release()

	// After release the waiter is deregistered; Append must not touch its channel.
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
	select {
	case <-ch:
		t.Fatal("released waiter must not be woken")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHostEventRing_EmptySubscribeReturnsNoEvents(t *testing.T) {
	r := apiconfig.NewHostEventRing(8, 1)
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
	r.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowSettled})

	got := r.Since(1, 1, nil)
	require.False(t, got.Reset)
	require.Empty(t, got.Events)
	require.Equal(t, uint64(2), got.NextCursor)

	got = r.Since(1, 1, []apiconfig.HostEventKind{})
	require.Empty(t, got.Events)
	require.Equal(t, uint64(2), got.NextCursor)
}
