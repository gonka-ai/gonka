//go:build testenvci

package citest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/replay"

	"github.com/stretchr/testify/require"
)

// oldHostsDrainBound is how soon after the Finish the escrow must be free to
// settle when no host can answer a challenge: a few verify round trips.
const oldHostsDrainBound = 30 * time.Second

// TestUsageChallengeOnOldHosts is a rollout state: the gateway runs this
// build and every host still runs a devshardd without VerifyChallenge. A
// gateway taken to a new release keeps serving its open escrows on the
// previous protocol, whose hosts are in exactly this state. An honest stream
// that calls for a challenge (row E2 of TestUsageOverclaimScenarios: a
// receipt, then tokens that decode to no text) must leave the escrow free to
// settle, since no host can vote, and its diffs must carry no MsgChallenge,
// which those hosts cannot apply.
//
// TESTENV_MIXED_CHILD_BINARY is the previous devshardd, built from the
// previous release line with scripts/build-baseline-infra.sh.
func TestUsageChallengeOnOldHosts(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	childBinary := strings.TrimSpace(os.Getenv(mixedChildBinaryEnv))
	require.NotEmpty(t, childBinary, "%s must point to a devshardd without VerifyChallenge", mixedChildBinaryEnv)
	childBytes, err := os.ReadFile(childBinary)
	require.NoError(t, err)
	childSum := sha256.Sum256(childBytes)
	dataset, err := replay.LoadFile(usageScenarioReplayFile)
	require.NoError(t, err)

	stack, cfg, eps := harness.BootUsageScenarioStack(t, "citest-usage-old-hosts-*", harness.UsageScenarioStackOpts{
		ModelID:        usageScenarioModel,
		ReplayFile:     usageScenarioReplayFile,
		ValidationRate: usageScenarioValidationRate,
		ChildBinary:    childBinary,
	})
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, eps.MockOpenAIHTTP)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-2", "versiond-3")
		}
	})
	for _, host := range cfg.Hosts {
		out, err := stack.ComposeExecOutput(host.ID, "sha256sum", cfg.Versiond.OverridePath)
		require.NoError(t, err, "%s: %s", host.ID, out)
		require.True(t, strings.HasPrefix(strings.TrimSpace(out), hex.EncodeToString(childSum[:])),
			"%s does not run %s: %s", host.ID, childBinary, out)
	}

	harness.PatchGatewayRedundancySpeedPolicy(t, client, eps.GatewayHTTP, "legacy")
	escrowID := harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)
	adminKey := harness.TestenvAdminAPIKey
	harness.ResetMockOpenAIFault(t, client, eps.MockOpenAIHTTP)
	harness.ClearGatewayParticipantQuarantines(t, client, eps.GatewayHTTP, adminKey, escrowID)

	var row usageScenario
	for _, candidate := range usageScenarios {
		if candidate.id == "E2" {
			row = candidate
		}
	}
	require.Equal(t, "E2", row.id)
	run := &usageRun{row: row, name: row.id + " " + row.executor + ": " + row.shape, seed: 1000 + int(time.Now().Unix()%1_000_000)*100}
	driveUsageScenario(t, stack, cfg, eps, escrowID, recordedSample(t, dataset, row.recording), run)
	nonce := run.obs.nonce

	pending := int64(-1)
	for time.Since(run.finished) < oldHostsDrainBound {
		pending = harness.GatewayPendingRaceCleanup(t, client, eps.GatewayHTTP, adminKey, escrowID)
		if pending == 0 {
			break
		}
		time.Sleep(time.Second)
	}
	t.Logf("inference %d: pending background work %d, %s after the Finish", nonce, pending, time.Since(run.finished).Round(time.Second))

	latest := harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey).LatestNonce
	for _, host := range cfg.Hosts {
		for _, tx := range harness.HostDiffTxs(t, stack, cfg, host.ID, escrowID, nonce, latest) {
			id, ok := tx.InferenceID()
			require.False(t, ok && id == nonce && tx.Kind == challengeTxKind,
				"%s: the diff log carries a MsgChallenge for inference %d", host.ID, nonce)
		}
	}
	require.Zero(t, pending, "the gateway still holds background work for escrow %s %s after the Finish, so it cannot settle", escrowID, oldHostsDrainBound)
}
