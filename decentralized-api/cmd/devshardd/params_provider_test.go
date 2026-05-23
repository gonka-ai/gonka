package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	devshardpkg "devshard"
	"devshard/mlnode"
	"devshard/runtimeconfig"
	"devshard/runtimeconfig/testserver"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDevshardd_AlwaysUsesRuntimeConfigProvider(t *testing.T) {
	t.Setenv("DEVSHARDD_RUNTIME_CONFIG_MAX_WAIT_SECONDS", "45")
	t.Setenv("DEVSHARDD_RUNTIME_CONFIG_CLIENT_DEADLINE_SLACK_SECONDS", "7")

	maxWait, slack := runtimeConfigSettingsFromEnv()
	assert.Equal(t, 45*time.Second, maxWait)
	assert.Equal(t, 7*time.Second, slack)

	srv := testserver.New()
	srv.SetHandlers(testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(10, 2, "raw")))
	mlClient := mlnode.ClientForTest(testserver.Dial(t, srv))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := newParamsProvider(ctx, nil, mlClient, nil)
	require.NoError(t, err)
	require.NotNil(t, result.RegisterEpochPrune)

	_, isRuntime := result.Provider.(runtimeconfig.Provider)
	assert.True(t, isRuntime, "expected runtimeconfig.Provider (mandatory dapi path)")

	snap := waitRuntimeSnapshot(t, result.Provider.(runtimeconfig.Provider), 10)
	assert.Equal(t, uint64(2), snap.CurrentEpochID)
}

func waitRuntimeSnapshot(t *testing.T, p runtimeconfig.Provider, height int64) runtimeconfig.Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := p.Snapshot()
		if s.ParamsBlockHeight >= height {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for params_block_height >= %d (got %d)", height, p.Snapshot().ParamsBlockHeight)
	return runtimeconfig.Snapshot{}
}

func TestDevshardd_ProviderRecordsAvailabilityOnApply(t *testing.T) {
	srv := testserver.New()
	srv.SetHandlers(testserver.FullConfig(runtimeconfig.TestRuntimeConfigProto(5, 1, "raw")))
	mlClient := mlnode.ClientForTest(testserver.Dial(t, srv))

	tracker := devshardpkg.NewAvailabilityTracker(true, 0, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := newParamsProvider(ctx, nil, mlClient, tracker)
	require.NoError(t, err)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		avail := tracker.CurrentAvailability()
		if avail.Enabled && avail.EpochID == 1 && avail.Time > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: availability=%+v", tracker.CurrentAvailability())
}

func TestDevshardd_NoChainParamsProvider(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	mainPath := filepath.Join(filepath.Dir(filename), "main.go")
	body, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	s := string(body)
	assert.NotContains(t, s, "chainParamsProvider")
	assert.NotContains(t, s, "newChainParamsProvider")
	assert.NotContains(t, s, "QueryParamsRequest")
	assert.NotContains(t, s, "QueryEpochInfoRequest")
	// Legacy 60s ticker refresh path must stay deleted (Step 8).
	assert.False(t, strings.Contains(s, "time.NewTicker(60 * time.Second)"),
		"unexpected 60s chain params ticker in main.go")
}

func TestRuntimeConfigSettingsFromEnv_Defaults(t *testing.T) {
	t.Setenv("DEVSHARDD_RUNTIME_CONFIG_MAX_WAIT_SECONDS", "")
	t.Setenv("DEVSHARDD_RUNTIME_CONFIG_CLIENT_DEADLINE_SLACK_SECONDS", "")

	maxWait, slack := runtimeConfigSettingsFromEnv()
	assert.Equal(t, 60*time.Second, maxWait)
	assert.Equal(t, 5*time.Second, slack)
}
