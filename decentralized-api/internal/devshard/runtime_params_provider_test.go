package devshard

import (
	"sync"
	"testing"

	"decentralized-api/apiconfig"
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

func TestConfigManagerRuntimeParams_FromPopulatedCache(t *testing.T) {
	cm := &apiconfig.ConfigManager{}
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		MaxNonce:              2000,
		ValidationRate:        5000,
		VoteThresholdFactor:   50,
		RefusalTimeout:        60,
		ExecutionTimeout:      1200,
	})

	p := ConfigManagerRuntimeParams(cm)
	got := p.SessionParams()

	assert.Equal(t, uint32(2000), got.MaxNonce)
	assert.Equal(t, uint32(5000), got.ValidationRate)
	assert.Equal(t, uint32(50), got.VoteThresholdFactor)
	assert.Equal(t, int64(60), got.RefusalTimeout)
	assert.Equal(t, int64(1200), got.ExecutionTimeout)
}

func TestConfigManagerRuntimeParams_EmptyCacheZeroFields(t *testing.T) {
	cm := &apiconfig.ConfigManager{}

	p := ConfigManagerRuntimeParams(cm)
	got := p.SessionParams()

	assert.Equal(t, SessionParams{}, got, "provider must not invent defaults; zero cache → zero params")
}

func TestConfigManagerRuntimeParams_Phase4FieldsFromCache(t *testing.T) {
	cm := &apiconfig.ConfigManager{}
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		MaxNonce:            1,
		RefusalTimeout:      90,
		ExecutionTimeout:    1800,
		ValidationRate:        4000,
		VoteThresholdFactor: 67,
	})

	got := ConfigManagerRuntimeParams(cm).SessionParams()

	assert.Equal(t, int64(90), got.RefusalTimeout)
	assert.Equal(t, int64(1800), got.ExecutionTimeout)
	assert.Equal(t, uint32(4000), got.ValidationRate)
	assert.Equal(t, uint32(67), got.VoteThresholdFactor)
}

func TestConfigManagerRuntimeParams_ReflectsUpdate(t *testing.T) {
	cm := &apiconfig.ConfigManager{}
	p := ConfigManagerRuntimeParams(cm)

	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		MaxNonce:         12,
		ValidationRate:   1000,
	})
	first := p.SessionParams()
	assert.Equal(t, uint32(12), first.MaxNonce)
	assert.Equal(t, uint32(1000), first.ValidationRate)

	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		MaxNonce:       22,
		ValidationRate: 2000,
	})
	second := p.SessionParams()
	assert.Equal(t, uint32(22), second.MaxNonce)
	assert.Equal(t, uint32(2000), second.ValidationRate)
}

func TestRuntimeConfigRuntimeParams_FromSnapshot(t *testing.T) {
	src := &stubSnapshotSource{}
	src.set(runtimeconfig.Snapshot{
		MaxNonce:            33,
		ValidationRate:      6000,
		VoteThresholdFactor: 50,
	})

	got := RuntimeConfigRuntimeParams(src).SessionParams()

	assert.Equal(t, uint32(33), got.MaxNonce)
	assert.Equal(t, uint32(6000), got.ValidationRate)
	assert.Equal(t, uint32(50), got.VoteThresholdFactor)
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

	t.Run("configManager", func(t *testing.T) {
		cm := &apiconfig.ConfigManager{}
		cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
			MaxNonce: 7,
		})
		p := ConfigManagerRuntimeParams(cm)
		runConcurrent(t, readers, iterations, func() SessionParams { return p.SessionParams() })
	})

	t.Run("runtimeConfig", func(t *testing.T) {
		src := &stubSnapshotSource{}
		src.set(runtimeconfig.Snapshot{
			MaxNonce: 7,
		})
		p := RuntimeConfigRuntimeParams(src)
		runConcurrent(t, readers, iterations, func() SessionParams { return p.SessionParams() })
	})
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

	p := ConfigManagerRuntimeParams(&apiconfig.ConfigManager{})
	m.SetRuntimeParamsProvider(p)
	require.NotNil(t, m.params)

	other := ConfigManagerRuntimeParams(&apiconfig.ConfigManager{})
	m.SetRuntimeParamsProvider(other)
	require.NotNil(t, m.params)
}
