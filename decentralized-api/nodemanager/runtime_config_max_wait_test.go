package nodemanager

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClampMaxWait_ZeroIsImmediate(t *testing.T) {
	require.Equal(t, time.Duration(0), clampMaxWait(0))
	require.Equal(t, time.Duration(0), clampMaxWait(-1))
}

func TestClampMaxWait_PositiveCappedByEnv(t *testing.T) {
	t.Setenv("DAPI_RUNTIME_CONFIG_MAX_WAIT_SECONDS", "2")
	require.Equal(t, 2*time.Second, clampMaxWait(600))
	require.Equal(t, time.Second, clampMaxWait(1))
}

func TestRuntimeConfigMaxWaitCap_Default(t *testing.T) {
	os.Unsetenv("DAPI_RUNTIME_CONFIG_MAX_WAIT_SECONDS")
	require.Equal(t, defaultRuntimeConfigMaxWaitCap, runtimeConfigMaxWaitCap())
}
