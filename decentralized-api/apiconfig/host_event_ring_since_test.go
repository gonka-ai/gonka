package apiconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostEventRing_SinceSkipsPrefix(t *testing.T) {
	const n = 4096
	r := NewHostEventRing(n, 1)
	for i := 0; i < n+n/2; i++ {
		kind := HostEventKindEscrowCreated
		if i%2 == 1 {
			kind = HostEventKindBlock
		}
		r.Append(HostEvent{Kind: kind})
	}

	cursor := r.Head() - 1
	got := r.Since(cursor, 1, []HostEventKind{HostEventKindEscrowCreated, HostEventKindBlock})
	require.False(t, got.Reset)
	require.Len(t, got.Events, 1)
	require.Equal(t, r.Head(), got.Events[0].Seq)
	require.Less(t, r.sinceSlots, 64)

	mid := r.Head() - uint64(n)/2
	got = r.Since(mid, 1, []HostEventKind{HostEventKindBlock})
	require.False(t, got.Reset)
	require.NotEmpty(t, got.Events)
	require.Greater(t, got.Events[0].Seq, mid)
	require.Equal(t, r.Head(), got.Events[len(got.Events)-1].Seq)
	for _, ev := range got.Events {
		require.Equal(t, HostEventKindBlock, ev.Kind)
		require.Greater(t, ev.Seq, mid)
	}
}

func TestHostEventRing_FirstAfterMatchesLinear(t *testing.T) {
	r := NewHostEventRing(8, 1)
	for i := 0; i < 20; i++ {
		kind := HostEventKindEscrowCreated
		if i%3 == 0 {
			kind = HostEventKindBlock
		}
		r.Append(HostEvent{Kind: kind})
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEqual(t, 0, r.start)
	for cursor := uint64(0); cursor <= r.headLocked()+1; cursor++ {
		got := r.firstAfterLocked(cursor)
		want := 0
		for want < r.size && r.buf[(r.start+want)%r.capacity].Seq <= cursor {
			want++
		}
		require.Equal(t, want, got, "cursor %d", cursor)
	}
}
