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
)

// TestPeerRPCMixedChildCapabilitiesNativeGRPC checks the protocol adaptation
// at both sides of a mixed deployment: one current versiond runs the
// pre-capability child (Peer RPC + h2c, but no --print-child-h2c flag), while
// its sibling runs the current child. The router admits both versiond hosts;
// the test then forces native-gRPC traffic through each backend independently.
// The historical child path is supplied by TESTENV_MIXED_CHILD_BINARY so this
// test uses a real pre-fix artifact rather than a fake protocol stub.
func TestPeerRPCMixedChildCapabilitiesNativeGRPC(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.SkipUnlessEnv(t, grpcLegacyCompatEnv)
	requireNoProxyGRPC(t)
	if os.Getenv(harness.EnvVersiondImage) != "" || os.Getenv(harness.EnvVersiondRouterImage) != "" {
		t.Fatal("mixed child capability test requires current versiond/router images")
	}
	harness.RequireDocker(t)

	legacySrc := strings.TrimSpace(os.Getenv(mixedChildBinaryEnv))
	info, err := os.Stat(legacySrc)
	require.NoError(t, err, "%s must point to a pre-capability devshardd", mixedChildBinaryEnv)
	require.False(t, info.IsDir(), "%s points to a directory", mixedChildBinaryEnv)

	stack := harness.NewStack(t, "citest-grpc-mixed-child-capabilities-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2)

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

	// Keep the gateway out of the reproduction until the router's catalog and
	// both versiond health checks have converged; otherwise a catalog failure
	// masks the mixed-capability admission result.
	stack.UpInfra(t, false)
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router")
		}
	})

	backend := harness.WaitRouterVersionBackend(t, stack, cfg.Versiond.VersionName, 60*time.Second)
	state := harness.RouterPoolHostState(t, stack, cfg, backend)
	require.Equal(t, harness.RouterSlotUp, state["versiond-0"],
		"legacy versiond must be admitted by the versiond-level h2 router")
	require.Equal(t, harness.RouterSlotUp, state["versiond-1"],
		"current versiond must be admitted by the versiond-level h2 router")
	t.Logf("native-gRPC version backend %s states: %v", backend, state)

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
					"mixed-child-capability probe through %s", target)}},
				MaxTokens: 8,
			})
		t.Logf("native-gRPC request through forced backend %s: err=%v", target, err)
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
