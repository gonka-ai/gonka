package apiconfig_test

import (
	"sync/atomic"
	"testing"
	"time"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestConfigManager_SetEpochChangeHandler_FiresOnEpochTransition(t *testing.T) {
	cm := newTestConfigManager()
	cm.EnsureRuntimeConfigNotifier()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))

	var fires atomic.Int32
	cm.SetEpochChangeHandler(func(old, new uint64) {
		if old == 3 && new == 4 {
			fires.Add(1)
		}
	})

	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 3))
	require.Equal(t, int32(0), fires.Load(), "initial publish must not fire handler")

	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(101, 4))
	require.Equal(t, int32(1), fires.Load())

	require.False(t, cm.ApplyRuntimeConfigBlockIfChanged(102, 4))
	require.Equal(t, int32(1), fires.Load(), "param-only publish must not fire handler")
}

func TestApplyRuntimeConfigBlockIfChanged_EpochHandlerMayReadSnapshot(t *testing.T) {
	cm := newTestConfigManager()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))

	done := make(chan struct{})
	cm.SetEpochChangeHandler(func(_, newEpoch uint64) {
		snap := cm.RuntimeConfigSnapshot(newEpoch)
		require.Equal(t, newEpoch, snap.CurrentEpochID)
		close(done)
	})

	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 1))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(101, 2))

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("epoch handler blocked: likely deadlock reading RuntimeConfigSnapshot under publish lock")
	}
}
