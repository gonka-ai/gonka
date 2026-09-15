package loadtest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devshard/testenv/config"
	"github.com/stretchr/testify/require"
)

func TestConfigureParticipants_ThreeParticipants(t *testing.T) {
	cfg := &config.File{Hosts: []config.HostCfg{
		{ID: "versiond-0"},
		{ID: "versiond-1"},
		{ID: "versiond-2"},
	}}

	configureParticipants(cfg, 3)

	require.Equal(t, 3, cfg.Escrow.Slots)
	require.Len(t, cfg.Hosts, 4)
	require.Equal(t, "versiond-0", cfg.Hosts[0].KeyName)
	require.Equal(t, "versiond-0", cfg.Hosts[1].KeyName)
	require.Equal(t, "versiond-2", cfg.Hosts[2].KeyName)
	require.Equal(t, "versiond-3", cfg.Hosts[3].KeyName)
}

func TestIsAddressInUse(t *testing.T) {
	require.True(t, isAddressInUse(errors.New("failed to set up container networking: Address already in use")))
	require.False(t, isAddressInUse(errors.New("container is unhealthy")))
	require.False(t, isAddressInUse(nil))
}

func TestStripStaticNetworking(t *testing.T) {
	compose := `networks:
  testenv:
    driver: bridge
    ipam:
      config:
        - subnet: 172.31.42.0/24
services:
  mock-chain:
    networks:
      testenv:
        ipv4_address: 172.31.42.2
`

	actual := stripStaticNetworking(compose)
	require.NotContains(t, actual, "ipam:")
	require.NotContains(t, actual, "subnet:")
	require.NotContains(t, actual, "ipv4_address:")
	require.Contains(t, actual, "driver: bridge")
}

func TestFormatStatusCounts(t *testing.T) {
	require.Equal(t, "finished=3, started=1", formatStatusCounts(map[string]int{
		"started":  1,
		"finished": 3,
	}))
	require.Equal(t, "unavailable", formatStatusCounts(nil))
}

func TestWaitForFinishedInferences_AllowsLoggedGhostPendingWithinThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"finished"},"2":{"status":"pending"}}}`)
	}))
	defer server.Close()

	err := waitForFinishedInferences(context.Background(), server.URL, "", 1, 0.5, map[string]struct{}{"2": {}}, time.Second)
	require.NoError(t, err)
}

func TestWaitForFinishedInferences_RejectsGhostRateAboveThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"inferences":{"1":{"status":"finished"},"2":{"status":"pending"}}}`)
	}))
	defer server.Close()

	err := waitForFinishedInferences(context.Background(), server.URL, "", 1, 0.25, map[string]struct{}{"2": {}}, time.Second)
	require.ErrorContains(t, err, "ghost inference rate 0.5000")
}

func TestReadGhostInferenceIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.log")
	require.NoError(t, os.WriteFile(path, []byte("devshardctl | request=r1 stage=ghost_probe_skipped escrow=1 nonce=9 reason=no_compatible_request_after_stale\n"), 0o644))

	ghosts, err := readGhostInferenceIDs(path)
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{"9": {}}, ghosts)
}
