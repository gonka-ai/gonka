package harness

import (
	"net/http"
	"testing"
	"time"

	"devshard/testenv/config"
)

// Executor fault trip files read by testenv-tagged devshardd. They must match
// transport/executor_fault_testenv.go.
const (
	// ExecutorFaultDropPayload makes a host apply the diff and sign state but
	// sign no receipt and run nothing: the gateway sees a refusal.
	ExecutorFaultDropPayload = "/tmp/devshard-fault-drop-payload"
	// ExecutorFaultForgeChallenge makes a challenged executor answer with a
	// receipt, ConfirmStart, and Finish that carry no executor signature.
	ExecutorFaultForgeChallenge = "/tmp/devshard-fault-forge-challenge"
)

// Seed escrow timeouts for BootTimeoutEvidenceStack, in seconds.
const (
	TimeoutEvidenceRefusalTimeout   = 5
	TimeoutEvidenceExecutionTimeout = 8
)

// BootTimeoutEvidenceStack is the error-miss roster (HA pair + two solos, three
// identities, so two non-executor verifiers clear VoteThreshold) with short
// refusal and execution timeouts already in the seed escrow.
func BootTimeoutEvidenceStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteMultiConfig(t, stack.WorkDir, MultiConfigOpts{
		Hosts:            4,
		EscrowSlots:      3,
		RefusalTimeout:   TimeoutEvidenceRefusalTimeout,
		ExecutionTimeout: TimeoutEvidenceExecutionTimeout,
	})
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireFourVersiondHosts(t, cfg)
	stack.Up(t)
	eps := stack.Endpoints(t, cfg)
	client := GatewayChatClient()
	WaitStackHealthy(t, stack, eps)
	WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)
	return stack, cfg, eps
}

// SetExecutorFault creates or removes a fault trip file on every versiond host.
func SetExecutorFault(t *testing.T, stack *Stack, cfg *config.File, file string, on bool) {
	t.Helper()
	for _, h := range cfg.Hosts {
		if on {
			stack.ComposeExec(t, h.ID, "touch", file)
		} else {
			stack.ComposeExec(t, h.ID, "rm", "-f", file)
		}
	}
}

// NewGatewayInferences returns the records in after whose ids are not in before.
func NewGatewayInferences(before, after map[string]GatewayDebugInference) map[string]GatewayDebugInference {
	fresh := make(map[string]GatewayDebugInference)
	for id, record := range after {
		if _, seen := before[id]; !seen {
			fresh[id] = record
		}
	}
	return fresh
}

// WaitGatewayInferencesSettle polls until settled holds for every record in watched, or
// wait elapses, and returns the last record seen for each of their ids.
func WaitGatewayInferencesSettle(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string, watched map[string]GatewayDebugInference, wait time.Duration, settled func(status string) bool) map[string]GatewayDebugInference {
	t.Helper()
	last := make(map[string]GatewayDebugInference, len(watched))
	AssertEventually(t, wait, time.Second, func() bool {
		all := GetGatewayDebugInferences(t, client, gatewayURL, adminAPIKey, escrowID)
		done := true
		for id := range watched {
			record, ok := all[id]
			if !ok {
				continue
			}
			last[id] = record
			if !settled(record.Status) {
				done = false
			}
		}
		return done
	})
	return last
}
