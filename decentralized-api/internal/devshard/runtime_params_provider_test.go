package devshard

import (
	"sync"
	"testing"

	"devshard/runtimeconfig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubSnapshotSource struct {
	mu   sync.RWMutex
	snap runtimeconfig.Snapshot
}

func (s *stubSnapshotSource) set(snap runtimeconfig.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = snap
}

func (s *stubSnapshotSource) Snapshot() runtimeconfig.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}

func TestRuntimeConfigRuntimeParams_FromSnapshot(t *testing.T) {
	src := &stubSnapshotSource{}
	src.set(runtimeconfig.Snapshot{
		MaxNonce:            33,
		ValidationRate:      6000,
		VoteThresholdFactor: 50,
		RefusalTimeout:      60,
		ExecutionTimeout:    1200,
	})

	got := RuntimeConfigRuntimeParams(src).SessionParams()

	assert.Equal(t, uint32(33), got.MaxNonce)
	assert.Equal(t, uint32(6000), got.ValidationRate)
	assert.Equal(t, uint32(50), got.VoteThresholdFactor)
	assert.Equal(t, int64(60), got.RefusalTimeout)
	assert.Equal(t, int64(1200), got.ExecutionTimeout)
}

func TestRuntimeConfigRuntimeParams_ZeroSnapshotZeroFields(t *testing.T) {
	src := &stubSnapshotSource{}

	got := RuntimeConfigRuntimeParams(src).SessionParams()

	assert.Equal(t, SessionParams{}, got)
}

func TestRuntimeConfigRuntimeParams_ReflectsLatestSnapshot(t *testing.T) {
	src := &stubSnapshotSource{}
	p := RuntimeConfigRuntimeParams(src)

	src.set(runtimeconfig.Snapshot{MaxNonce: 1})
	require.Equal(t, uint32(1), p.SessionParams().MaxNonce)

	src.set(runtimeconfig.Snapshot{MaxNonce: 9999})
	require.Equal(t, uint32(9999), p.SessionParams().MaxNonce, "snapshot updates must propagate on the next read")
}

func TestRuntimeParamsProvider_ConcurrentReadsAreSafe(t *testing.T) {
	const readers = 8
	const iterations = 200

	src := &stubSnapshotSource{}
	src.set(runtimeconfig.Snapshot{
		MaxNonce: 7,
	})
	p := RuntimeConfigRuntimeParams(src)
	runConcurrent(t, readers, iterations, func() SessionParams { return p.SessionParams() })
}

func runConcurrent(t *testing.T, readers, iterations int, fn func() SessionParams) {
	t.Helper()
	var fails sync.Map
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			var baseline SessionParams
			for j := 0; j < iterations; j++ {
				got := fn()
				if j == 0 {
					baseline = got
					continue
				}
				if got != baseline {
					fails.Store(seed, got)
				}
			}
		}(i)
	}
	wg.Wait()
	var count int
	fails.Range(func(_, _ any) bool {
		count++
		return true
	})
	require.Zero(t, count, "concurrent SessionParams reads diverged")
}

// TestHostManager_SetRuntimeParamsProvider_NoBehaviorChange guards Phase 2's
// optional wiring: attaching a provider must not change behavior when create
// is never called.
func TestHostManager_SetRuntimeParamsProvider_NoBehaviorChange(t *testing.T) {
	m := &HostManager{}

	src := &stubSnapshotSource{}
	p := RuntimeConfigRuntimeParams(src)
	m.SetRuntimeParamsProvider(p)
	require.NotNil(t, m.params)

	other := RuntimeConfigRuntimeParams(src)
	m.SetRuntimeParamsProvider(other)
	require.NotNil(t, m.params)
}
