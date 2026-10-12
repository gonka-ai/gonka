//go:build testenvci

package citest

import (
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

// TestPeerRPCMixedChildCapabilitiesNativeGRPC checks a mixed child rollout.
// versiond-0 runs a real pre-capability child (no --print-child-h2c). versiond-1
// runs the current child. Both stay in the JSON pool. Only the current child
// stays in the native-gRPC pool, and a chat through that pool succeeds.
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
	peerBackend := backend + "_rpc"
	jsonState := harness.RouterPoolHostState(t, stack, cfg, backend)
	require.Equal(t, harness.RouterSlotUp, jsonState["versiond-0"],
		"legacy versiond must stay in the JSON pool")
	require.Equal(t, harness.RouterSlotUp, jsonState["versiond-1"],
		"current versiond must stay in the JSON pool")
	harness.WaitRouterPoolState(t, stack, cfg, peerBackend, "versiond-1", harness.RouterSlotUp, 60*time.Second)
	harness.WaitRouterPoolState(t, stack, cfg, peerBackend, "versiond-0", harness.RouterSlotDown, 60*time.Second)
	t.Logf("json backend %s: %v", backend, jsonState)
	t.Logf("native-gRPC backend %s: %v", peerBackend, harness.RouterPoolHostState(t, stack, cfg, peerBackend))

	harness.WaitRouterCatalogAdmitted(t, stack, 60*time.Second)
	stack.UpGateway(t)
	eps := stack.Endpoints(t, cfg)
	client := harness.GatewayChatClient()
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 2*time.Minute, stack)

	model := config.PrimaryModelID(cfg)
	_, err = harness.TryPostGatewayChatCompletion(client, eps.GatewayHTTP,
		harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
			Model:     model,
			Messages:  []harness.ChatMessage{{Role: "user", Content: "mixed-child-capability probe through the native-gRPC pool"}},
			MaxTokens: 8,
		})
	require.NoError(t, err)
}
