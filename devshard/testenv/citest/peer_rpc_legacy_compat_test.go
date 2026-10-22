//go:build testenvci

package citest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const (
	grpcLegacyCompatEnv = "TESTENV_GRPC_LEGACY_COMPAT"
	mixedChildBinaryEnv = "TESTENV_MIXED_CHILD_BINARY"
	legacyVersiondImage = "devshard-versiond:0.2.15-v5"
	legacyRouterImage   = "devshard-versiond-router:0.2.15-v5"
)

type peerRPCCompatibilityCase struct {
	name           string
	legacyRouter   bool
	legacyVersiond bool
	legacyChild    bool
	requestLabel   string
}

var peerRPCVersiondCompatibilityCases = []peerRPCCompatibilityCase{
	{
		name:         "current_versiond_legacy_child",
		legacyChild:  true,
		requestLabel: "current-versiond-legacy-child",
	},
	{
		name:           "legacy_versiond_current_child",
		legacyVersiond: true,
		requestLabel:   "legacy-versiond-current-child",
	},
	{
		name:           "legacy_versiond_legacy_child",
		legacyVersiond: true,
		legacyChild:    true,
		requestLabel:   "legacy-versiond-legacy-child",
	},
}

var peerRPCRouterCompatibilityCases = []peerRPCCompatibilityCase{
	{
		name:           "current_router_legacy_versiond",
		legacyVersiond: true,
		legacyChild:    true,
		requestLabel:   "current-router-legacy-versiond",
	},
	{
		name:         "legacy_router_current_versiond",
		legacyRouter: true,
		requestLabel: "legacy-router-current-versiond",
	},
	{
		name:           "current_router_mixed_versiond",
		legacyVersiond: true,
		requestLabel:   "current-router-mixed-versiond",
	},
	{
		name:         "legacy_router_mixed_children",
		legacyRouter: true,
		legacyChild:  true,
		requestLabel: "legacy-router-mixed-children",
	},
}

// TestPeerRPCVersiondCompatibilityMatrix exercises legacy versiond/child
// combinations through the current native-gRPC router. Every subtest creates a
// fresh stack and forces traffic through versiond-0 and versiond-1 separately,
// so the current/current control host cannot hide a compatibility failure on
// versiond-0.
//
// The historical child path is supplied by TESTENV_MIXED_CHILD_BINARY. The
// baseline versiond image is pinned to versiond-0 for legacy-versiond cases;
// versiond-1 and the router remain current.
func TestPeerRPCVersiondCompatibilityMatrix(t *testing.T) {
	legacySrc := requirePeerRPCCompatibilityEnv(t)
	runPeerRPCCompatibilityCases(t, peerRPCVersiondCompatibilityCases, legacySrc)
}

// TestPeerRPCRouterCompatibilityMatrix exercises the four router/versiond
// combinations relevant during rollout: current router to legacy versiond,
// legacy router to current versiond, current router to a mixed versiond pair,
// and legacy router to mixed children. It runs under the same Make target as
// TestPeerRPCVersiondCompatibilityMatrix; there is no separate trigger.
func TestPeerRPCRouterCompatibilityMatrix(t *testing.T) {
	legacySrc := requirePeerRPCCompatibilityEnv(t)
	runPeerRPCCompatibilityCases(t, peerRPCRouterCompatibilityCases, legacySrc)
}

func requirePeerRPCCompatibilityEnv(t *testing.T) string {
	t.Helper()
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.SkipUnlessEnv(t, grpcLegacyCompatEnv)
	requireNoProxyGRPC(t)
	if os.Getenv(harness.EnvVersiondImage) != "" || os.Getenv(harness.EnvVersiondRouterImage) != "" {
		t.Fatal("cross-version compatibility matrix requires current router images and per-case versiond pinning")
	}
	harness.RequireDocker(t)

	legacySrc := strings.TrimSpace(os.Getenv(mixedChildBinaryEnv))
	info, err := os.Stat(legacySrc)
	require.NoError(t, err, "%s must point to a pre-capability devshardd", mixedChildBinaryEnv)
	require.False(t, info.IsDir(), "%s points to a directory", mixedChildBinaryEnv)
	return legacySrc
}

func runPeerRPCCompatibilityCases(t *testing.T, cases []peerRPCCompatibilityCase, legacySrc string) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runPeerRPCCompatibilityCase(t, tc, legacySrc)
		})
	}
}

func runPeerRPCCompatibilityCase(t *testing.T, tc peerRPCCompatibilityCase, legacySrc string) {
	t.Helper()
	stack := harness.NewStack(t, "citest-grpc-compat-"+tc.name+"-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2)

	if tc.legacyVersiond {
		harness.PinVersiondServiceImage(t, stack.ComposePath, "versiond-0", legacyVersiondImage)
	}
	if tc.legacyRouter {
		harness.PinVersiondRouterServiceImage(t, stack.ComposePath, legacyRouterImage)
	}
	if tc.legacyChild {
		// Keep the old binary inside the generated workdir so Docker Desktop can
		// mount it just like the normal current child, but only into versiond-0.
		legacyMount := filepath.Join(stack.WorkDir, "legacy-devshardd")
		legacyBytes, err := os.ReadFile(legacySrc)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(legacyMount, legacyBytes, 0o755))
		overrideKey := "VERSIOND_OVERRIDE_" + strings.ReplaceAll(cfg.Versiond.VersionName, ".", "_")
		harness.PatchComposeServiceEnv(t, stack.ComposePath, "versiond-0", overrideKey,
			"/opt/devshard/devshardd")
		harness.PatchVersiondServiceBinaryMount(t, stack.ComposePath, "versiond-0", "./legacy-devshardd")
	}

	// Keep the gateway out of the reproduction until the router's catalog and
	// both versiond health checks have converged; otherwise a catalog failure
	// masks the compatibility result.
	stack.UpInfra(t, false)
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router")
		}
	})

	backend := harness.WaitRouterVersionBackend(t, stack, cfg.Versiond.VersionName, 60*time.Second)
	state := harness.RouterPoolHostState(t, stack, cfg, backend)
	require.Equal(t, harness.RouterSlotUp, state["versiond-0"],
		"versiond-0 must be admitted by the versiond-level h2 router")
	require.Equal(t, harness.RouterSlotUp, state["versiond-1"],
		"versiond-1 must be admitted by the versiond-level h2 router")
	t.Logf("%s version backend %s states: %v", tc.name, backend, state)

	harness.WaitRouterCatalogAdmitted(t, stack, 60*time.Second)
	stack.UpGateway(t)
	eps := stack.Endpoints(t, cfg)
	client := harness.GatewayChatClient()
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 2*time.Minute, stack)

	slots := make(map[string]harness.RouterSlot, len(cfg.Hosts))
	for _, slot := range harness.RouterPool(t, stack) {
		if slot.Backend == backend {
			if host := harness.HostIDForUpstream(cfg, slot.Address); host != "" {
				slots[host] = slot
			}
		}
	}
	require.Len(t, slots, 2, "version backend %s must expose both versiond hosts", backend)
	t.Cleanup(func() {
		for _, slot := range slots {
			harness.SetRouterServerEnabled(t, stack, backend, slot.Name, true)
		}
	})

	model := config.PrimaryModelID(cfg)
	var probeFailures []string
	// Probe the current host first so a legacy transport failure cannot poison
	// the gateway's long-lived peer connection before the control probe runs.
	for _, target := range []string{"versiond-1", "versiond-0"} {
		for host, slot := range slots {
			harness.SetRouterServerEnabled(t, stack, backend, slot.Name, host == target)
		}
		other := "versiond-1"
		if target == "versiond-1" {
			other = "versiond-0"
		}
		ready := harness.AssertEventually(t, 15*time.Second, 250*time.Millisecond, func() bool {
			current := harness.RouterPoolHostState(t, stack, cfg, backend)
			return current[target] == harness.RouterSlotUp && current[other] != harness.RouterSlotUp
		})
		require.True(t, ready, "router did not isolate native-gRPC traffic to %s: %s",
			target, harness.DescribeRouterPool(t, stack, cfg))

		_, err := harness.TryPostGatewayChatCompletion(client, eps.GatewayHTTP,
			harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
				Model: model,
				Messages: []harness.ChatMessage{{Role: "user", Content: fmt.Sprintf(
					"%s probe through %s", tc.requestLabel, target)}},
				MaxTokens: 8,
			})
		t.Logf("%s request through forced backend %s: err=%v", tc.name, target, err)
		if err != nil {
			probeFailures = append(probeFailures, target+": "+err.Error())
		}

		for _, slot := range slots {
			harness.SetRouterServerEnabled(t, stack, backend, slot.Name, true)
		}
		harness.WaitRouterPoolState(t, stack, cfg, backend, "versiond-0", harness.RouterSlotUp, 15*time.Second)
		harness.WaitRouterPoolState(t, stack, cfg, backend, "versiond-1", harness.RouterSlotUp, 15*time.Second)
	}
	require.Empty(t, probeFailures, "forced native-gRPC backend probes failed: %s", strings.Join(probeFailures, "; "))
}
