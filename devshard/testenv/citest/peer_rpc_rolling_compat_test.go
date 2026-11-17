//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

type peerRPCRollingState struct {
	routerLegacy   bool
	versiondLegacy bool
	childLegacy    bool
}

type peerRPCRollingSequence struct {
	name  string
	steps []peerRPCRollingState
}

var peerRPCRollingSequences = []peerRPCRollingSequence{
	{
		name: "router_then_versiond",
		steps: []peerRPCRollingState{
			{routerLegacy: true, versiondLegacy: true, childLegacy: true},
			{routerLegacy: false, versiondLegacy: true, childLegacy: true},
			{routerLegacy: false, versiondLegacy: false, childLegacy: false},
		},
	},
	{
		name: "versiond_then_router",
		steps: []peerRPCRollingState{
			{routerLegacy: true, versiondLegacy: true, childLegacy: true},
			{routerLegacy: true, versiondLegacy: false, childLegacy: false},
			{routerLegacy: false, versiondLegacy: false, childLegacy: false},
		},
	},
	{
		name: "child_then_versiond",
		steps: []peerRPCRollingState{
			{routerLegacy: false, versiondLegacy: true, childLegacy: true},
			{routerLegacy: false, versiondLegacy: true, childLegacy: false},
			{routerLegacy: false, versiondLegacy: false, childLegacy: false},
		},
	},
	{
		name: "rollback_current_to_legacy",
		steps: []peerRPCRollingState{
			{routerLegacy: false, versiondLegacy: false, childLegacy: false},
			{routerLegacy: false, versiondLegacy: true, childLegacy: true},
			{routerLegacy: true, versiondLegacy: true, childLegacy: true},
		},
	},
	{
		name: "upgrade_legacy_to_current",
		steps: []peerRPCRollingState{
			{routerLegacy: true, versiondLegacy: true, childLegacy: true},
			{routerLegacy: true, versiondLegacy: false, childLegacy: true},
			{routerLegacy: true, versiondLegacy: false, childLegacy: false},
			{routerLegacy: false, versiondLegacy: false, childLegacy: false},
		},
	},
}

// TestPeerRPCRollingDeployCompatibility checks that protocol changes remain
// safe across realistic component-ordering during an upgrade or rollback.
// Each sequence starts from its first state, applies one transition at a time,
// and probes both versiond hosts after every transition. A native-gRPC request
// must succeed, or the rollout must be visibly blocked; an UP host returning a
// 502 is treated as a compatibility failure.
//
// This test is part of citest-peerrpc-cross-version-compat, alongside the
// versiond and router compatibility matrices.
func TestPeerRPCRollingDeployCompatibility(t *testing.T) {
	legacySrc := requirePeerRPCCompatibilityEnv(t)
	for _, sequence := range peerRPCRollingSequences {
		t.Run(sequence.name, func(t *testing.T) {
			runPeerRPCRollingSequence(t, sequence, legacySrc)
		})
	}
}

func runPeerRPCRollingSequence(t *testing.T, sequence peerRPCRollingSequence, legacySrc string) {
	t.Helper()
	require.NotEmpty(t, sequence.steps)

	stack := harness.NewStack(t, "citest-grpc-rolling-"+sequence.name+"-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2)

	currentChild := filepath.Join(stack.TestenvDir, "..", "..", "build", "devshardd")
	legacyMount := filepath.Join(stack.WorkDir, "legacy-devshardd")
	legacyBytes, err := os.ReadFile(legacySrc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(legacyMount, legacyBytes, 0o755))

	state := peerRPCRollingState{}
	applyPeerRPCRollingComposeState(t, stack, sequence.steps[0], state, currentChild, legacyMount, false)
	state = sequence.steps[0]

	stack.UpInfra(t, false)
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router")
		}
	})

	harness.WaitRouterVersionBackend(t, stack, cfg.Versiond.VersionName, 60*time.Second)
	harness.WaitRouterCatalogAdmitted(t, stack, 60*time.Second)
	stack.UpGateway(t)
	eps := stack.Endpoints(t, cfg)

	client := harness.GatewayChatClient()
	for stepIndex, desired := range sequence.steps {
		if stepIndex > 0 {
			harness.Step(t, "%s: applying step %d", sequence.name, stepIndex)
			applyPeerRPCRollingComposeState(t, stack, desired, state, currentChild, legacyMount, true)
			state = desired
		}

		backend := harness.WaitRouterVersionBackend(t, stack, cfg.Versiond.VersionName, 60*time.Second)
		harness.WaitRouterCatalogAdmitted(t, stack, 60*time.Second)
		probeRollingState(t, stack, cfg, eps.GatewayHTTP, client, backend, state, stepIndex == len(sequence.steps)-1)
	}
}

func applyPeerRPCRollingComposeState(
	t *testing.T,
	stack *harness.Stack,
	desired, current peerRPCRollingState,
	currentChild, legacyMount string,
	recreate bool,
) {
	t.Helper()
	var services []string
	addService := func(service string) {
		for _, existing := range services {
			if existing == service {
				return
			}
		}
		services = append(services, service)
	}

	if desired.routerLegacy != current.routerLegacy {
		image := "devshard-versiond-router:latest"
		if desired.routerLegacy {
			image = legacyRouterImage
		}
		if desired.routerLegacy {
			harness.PinVersiondRouterServiceImage(t, stack.ComposePath, image)
		} else {
			harness.PatchComposeServiceImage(t, stack.ComposePath, "versiond-router", image)
		}
		addService("versiond-router")
	}
	if desired.versiondLegacy != current.versiondLegacy {
		image := "devshard-versiond:latest"
		if desired.versiondLegacy {
			image = legacyVersiondImage
		}
		for _, service := range []string{"versiond-0", "versiond-1"} {
			if desired.versiondLegacy {
				harness.PinVersiondServiceImage(t, stack.ComposePath, service, image)
			} else {
				harness.PatchComposeServiceImage(t, stack.ComposePath, service, image)
			}
			addService(service)
		}
	}
	if desired.childLegacy != current.childLegacy {
		mount := currentChild
		if desired.childLegacy {
			mount = legacyMount
		}
		for _, service := range []string{"versiond-0", "versiond-1"} {
			harness.PatchVersiondServiceBinaryMount(t, stack.ComposePath, service, mount)
			addService(service)
		}
	}
	if recreate && len(services) > 0 {
		stack.RecreateServices(t, services...)
	}
}

func probeRollingState(
	t *testing.T,
	stack *harness.Stack,
	cfg *config.File,
	gatewayURL string,
	client *http.Client,
	backend string,
	state peerRPCRollingState,
	final bool,
) {
	t.Helper()
	routerState := harness.RouterPoolHostState(t, stack, cfg, backend)
	require.Equal(t, harness.RouterSlotUp, routerState["versiond-0"], "versiond-0 left the router pool")
	require.Equal(t, harness.RouterSlotUp, routerState["versiond-1"], "versiond-1 left the router pool")

	if state.routerLegacy {
		// The legacy router has no :8081 h2c frontend. A failed request here is
		// the expected explicit rollout block; a successful request would mean
		// traffic unexpectedly bypassed the native-gRPC capability boundary.
		harness.WaitGETOK(t, client, gatewayURL+"/v1/status", 2*time.Minute,
			"gateway HTTP while legacy router blocks native gRPC", stack)
		_, err := harness.TryPostGatewayChatCompletion(client, gatewayURL,
			harness.TestenvAdminAPIKey, rollingChatRequest("legacy-router"))
		t.Logf("legacy-router rolling state request: err=%v", err)
		require.Error(t, err, "legacy router must block native-gRPC traffic")
		return
	}

	harness.WaitGatewayChatReady(t, client, gatewayURL, 2*time.Minute, stack)
	var failures []string
	for _, target := range []string{"versiond-1", "versiond-0"} {
		for _, slot := range harness.RouterPool(t, stack) {
			if slot.Backend == backend {
				host := harness.HostIDForUpstream(cfg, slot.Address)
				harness.SetRouterServerEnabled(t, stack, backend, slot.Name, host == target)
			}
		}
		other := "versiond-1"
		if target == "versiond-1" {
			other = "versiond-0"
		}
		ready := harness.AssertEventually(t, 15*time.Second, 250*time.Millisecond, func() bool {
			current := harness.RouterPoolHostState(t, stack, cfg, backend)
			return current[target] == harness.RouterSlotUp && current[other] != harness.RouterSlotUp
		})
		require.True(t, ready, "router did not isolate rolling traffic to %s", target)

		_, err := harness.TryPostGatewayChatCompletion(client, gatewayURL,
			harness.TestenvAdminAPIKey, rollingChatRequest(fmt.Sprintf("rolling-%s", target)))
		t.Logf("rolling state final=%t target=%s legacyVersiond=%t legacyChild=%t err=%v",
			final, target, state.versiondLegacy, state.childLegacy, err)
		if err != nil {
			failures = append(failures, target+": "+err.Error())
		}
		for _, slot := range harness.RouterPool(t, stack) {
			if slot.Backend == backend {
				harness.SetRouterServerEnabled(t, stack, backend, slot.Name, true)
			}
		}
		harness.WaitRouterPoolState(t, stack, cfg, backend, "versiond-0", harness.RouterSlotUp, 15*time.Second)
		harness.WaitRouterPoolState(t, stack, cfg, backend, "versiond-1", harness.RouterSlotUp, 15*time.Second)
	}
	require.Empty(t, failures, "rolling native-gRPC requests failed while hosts were UP: %s", strings.Join(failures, "; "))
}

func rollingChatRequest(label string) harness.ChatCompletionRequest {
	return harness.ChatCompletionRequest{
		Model:     "test-model",
		Messages:  []harness.ChatMessage{{Role: "user", Content: label + " rolling compatibility probe"}},
		MaxTokens: 8,
	}
}
