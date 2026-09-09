package runtimeconfig

import (
	"context"
	"sync"
	"testing"
	"time"

	"common/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeSource is a concurrency-safe SnapshotSource backed by a single snapshot
// plus a fan-out notifier, mirroring the dapi ConfigManager contract.
type fakeSource struct {
	mu   sync.Mutex
	snap Snapshot
	ch   chan struct{}
}

func newFakeSource(snap Snapshot) *fakeSource {
	return &fakeSource{snap: snap, ch: make(chan struct{})}
}

func (f *fakeSource) RuntimeConfigSnapshot(epochID uint64) Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snap
	s.CurrentEpochID = epochID
	return s
}

func (f *fakeSource) NotifyChan() (<-chan struct{}, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ch, true
}

// publish advances the snapshot to height and wakes waiters (Notify semantics).
func (f *fakeSource) publish(height int64) {
	f.mu.Lock()
	f.snap.ParamsBlockHeight = height
	close(f.ch)
	f.ch = make(chan struct{})
	f.mu.Unlock()
}

type fixedEpoch uint64

func (e fixedEpoch) CurrentEpochID() uint64 { return uint64(e) }

func newTestServer(src *fakeSource, epoch EpochSource) *Server {
	return NewServer(ServerDeps{
		Source:     src,
		Epochs:     epoch,
		Notifier:   src,
		MaxWaitCap: func() time.Duration { return 60 * time.Second },
	})
}

func TestServer_FullResponseWhenClientZero(t *testing.T) {
	src := newFakeSource(Snapshot{ParamsBlockHeight: 100, LogprobsMode: "full", DevshardRequestsEnabled: true})
	srv := newTestServer(src, fixedEpoch(7))

	resp, err := srv.Handle(context.Background(), &gen.GetRuntimeConfigRequest{ClientParamsBlockHeight: 0})
	require.NoError(t, err)
	require.False(t, resp.Unchanged)
	require.NotNil(t, resp.Config)
	require.Equal(t, int64(100), resp.Config.ParamsBlockHeight)
	require.Equal(t, uint64(7), resp.Config.CurrentEpochId)
	require.Equal(t, "full", resp.Config.LogprobsMode)
}

func TestServer_FullResponseWhenBehind(t *testing.T) {
	src := newFakeSource(Snapshot{ParamsBlockHeight: 100})
	srv := newTestServer(src, fixedEpoch(1))

	start := time.Now()
	resp, err := srv.Handle(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 50,
		MaxWaitSeconds:          10,
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 100*time.Millisecond)
	require.False(t, resp.Unchanged)
	require.Equal(t, int64(100), resp.Config.ParamsBlockHeight)
}

func TestServer_ImmediateUnchangedWhenCaughtUp(t *testing.T) {
	src := newFakeSource(Snapshot{ParamsBlockHeight: 100})
	srv := newTestServer(src, fixedEpoch(1))

	start := time.Now()
	resp, err := srv.Handle(context.Background(), &gen.GetRuntimeConfigRequest{ClientParamsBlockHeight: 100})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 50*time.Millisecond)
	require.True(t, resp.Unchanged)
	require.Nil(t, resp.Config)
}

func TestServer_NegativeMaxWaitTreatedAsZero(t *testing.T) {
	src := newFakeSource(Snapshot{ParamsBlockHeight: 100})
	srv := newTestServer(src, fixedEpoch(1))

	start := time.Now()
	resp, err := srv.Handle(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
		MaxWaitSeconds:          -1,
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 50*time.Millisecond)
	require.True(t, resp.Unchanged)
}

func TestServer_LongPollReturnsOnNotify(t *testing.T) {
	src := newFakeSource(Snapshot{ParamsBlockHeight: 100})
	srv := newTestServer(src, fixedEpoch(1))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan *gen.GetRuntimeConfigResponse, 1)
	go func() {
		resp, err := srv.Handle(ctx, &gen.GetRuntimeConfigRequest{
			ClientParamsBlockHeight: 100,
			MaxWaitSeconds:          2,
		})
		require.NoError(t, err)
		done <- resp
	}()

	time.Sleep(50 * time.Millisecond)
	src.publish(101)

	select {
	case resp := <-done:
		require.False(t, resp.Unchanged)
		require.Equal(t, int64(101), resp.Config.ParamsBlockHeight)
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("expected return on notify before max_wait")
	}
}

func TestServer_LongPollTimesOut(t *testing.T) {
	src := newFakeSource(Snapshot{ParamsBlockHeight: 100})
	srv := newTestServer(src, fixedEpoch(1))

	start := time.Now()
	resp, err := srv.Handle(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
		MaxWaitSeconds:          1,
	})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.True(t, resp.Unchanged)
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
	require.Less(t, elapsed, 3*time.Second)
}

func TestServer_LongPollContextCancel(t *testing.T) {
	src := newFakeSource(Snapshot{ParamsBlockHeight: 100})
	srv := newTestServer(src, fixedEpoch(1))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := srv.Handle(ctx, &gen.GetRuntimeConfigRequest{
			ClientParamsBlockHeight: 100,
			MaxWaitSeconds:          30,
		})
		done <- err
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(2 * time.Second):
		t.Fatal("expected cancel to unblock")
	}
}

func TestServer_BroadcastWakesAllWaiters(t *testing.T) {
	src := newFakeSource(Snapshot{ParamsBlockHeight: 100})
	srv := newTestServer(src, fixedEpoch(1))

	const n = 8
	var wg sync.WaitGroup
	results := make([]*gen.GetRuntimeConfigResponse, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = srv.Handle(context.Background(), &gen.GetRuntimeConfigRequest{
				ClientParamsBlockHeight: 100,
				MaxWaitSeconds:          5,
			})
		}(i)
	}

	time.Sleep(50 * time.Millisecond)
	src.publish(101)

	wg.Wait()
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.False(t, results[i].Unchanged)
		require.Equal(t, int64(101), results[i].Config.ParamsBlockHeight)
	}
}

func TestServer_MaxWaitClampedToCap(t *testing.T) {
	src := newFakeSource(Snapshot{ParamsBlockHeight: 100})
	srv := NewServer(ServerDeps{
		Source:     src,
		Epochs:     fixedEpoch(1),
		Notifier:   src,
		MaxWaitCap: func() time.Duration { return time.Second },
	})

	start := time.Now()
	resp, err := srv.Handle(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
		MaxWaitSeconds:          600,
	})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.True(t, resp.Unchanged)
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
	require.Less(t, elapsed, 5*time.Second)
}

func TestServer_LongPollWithoutNotifierFails(t *testing.T) {
	src := newFakeSource(Snapshot{ParamsBlockHeight: 100})
	srv := NewServer(ServerDeps{Source: src, Epochs: fixedEpoch(1)}) // no Notifier

	_, err := srv.Handle(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
		MaxWaitSeconds:          5,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestClampMaxWait(t *testing.T) {
	require.Equal(t, time.Duration(0), ClampMaxWait(0, time.Minute))
	require.Equal(t, time.Duration(0), ClampMaxWait(-5, time.Minute))
	require.Equal(t, 2*time.Second, ClampMaxWait(2, time.Minute))
	require.Equal(t, time.Minute, ClampMaxWait(600, time.Minute))
	require.Equal(t, DefaultMaxWaitCap, ClampMaxWait(600, 0)) // zero cap -> default
}
