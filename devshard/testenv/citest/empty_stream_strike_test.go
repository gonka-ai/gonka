//go:build testenvci

package citest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/replay"

	"github.com/stretchr/testify/require"
)

// emptyStrikeLine is the part of the gateway's empty_stream_strike_* log
// lines the test reads.
type emptyStrikeLine struct {
	Msg         string `json:"msg"`
	Escrow      string `json:"escrow"`
	Nonce       uint64 `json:"nonce"`
	Participant string `json:"participant"`
	Outcome     string `json:"outcome"`
	Strike      bool   `json:"strike"`
}

// TestEmptyStreamStrikeFollowsTheChallenge drives two rows of
// TestUsageOverclaimScenarios whose stream reaches the gateway whole, bound
// to its Finish, and with no text. The gateway holds the empty-stream strike
// of each attempt until its challenge ends. The honest row (E2: logit_bias on
// a lone byte, 256 tokens that decode to nothing) must not strike any host it
// tried: the voters count the positions of the stored body and the challenge
// ends none. The inflating row (E3: the ML node drops every token and keeps
// its usage) must strike every host it tried: the voters vote EMPTY_STREAM
// and the inference is invalidated.
func TestEmptyStreamStrikeFollowsTheChallenge(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	dataset, err := replay.LoadFile(usageScenarioReplayFile)
	require.NoError(t, err)
	stack, cfg, eps := harness.BootUsageScenarioStack(t, "citest-empty-strike-*", harness.UsageScenarioStackOpts{
		ModelID:        usageScenarioModel,
		ReplayFile:     usageScenarioReplayFile,
		ValidationRate: usageScenarioValidationRate,
	})
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, eps.MockOpenAIHTTP)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-openai-0", "versiond-0", "versiond-1", "versiond-2", "versiond-3")
		}
	})
	harness.PatchGatewayRedundancySpeedPolicy(t, client, eps.GatewayHTTP, "legacy")
	escrowID := harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)
	harness.ResetMockOpenAIFault(t, client, eps.MockOpenAIHTTP)
	harness.ClearGatewayParticipantQuarantines(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, escrowID)

	row := func(id, shape string) usageScenario {
		for _, candidate := range usageScenarios {
			if candidate.id == id && strings.Contains(candidate.shape, shape) {
				return candidate
			}
		}
		t.Fatalf("no usage scenario %s %q", id, shape)
		return usageScenario{}
	}
	seed := 1000 + int(time.Now().Unix()%1_000_000)*100
	cases := []struct {
		run     *usageRun
		outcome string
		strike  bool
		// from and to bound the nonces of the row's attempts: a request's
		// attempts start before it returns, so the state's latest nonce after
		// a row is at or past all of them, and before every later row's.
		from, to uint64
	}{
		{run: &usageRun{row: row("E2", "single-byte fragment")}, outcome: "none"},
		{run: &usageRun{row: row("E3", "without generated positions")}, outcome: "invalidated", strike: true},
	}
	latestNonce := func() uint64 {
		return harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey).LatestNonce
	}
	from := latestNonce()
	for i := range cases {
		c := &cases[i]
		c.run.seed = seed + i
		c.run.name = c.run.row.id + " " + c.run.row.executor + ": " + c.run.row.shape
		driveUsageScenario(t, stack, cfg, eps, escrowID, recordedSample(t, dataset, c.run.row.recording), c.run)
		c.from, c.to = from, latestNonce()
		from = c.to
		t.Logf("%s: inference %d finished, attempts in (%d, %d]", c.run.name, c.run.obs.nonce, c.from, c.to)
	}

	lines := func(msg string) map[uint64]emptyStrikeLine {
		out, err := stack.ComposeLogsAll("devshardctl")
		require.NoError(t, err)
		found := map[uint64]emptyStrikeLine{}
		for _, raw := range strings.Split(out, "\n") {
			i := strings.IndexByte(raw, '{')
			if i < 0 || !strings.Contains(raw, `"`+msg+`"`) {
				continue
			}
			var line emptyStrikeLine
			if json.Unmarshal([]byte(raw[i:]), &line) == nil && line.Msg == msg && line.Escrow == escrowID {
				found[line.Nonce] = line
			}
		}
		return found
	}
	var held, settled map[uint64]emptyStrikeLine
	harness.AssertEventually(t, usageChallengeWindow+time.Minute, 2*time.Second, func() bool {
		held, settled = lines("empty_stream_strike_held"), lines("empty_stream_strike_settled")
		for nonce := range held {
			if _, ok := settled[nonce]; !ok && nonce > cases[0].from && nonce <= cases[len(cases)-1].to {
				return false
			}
		}
		return true
	})
	for _, c := range cases {
		require.Contains(t, held, c.run.obs.nonce, "%s: the strike of inference %d was applied at once, not left to its challenge", c.run.name, c.run.obs.nonce)
		hosts := map[string]bool{}
		for nonce, line := range held {
			if nonce <= c.from || nonce > c.to {
				continue
			}
			got, ok := settled[nonce]
			require.True(t, ok, "%s: the strike held for inference %d never settled", c.run.name, nonce)
			require.Equal(t, c.outcome, got.Outcome, "%s: challenge outcome of inference %d", c.run.name, nonce)
			require.Equal(t, c.strike, got.Strike, "%s: strike on %s for inference %d", c.run.name, got.Participant, nonce)
			hosts[line.Participant] = true
		}
		t.Logf("%s: %d host(s) held a strike, every one settled %s with strike=%v", c.run.name, len(hosts), c.outcome, c.strike)
	}
}
