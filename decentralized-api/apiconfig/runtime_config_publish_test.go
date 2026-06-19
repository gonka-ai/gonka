package apiconfig_test

import (
	"testing"
	"time"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestApplyRuntimeConfigBlockIfChanged_FirstPublish(t *testing.T) {
	cm := newTestConfigManager()
	cm.EnsureRuntimeConfigNotifier()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))

	ch := cm.RuntimeConfigNotifier().NotifyChan()
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 3))

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected notify on first publish")
	}
	require.Equal(t, int64(100), cm.RuntimeParamsBlockHeight())
}

func TestApplyRuntimeConfigBlockIfChanged_NoNotifyWhenUnchanged(t *testing.T) {
	cm := newTestConfigManager()
	cm.EnsureRuntimeConfigNotifier()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 3))

	ch := cm.RuntimeConfigNotifier().NotifyChan()
	require.False(t, cm.ApplyRuntimeConfigBlockIfChanged(101, 3))
	select {
	case <-ch:
		t.Fatal("expected no notify when params and epoch unchanged")
	case <-time.After(50 * time.Millisecond):
	}
	require.Equal(t, int64(100), cm.RuntimeParamsBlockHeight())
}

func TestApplyRuntimeConfigBlockIfChanged_NotifyOnParamChange(t *testing.T) {
	cm := newTestConfigManager()
	cm.EnsureRuntimeConfigNotifier()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 3))

	ch := cm.RuntimeConfigNotifier().NotifyChan()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "full"}))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(101, 3))

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected notify on param change")
	}
	require.Equal(t, int64(101), cm.RuntimeParamsBlockHeight())
}

func TestApplyRuntimeConfigBlockIfChanged_NotifyOnEpochOnly(t *testing.T) {
	cm := newTestConfigManager()
	cm.EnsureRuntimeConfigNotifier()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 3))

	ch := cm.RuntimeConfigNotifier().NotifyChan()
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(105, 4))

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected notify on epoch change")
	}
	require.Equal(t, int64(105), cm.RuntimeParamsBlockHeight())
}

func TestSetRuntimeParamsBlockHeight_DoesNotNotify(t *testing.T) {
	cm := newTestConfigManager()
	cm.EnsureRuntimeConfigNotifier()

	ch := cm.RuntimeConfigNotifier().NotifyChan()
	cm.SetRuntimeParamsBlockHeight(100)
	select {
	case <-ch:
		t.Fatal("SetRuntimeParamsBlockHeight must not notify")
	case <-time.After(50 * time.Millisecond):
	}
}
