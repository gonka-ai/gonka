package devshard

import (
	"sync"
	"sync/atomic"
	"testing"

	"decentralized-api/apiconfig"

	"devshard/runtimeconfig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubSnapshotSource is a deterministic RuntimeConfigSnapshotSource for the
// devshardd-standalone provider tests. Snapshot writes are guarded so the
// concurrent-read sub-test can swap in fresh values mid-flight.
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
		DefaultSealGraceNonces:            55,
		DefaultInferenceClearGraceSeconds: 90,
		MaxNonce:                          2000,
	})

	p := ConfigManagerRuntimeParams(cm)
	got := p.SessionParams()

	assert.Equal(t, uint32(55), got.SealGraceNonces)
	assert.Equal(t, uint32(90), got.InferenceClearGraceSeconds)
	assert.Equal(t, uint32(2000), got.MaxNonce)
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
		DefaultSealGraceNonces:            1,
		DefaultInferenceClearGraceSeconds: 1,
		MaxNonce:                          1,
		RefusalTimeout:                    90,
		ExecutionTimeout:                    1800,
		ValidationRate:                    4000,
		VoteThresholdFactor:               67,
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
		DefaultSealGraceNonces:            10,
		DefaultInferenceClearGraceSeconds: 11,
		MaxNonce:                          12,
	})
	first := p.SessionParams()
	assert.Equal(t, uint32(10), first.SealGraceNonces)
	assert.Equal(t, uint32(11), first.InferenceClearGraceSeconds)
	assert.Equal(t, uint32(12), first.MaxNonce)

	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		DefaultSealGraceNonces:            20,
		DefaultInferenceClearGraceSeconds: 21,
		MaxNonce:                          22,
	})
	second := p.SessionParams()
	assert.Equal(t, uint32(20), second.SealGraceNonces)
	assert.Equal(t, uint32(21), second.InferenceClearGraceSeconds)
	assert.Equal(t, uint32(22), second.MaxNonce)
}

func TestRuntimeConfigRuntimeParams_FromSnapshot(t *testing.T) {
	src := &stubSnapshotSource{}
	src.set(runtimeconfig.Snapshot{
		DefaultSealGraceNonces:            33,
		DefaultInferenceClearGraceSeconds: 44,
		MaxNonce:                          555,
	})

	got := RuntimeConfigRuntimeParams(src).SessionParams()

	assert.Equal(t, uint32(33), got.SealGraceNonces)
	assert.Equal(t, uint32(44), got.InferenceClearGraceSeconds)
	assert.Equal(t, uint32(555), got.MaxNonce)
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
	const (
		readers    = 16
		iterations = 500
	)

	t.Run("config manager", func(t *testing.T) {
		cm := &apiconfig.ConfigManager{}
		cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
			DefaultSealGraceNonces:            7,
			DefaultInferenceClearGraceSeconds: 8,
			MaxNonce:                          9,
		})
		p := ConfigManagerRuntimeParams(cm)
		runConcurrent(t, readers, iterations, func() SessionParams { return p.SessionParams() })
	})

	t.Run("runtime config snapshot", func(t *testing.T) {
		src := &stubSnapshotSource{}
		src.set(runtimeconfig.Snapshot{
			DefaultSealGraceNonces:            7,
			DefaultInferenceClearGraceSeconds: 8,
			MaxNonce:                          9,
		})
		p := RuntimeConfigRuntimeParams(src)
		runConcurrent(t, readers, iterations, func() SessionParams { return p.SessionParams() })
	})
}

// runConcurrent fan-outs readers calls of fn and asserts every read returned
// the same value. Detects torn reads via go test -race.
func runConcurrent(t *testing.T, readers, iterations int, fn func() SessionParams) {
	t.Helper()

	want := fn()
	var fails atomic.Int64
	var wg sync.WaitGroup
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if got := fn(); got != want {
					fails.Add(1)
					return
				}
			}
		}()
	}
	wg.Wait()
	require.Zero(t, fails.Load(), "concurrent SessionParams reads diverged")
}

// TestHostManager_SetRuntimeParamsProvider_NoBehaviorChange guards Phase 2's
// "captured but not consulted" contract: wiring the provider in dapi/devshardd
// must not change today's bind path. Phase 5 turns this into a real
// freeze-at-bind assertion.
func TestHostManager_SetRuntimeParamsProvider_NoBehaviorChange(t *testing.T) {
	m := &HostManager{}
	require.Nil(t, m.params)

	p := ConfigManagerRuntimeParams(&apiconfig.ConfigManager{})
	m.SetRuntimeParamsProvider(p)
	require.NotNil(t, m.params, "setter must store the provider for phase 5")

	// Sanity: replacing the provider is allowed (governance updates pick a new
	// cache snapshot via the same long-poll; the manager just reads through it).
	other := ConfigManagerRuntimeParams(&apiconfig.ConfigManager{})
	m.SetRuntimeParamsProvider(other)
	require.NotNil(t, m.params)
}
