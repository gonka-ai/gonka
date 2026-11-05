//go:build testenvci

package citest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"common/completionapi"
	"common/validation"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"
	"devshard/testenv/replay"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rows below are the outcome tables of the usage challenge design in
// gonka-ai/gonka#2000 ("What each case comes to"): what the gateway does,
// what the escrow's hosts vote, whether the payment stands, whether the
// executor takes an Invalid, and how the inference reaches replay. The design
// lands in three PRs. A row asserts the cells of the PRs in landedPRs and,
// for the others, the behaviour before them, logging the design's value as
// pending.
//
// The executor binary is honest in every row. An inflating executor is an
// operator running a modified engine or tampering with storage, so the
// injection sits in mock-openai, which stands for the executor's ML node and
// serves answers recorded from vLLM's output processor: token ids, chunk
// boundaries, finish reasons and usage are vLLM's, the logprob values are
// synthetic. The same mock answers the
// validators' replays with the honest recording, since it also stands for
// their ML nodes.
//
// Not a row: inflated input on a request with image parts. No model the
// network serves takes images, the engine refuses such a request and there is
// no Finish to inflate; the design leaves it to replay for a vision model.

// usageScenarioReplayFile holds the ML node 3.1.0 recordings (vLLM fork 71ee49c4d).
var usageScenarioReplayFile = filepath.Join("..", "mockopenai", "testdata", "vllm_recorded_mlnode310.jsonl")

// usageScenarioModel is the model of every row: one escrow serves one model.
const usageScenarioModel = "zai-org/GLM-5.3-Flash"

// usageScenarioValidationRate keeps sampled replay from deciding a row, so
// each row shows the challenge path alone. Today's replay verdict is measured
// separately, on the same mock, for every row.
const usageScenarioValidationRate = 1

// usageChallengeWindow is how long after the last Finish the table waits for
// challenges that have not arrived. A voter that cannot get the body retries
// it for the fetch window counted from its Finish, and a host caps that
// window at half the seal grace: 15 s with the 30 s grace this stack's mock
// chain seeds. A host holds the gateway's first ask for up to 20 s, so the
// window runs out inside it and the vote comes back with that ask; the
// gateway sends MsgChallenge in the next diff, so 20 s + 10 s for that diff,
// with room to spare. A body that can be had is voted on at once.
const usageChallengeWindow = 45 * time.Second

// stallHardTimeout is the gateway's hard timeout for a streaming attempt
// during the stall row (streaming_attempt_hard_timeout_ms, 30 minutes by
// default): the gateway cancels the winner this long after it started.
const stallHardTimeout = 3 * time.Second

// usageVoters is how many identities vote on a challenge: the three
// identities of the stack less the executor's.
const usageVoters = 2

// gatewayChallengesMetric counts the gateway's challenges by reason.
const gatewayChallengesMetric = "devshard_gateway_challenges_total"

// challengeTxKind is the DevshardTx member MsgChallenge is applied as.
const challengeTxKind = "challenge"

// Gateway challenge reasons and voter faults, as the design names them.
const (
	reasonNone       = ""
	reasonUnbound    = "STREAM_UNBOUND"
	reasonOutputHigh = "OUTPUT_ABOVE_BYTES"
	reasonInputHigh  = "INPUT_ABOVE_BYTES"
	reasonEmpty      = "EMPTY_STREAM"

	faultNotCalled   = ""
	faultNone        = "NONE"
	faultUnavailable = "PAYLOAD_UNAVAILABLE"
	faultHash        = "HASH_MISMATCH"
	faultInflated    = "USAGE_INFLATED"
	faultEmpty       = "EMPTY_STREAM"
)

var voterFaults = []string{faultNone, faultUnavailable, faultHash, faultEmpty, faultInflated}

var gatewayReasons = []string{reasonUnbound, reasonEmpty, reasonOutputHigh, reasonInputHigh}

// Payment and Invalid cells.
const (
	paymentStands = "stands"
	paymentLost   = "lost"
	invalidSame   = "+0"
	invalidUp     = "+1"
)

// How a row's inference reaches replay.
const (
	replaySampled = "sampled" // the normal sampling rate applies
	replayRaised  = "raised"  // the challenged mark raises the rate
	replayNone    = "none"    // the record is terminal
)

// usageRecording names a recorded answer: build file, model and case.
type usageRecording struct {
	build string
	model string
	name  string
}

func (r usageRecording) String() string { return r.build + "/" + r.model + "/" + r.name }

// landedPRs are the PRs of gonka-ai/gonka#2000 whose cells the test
// asserts: PR 1 of the three.
var landedPRs = map[int]bool{1: true}

// cell is one expected cell of the design's table and the PR of
// gonka-ai/gonka#2000 that brings it. The zero cell is today's behaviour,
// which the row keeps. Until its PR lands a cell expects today's value and
// logs the design's as pending.
type cell struct {
	pr   int
	want string
}

func inPR(pr int, want string) cell { return cell{pr, want} }

// usageWant is a row's expected cells, by column.
type usageWant struct {
	gateway, voters, payment, invalid, replay cell
}

// todayCells is what today's code shows in every column: no challenge, no
// vote, the payment stands, no Invalid, the normal sampling rate.
var todayCells = usageOutcome{challenge: reasonNone, fault: faultNotCalled, payment: paymentStands, invalid: invalidSame, replay: replaySampled}

// now is the value the cell expects with landedPRs.
func (c cell) now(today string) string {
	if c.pr != 0 && landedPRs[c.pr] {
		return c.want
	}
	return today
}

func assertCell(t *testing.T, column string, c cell, today, got string) {
	t.Helper()
	if c.pr != 0 && !landedPRs[c.pr] {
		t.Logf("%s: pending PR %d: want %s, today %s", column, c.pr, c.want, got)
	}
	assert.Equal(t, c.now(today), got, column)
}

// usageOutcome is one row of the tables, as measured.
type usageOutcome struct {
	challenge string // the gateway's reason; reasonNone when it raises none
	fault     string // the voters' fault; faultNotCalled when no vote is taken
	payment   string // paymentStands or paymentLost
	invalid   string // invalidUp when the executor's hs.Invalid goes up
	replay    string // replaySampled, replayRaised or replayNone
}

type usageScenario struct {
	id        string
	executor  string
	shape     string
	recording usageRecording
	// mlNode is what the executor's ML node does to the recorded answer.
	mlNode mockopenai.FaultPatch
	// cut closes the connections between the gateway and the hosts after
	// the first token reaches the gateway; the executor's run goes on.
	cut bool
	// cutEarly cuts before the first token instead: the gateway has the
	// receipt and no content.
	cutEarly bool
	// stall holds the ML node's answer after the first token past
	// stallHardTimeout, so the gateway cancels the attempt itself; the
	// executor's run goes on.
	stall bool
	// body is what the executor's operator does to the stored answer once
	// the Finish is signed.
	body storedBody
	want usageWant
	// replayVerdict is what a validator's replay concludes today if it
	// samples the inference: "pass" or the reason it fails. Empty where the
	// in-process check cannot see the row's tampering: it replays the ML
	// node's answer, not the body the executor serves.
	replayVerdict string
}

// sequential rows act on every stream the gateway holds, so they run one at
// a time, after the others.
func (s usageScenario) sequential() bool { return s.cut || s.stall }

type storedBody int

const (
	bodyKept storedBody = iota
	bodyRemoved
	bodyReplaced
)

func intKnob(v int) *int    { return &v }
func boolKnob(v bool) *bool { return &v }

var usageScenarios = []usageScenario{
	{
		id: "H1", executor: "honest", shape: "full stream",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "plain_short"},
		want:          usageWant{},
		replayVerdict: "pass",
	},
	{
		id: "I1", executor: "inflating", shape: "inflated output_tokens, full stream",
		recording: usageRecording{"mlnode310", usageScenarioModel, "plain_short"},
		// The issue's example: output_tokens set to the request's max_tokens.
		mlNode:        mockopenai.FaultPatch{UsageCompletionTokens: intKnob(4096)},
		want:          usageWant{gateway: inPR(2, reasonOutputHigh), voters: inPR(2, faultInflated), payment: inPR(2, paymentLost), invalid: inPR(2, invalidUp), replay: inPR(2, replayNone)},
		replayVerdict: "Inflated token counts.",
	},
	{
		id: "I1", executor: "inflating", shape: "inflated output_tokens, padded with empty lines",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "plain_short"},
		mlNode:        mockopenai.FaultPatch{UsageCompletionTokens: intKnob(4096), PadEmptyChunks: intKnob(200)},
		want:          usageWant{gateway: inPR(2, reasonOutputHigh), voters: inPR(2, faultInflated), payment: inPR(2, paymentLost), invalid: inPR(2, invalidUp), replay: inPR(2, replayNone)},
		replayVerdict: "Inflated token counts.",
	},
	{
		// The ML node stops after the first token. The gateway gives a
		// streaming attempt a hard timeout and cancels this one itself; the
		// executor's run goes on, signs the Finish and has nothing to serve.
		// An attempt the gateway cut is challenged like one the network cut,
		// since the receipt is signed either way. The row runs before the
		// cut rows: a host they put on probation gets no winner crown, and
		// the hard timeout applies to the winner.
		id: "I3", executor: "inflating", shape: "stalls after the first token past the gateway's hard timeout, then withholds the body",
		recording: usageRecording{"mlnode310", usageScenarioModel, "long_mixed"},
		stall:     true,
		body:      bodyRemoved,
		want:      usageWant{gateway: inPR(1, reasonUnbound), voters: inPR(1, faultUnavailable), payment: inPR(1, paymentLost), invalid: inPR(1, invalidUp), replay: inPR(1, replayNone)},
	},
	{
		id: "H2", executor: "honest", shape: "stream cut by the network, Finish on the diff",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "long_mixed"},
		cut:           true,
		want:          usageWant{gateway: inPR(1, reasonUnbound), voters: inPR(1, faultNone), replay: inPR(3, replayRaised)},
		replayVerdict: "pass",
	},
	{
		// The design's table has the claim above the output bound here, since
		// the parser drops the held-back argument. In this recording the
		// dropped object is 24 of 44 tokens and the client still gets 130
		// bytes, so the claim stays under twice the bytes and, by the
		// design's own rule, nothing is challenged.
		id: "H3", executor: "honest", shape: "cut by max_tokens inside a non-string tool argument",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "cut_in_object_arg"},
		want:          usageWant{},
		replayVerdict: "pass",
	},
	{
		id: "H5", executor: "honest", shape: "cut by max_tokens inside a multi-byte UTF-8 character",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "utf8_tail_cut"},
		want:          usageWant{},
		replayVerdict: "pass",
	},
	{
		id: "I2", executor: "inflating", shape: "honest output, the rest of the reservation moved into input_tokens",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "plain_short"},
		mlNode:        mockopenai.FaultPatch{UsagePromptTokens: intKnob(4400)},
		want:          usageWant{gateway: inPR(2, reasonInputHigh), voters: inPR(2, faultInflated), payment: inPR(2, paymentLost), invalid: inPR(2, invalidUp), replay: inPR(2, replayNone)},
		replayVerdict: "Inflated token counts.",
	},
	{
		id: "I3", executor: "inflating", shape: "cuts, then withholds the body",
		recording: usageRecording{"mlnode310", usageScenarioModel, "long_mixed"},
		cut:       true,
		body:      bodyRemoved,
		want:      usageWant{gateway: inPR(1, reasonUnbound), voters: inPR(1, faultUnavailable), payment: inPR(1, paymentLost), invalid: inPR(1, invalidUp), replay: inPR(1, replayNone)},
	},
	{
		id: "I4", executor: "inflating", shape: "cuts and stores a body that isn't the signed one",
		recording: usageRecording{"mlnode310", usageScenarioModel, "long_mixed"},
		cut:       true,
		body:      bodyReplaced,
		want:      usageWant{gateway: inPR(1, reasonUnbound), voters: inPR(1, faultHash), payment: inPR(1, paymentLost), invalid: inPR(1, invalidUp), replay: inPR(1, replayNone)},
	},
	{
		// The stored body is the long answer the executor made up: padded
		// with whitespace, self-consistent, signed. The voters see what an
		// honest cut looks like.
		id: "I5", executor: "inflating", shape: "streams a short text, cuts, stores a self-consistent long body and signs it",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "whitespace_padded"},
		cut:           true,
		want:          usageWant{gateway: inPR(1, reasonUnbound), voters: inPR(1, faultNone), replay: inPR(3, replayRaised)},
		replayVerdict: "pass",
	},
	{
		// 1500 claimed against 280 generated, under twice the 1007 bytes served.
		id: "I6", executor: "inflating", shape: "inflation under the byte bounds",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "long_mixed"},
		mlNode:        mockopenai.FaultPatch{UsageCompletionTokens: intKnob(1500)},
		want:          usageWant{},
		replayVerdict: "Inflated token counts.",
	},
	{
		// A real answer padded with whitespace the renderer counts honestly;
		// the positions match the claim.
		id: "I7", executor: "inflating", shape: "made-up long text with positions to match",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "whitespace_padded"},
		want:          usageWant{},
		replayVerdict: "pass",
	},
	{
		// The cut falls before the first content chunk, so the gateway gets
		// nothing while the executor stores the whole answer.
		id: "E1", executor: "honest", shape: "stream to the gateway lost before any content, tokens in the stored body",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "long_mixed"},
		cut:           true,
		cutEarly:      true,
		want:          usageWant{gateway: inPR(1, reasonEmpty), voters: inPR(1, faultNone), replay: inPR(3, replayRaised)},
		replayVerdict: "pass",
	},
	{
		// logit_bias +100 on a token that is one byte of a UTF-8 sequence
		// and decodes to nothing alone: 256 honest tokens, no text. For the
		// voters the content is the generated logprob positions the stored
		// body keeps (one per token, its token field the id), not the decoded
		// text, so they find all 256 and keep the payment.
		id: "E2", executor: "honest", shape: "client logit_bias +100 on a single-byte fragment, tokens but no text",
		recording:     usageRecording{"mlnode310", usageScenarioModel, "forced_fragment"},
		want:          usageWant{gateway: inPR(1, reasonEmpty), voters: inPR(1, faultNone)},
		replayVerdict: "pass",
	},
	{
		// The executor's signed Finish is applied, the stream carries no
		// text, and no host has the stored answer for the whole window.
		id: "E3", executor: "inflating", shape: "signed Finish, empty stream, no stored body",
		recording: usageRecording{"mlnode310", usageScenarioModel, "forced_fragment"},
		body:      bodyRemoved,
		want: usageWant{gateway: inPR(1, reasonEmpty), voters: inPR(1, faultUnavailable),
			payment: inPR(1, paymentLost), invalid: inPR(1, invalidUp), replay: inPR(1, replayNone)},
	},
	{
		// The ML node's answer keeps its usage and loses every token, so
		// the signed Finish claims tokens the stored body, whose hash
		// matches, holds no logprob position for.
		id: "E3", executor: "inflating", shape: "signed Finish, empty stream, stored body without generated positions",
		recording: usageRecording{"mlnode310", usageScenarioModel, "plain_short"},
		mlNode:    mockopenai.FaultPatch{DropTokens: boolKnob(true)},
		want: usageWant{gateway: inPR(1, reasonEmpty), voters: inPR(1, faultEmpty),
			payment: inPR(1, paymentLost), invalid: inPR(1, invalidUp), replay: inPR(1, replayNone)},
	},
}

// usageObservation is what a row measured.
type usageObservation struct {
	usageOutcome
	nonce         uint64
	slot          uint32
	httpStatus    int
	sawDone       bool
	servedBytes   int
	inputTokens   uint64
	outputTokens  uint64
	reservedCost  uint64
	actualCost    uint64
	status        string
	diffTxKinds   []string
	sampledVotes  uint32
	replayVerdict string
	timeline      []string
	// From the gateway's challenge_finished log and the voters' stores.
	outcome         string
	votes           int
	unanswered      int
	voteRows        int
	voteRowsSettled int
}

func TestUsageOverclaimScenarios(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	dataset, err := replay.LoadFile(usageScenarioReplayFile)
	require.NoError(t, err)
	stack, cfg, eps := harness.BootUsageScenarioStack(t, "citest-usage-scenarios-*", harness.UsageScenarioStackOpts{
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
	// A kept stack serves several runs and the gateway caches answers by
	// request body, so each run takes seeds of its own.
	seedBase := 1000 + int(time.Now().Unix()%1_000_000)*100
	t.Logf("row seeds from %d", seedBase)

	// Drive: all rows at once, each up to its Finish and its injection; a
	// row's ML node knobs are scoped to its seed. Settle: one shared wait
	// for the challenges of all rows. Observe: each row's cells, from its
	// own inference.
	adminKey := harness.TestenvAdminAPIKey
	harness.ResetMockOpenAIFault(t, client, eps.MockOpenAIHTTP)
	// Empty streams of earlier runs count toward a host's quarantine; the
	// table starts with every host available.
	harness.ClearGatewayParticipantQuarantines(t, client, eps.GatewayHTTP, adminKey, escrowID)
	ledgerBefore := harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey)
	metricsURL := harness.GatewayMetricsURL(eps)
	metricsBefore := harness.ParseMetricsText(harness.FetchMetricsText(t, client, metricsURL))
	mockBefore := harness.GetMockOpenAIStats(t, client, eps.MockOpenAIHTTP)
	runs := make([]*usageRun, len(usageScenarios))
	phase := time.Now()
	for i, row := range usageScenarios {
		runs[i] = &usageRun{row: row, name: row.id + " " + row.executor + ": " + row.shape, seed: seedBase + i}
	}
	t.Run("drive", func(t *testing.T) {
		t.Run("uncut", func(t *testing.T) {
			for _, run := range runs {
				if run.row.sequential() {
					continue
				}
				t.Run(run.name, func(t *testing.T) {
					t.Parallel()
					driveUsageScenario(t, stack, cfg, eps, escrowID, recordedSample(t, dataset, run.row.recording), run)
					run.driven = true
				})
			}
		})
		// A cut closes every stream the gateway holds and a stall pauses
		// the ML node for all requests, so these rows run one at a time,
		// after the others.
		for _, run := range runs {
			if !run.row.sequential() {
				continue
			}
			// The gateway quarantines the executor of a cut or stalled
			// stream; without a reset the third cut leaves no host to
			// serve the next row.
			harness.ClearGatewayParticipantQuarantines(t, client, eps.GatewayHTTP, adminKey, escrowID)
			t.Run(run.name, func(t *testing.T) {
				driveUsageScenario(t, stack, cfg, eps, escrowID, recordedSample(t, dataset, run.row.recording), run)
				run.driven = true
			})
		}
	})
	t.Logf("phase drive: %s", time.Since(phase).Round(time.Second))
	phase = time.Now()
	txs := settleUsageScenarios(t, stack, cfg, eps, escrowID, runs)
	t.Logf("phase settle: %s", time.Since(phase).Round(time.Second))
	finished := gatewayChallengesFinished(t, stack, escrowID)
	// Counters the gateway and the mock keep only in total.
	metricsAfter := harness.ParseMetricsText(harness.FetchMetricsText(t, client, metricsURL))
	mockAfter := harness.GetMockOpenAIStats(t, client, eps.MockOpenAIHTTP)
	// The metric labels the reason in lower case.
	challenges := map[string]float64{}
	for _, reason := range gatewayReasons {
		challenges[reason] = harness.MetricDelta(metricsBefore, metricsAfter, gatewayChallengesMetric, map[string]string{"reason": strings.ToLower(reason)})
	}
	t.Logf("table: served bindings bound+%.0f mismatch+%.0f | gateway challenges %v | mock-openai requests +%d replay hits +%d misses +%d",
		harness.MetricDelta(metricsBefore, metricsAfter, "devshard_gateway_served_bindings_total", map[string]string{"verdict": "bound"}),
		harness.MetricDelta(metricsBefore, metricsAfter, "devshard_gateway_served_bindings_total", map[string]string{"verdict": "mismatch"}),
		challenges, mockAfter.RequestsReceived-mockBefore.RequestsReceived, mockAfter.ReplayHits-mockBefore.ReplayHits, mockAfter.ReplayMisses-mockBefore.ReplayMisses)
	phase = time.Now()

	invalidRows, challengedRows := 0, 0
	for _, run := range runs {
		row := run.row
		t.Run(run.name, func(t *testing.T) {
			if !run.driven {
				t.Skip("the row's drive failed")
			}
			got := observeUsageScenario(t, stack, cfg, eps, escrowID, run, txs, finished)
			if got.invalid == invalidUp {
				invalidRows++
			}
			if got.challenge != reasonNone {
				challengedRows++
			}
			t.Logf("%s %s | %s | recording %s | gateway: challenge=%q outcome=%q votes=%d unanswered=%d served_bytes=%d | "+
				"voters: fault=%q diff_txs=%v vote_rows=%d settled=%d | "+
				"payment: status=%s %s claim in=%d out=%d reserved=%d actual=%d | invalid %s | "+
				"replay=%s sampled_votes=%d replay_if_sampled=%q | http=%d done=%t nonce=%d slot=%d | timeline=%v",
				row.id, row.executor, row.shape, row.recording,
				got.challenge, got.outcome, got.votes, got.unanswered, got.servedBytes,
				got.fault, got.diffTxKinds, got.voteRows, got.voteRowsSettled,
				got.status, got.payment, got.inputTokens, got.outputTokens, got.reservedCost, got.actualCost, got.invalid,
				got.replay, got.sampledVotes, got.replayVerdict, got.httpStatus, got.sawDone, got.nonce, got.slot, got.timeline)

			assert.Zero(t, got.sampledVotes, "sampled replay voted on the row's inference; the row no longer shows the challenge path alone")
			if row.replayVerdict != "" {
				assert.Equal(t, row.replayVerdict, got.replayVerdict, "today's replay check, if it sampled the inference")
			}
			assertCell(t, "gateway: challenge reason", row.want.gateway, todayCells.challenge, got.challenge)
			assertCell(t, "voters: fault", row.want.voters, todayCells.fault, got.fault)
			assertCell(t, "payment", row.want.payment, todayCells.payment, got.payment)
			assertCell(t, "executor hs.Invalid", row.want.invalid, todayCells.invalid, got.invalid)
			assertCell(t, "replay", row.want.replay, todayCells.replay, got.replay)
			if got.challenge != reasonNone {
				// Every voter fetched from its own store and kept its vote. The
				// gateway collects votes until the outcome is settled: all of
				// them when the faults pass the threshold, and for an honest
				// body as many as it takes before the rest could not lift the
				// faults past it, so a none outcome may leave a vote uncollected.
				assert.Equal(t, usageVoters, got.votes+got.unanswered, "voters asked")
				if got.outcome == "invalidated" {
					assert.Equal(t, usageVoters, got.votes, "votes in MsgChallenge")
				} else {
					assert.GreaterOrEqual(t, got.votes, 1, "votes in MsgChallenge")
				}
				assert.Equal(t, usageVoters, got.voteRows, "rows of devshard_challenge_votes over the voters' stores")
				assert.Equal(t, usageVoters, got.voteRowsSettled, "settled rows of devshard_challenge_votes")
			}
		})
	}
	t.Logf("phase observe: %s", time.Since(phase).Round(time.Second))
	// Every challenge of the table is counted, under the executor's
	// participant key. A request can make more than one, as with Invalid.
	counted := 0.0
	for _, n := range challenges {
		counted += n
	}
	assert.GreaterOrEqual(t, counted, float64(challengedRows), "challenges counted by the gateway against the challenged rows")
	assert.Zero(t, harness.MetricDelta(metricsBefore, metricsAfter, gatewayChallengesMetric, map[string]string{"participant_key": "unknown"}),
		"challenges counted without the executor's participant key")

	// A row reads its Invalid from its own record, since the rows settle
	// together; the hosts' counters must add up to the invalidated
	// inferences of the table. A request can make more than one: each
	// attempt of a row whose ML node misbehaves for every executor.
	invalidated := 0
	seen := map[uint64]bool{}
	for _, tx := range txs {
		id, ok := tx.InferenceID()
		if !ok || seen[id] || tx.Kind != "startInference" {
			continue
		}
		seen[id] = true
		if rec, ok := harness.GetGatewayInferenceRecord(t, client, eps.GatewayHTTP, adminKey, id); ok && rec.Status == "invalidated" {
			invalidated++
		}
	}
	require.GreaterOrEqual(t, invalidated, invalidRows)
	ledgerAfter := harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey)
	hostInvalid := 0
	for slot, after := range ledgerAfter.HostStats {
		hostInvalid += int(after.Invalid) - int(ledgerBefore.HostStats[slot].Invalid)
	}
	assert.Equal(t, invalidated, hostInvalid, "hs.Invalid added over all hosts against the invalidated inferences of the table")
}

// usageRun is a row on its way through the three phases.
type usageRun struct {
	row    usageScenario
	name   string
	seed   int
	obs    usageObservation
	driven bool

	// finished is when the drive saw the row's Finish.
	finished time.Time
}

func recordedSample(t *testing.T, dataset *replay.Dataset, rec usageRecording) replay.Sample {
	t.Helper()
	require.Equal(t, "vllm_recorded_"+rec.build+".jsonl", filepath.Base(usageScenarioReplayFile), "one build per stack")
	for i := 0; i < dataset.Len(); i++ {
		sample, ok := dataset.Sample(i)
		require.True(t, ok)
		if sample.Model == rec.model && sample.Name == rec.name {
			require.NotEmpty(t, sample.ClientRequest, "%s keeps no client request", rec)
			return sample
		}
	}
	t.Fatalf("no recording %s", rec)
	return replay.Sample{}
}

// clientRequestWithSeed is the recording's client request under its own
// seed. The seed stays out of the recording's key, so the ML node still
// answers with the recording, while the gateway's response cache, keyed by
// the client body, sees a new request for every row.
func clientRequestWithSeed(t *testing.T, sample replay.Sample, seed int) []byte {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(sample.ClientRequest, &body))
	body["seed"] = seed
	out, err := json.Marshal(body)
	require.NoError(t, err)
	return out
}

// driveUsageScenario sends the row's request with its ML node knobs scoped
// to its seed, waits for its Finish and applies the row's change to the
// stored body. It runs alongside the other rows.
func driveUsageScenario(t *testing.T, stack *harness.Stack, cfg *config.File, eps harness.Endpoints, escrowID string, sample replay.Sample, run *usageRun) {
	t.Helper()
	row := run.row
	client := harness.GatewayChatClient()
	adminKey := harness.TestenvAdminAPIKey
	if row.mlNode != (mockopenai.FaultPatch{}) {
		knobs := row.mlNode
		knobs.Seed = &run.seed
		harness.PatchMockOpenAIFault(t, client, eps.MockOpenAIHTTP, knobs)
		defer harness.PatchMockOpenAIFault(t, client, eps.MockOpenAIHTTP, mockopenai.FaultPatch{Seed: &run.seed})
	}
	if row.body != bodyKept {
		// The operator's storage goes wrong before the Finish is signed:
		// changed after it, a voter may already have fetched the true body.
		harness.ArmStoredPayloadTamper(t, stack, cfg, escrowID, run.seed, row.body == bodyReplaced)
		defer harness.DisarmStoredPayloadTamper(t, stack, cfg, run.seed)
	}

	harness.Step(t, "%s: stream %s through the gateway", row.id, row.recording)
	body := clientRequestWithSeed(t, sample, run.seed)
	var resp harness.GatewayChatHTTPResult
	start := time.Now()
	var cutAt time.Time
	switch {
	case row.cut:
		// The cut acts on every stream at once: a cut row cannot run
		// alongside others.
		resp, cutAt = postAndCut(t, stack, client, eps, body, row.cutEarly)
	case row.stall:
		resp, cutAt = postAndStall(t, stack, client, eps, body)
	default:
		// As an ordinary client: an admin key lifts the max_tokens cap.
		var err error
		resp, err = postGatewayChatRawSoft(client, eps.GatewayHTTP, body)
		require.NoError(t, err)
	}
	chunks, sawDone := harness.ParseSSEDataChunks(resp.Body)
	obs := &run.obs
	obs.httpStatus, obs.sawDone, obs.servedBytes = resp.Status, sawDone, servedTextBytes(chunks)
	if resp.Status >= 300 {
		// The row still follows its inference: the client's answer is not
		// what the design decides on.
		t.Logf("%s: gateway answered %d: %s", row.id, resp.Status, resp.Body)
	}

	if requestID := resp.Header.Get("X-Request-Id"); requestID != "" {
		accounting := harness.WaitGatewayRequestWinner(t, client, eps.GatewayHTTP, adminKey, escrowID, requestID, time.Minute)
		obs.nonce = accounting.WinnerNonce
		if obs.nonce == 0 {
			// A request whose winner failed after streaming names no winner
			// in its accounting. The row's inference is the attempt the
			// client was reading: the one the gateway selected.
			obs.nonce = gatewayWinnerSelected(t, stack, requestID)
		}
		if obs.nonce == 0 {
			obs.nonce = accounting.Nonce()
		}
	} else {
		// A request that fails before its stream starts gets no
		// X-Request-Id; its attempts are the stored prompts with its seed,
		// and the first of them is the row's inference.
		harness.AssertEventually(t, time.Minute, time.Second, func() bool {
			ids := harness.StoredInferenceIDs(t, stack, cfg, escrowID, run.seed)
			if len(ids) > 0 {
				obs.nonce = ids[0]
			}
			return len(ids) > 0
		})
		require.NotZero(t, obs.nonce, "no stored prompt with seed %d", run.seed)
	}
	since := func() string {
		if !cutAt.IsZero() {
			return fmt.Sprintf("cut+%.0fs", time.Since(cutAt).Seconds())
		}
		return fmt.Sprintf("+%.0fs", time.Since(start).Seconds())
	}

	harness.Step(t, "%s: wait for inference %d to finish", row.id, obs.nonce)
	var rec harness.GatewayInferenceRecord
	lastStatus := ""
	wait := time.Minute
	if row.sequential() {
		// The Finish of a cut or stalled stream reaches the gateway later,
		// at the latest when the execution deadline makes it ask the group.
		wait += 3 * time.Minute
	}
	harness.AssertEventually(t, wait, time.Second, func() bool {
		var ok bool
		rec, ok = harness.GetGatewayInferenceRecord(t, client, eps.GatewayHTTP, adminKey, obs.nonce)
		if ok && rec.Status != lastStatus {
			lastStatus = rec.Status
			obs.timeline = append(obs.timeline, rec.Status+"@"+since())
		}
		return ok && rec.Status != "pending" && rec.Status != "started"
	})
	require.NotEqual(t, "pending", rec.Status, "inference %d never started", obs.nonce)
	require.NotEqual(t, "started", rec.Status, "inference %d never finished", obs.nonce)
	run.finished = time.Now()
	obs.replayVerdict = replayVerdictToday(t, eps.MockOpenAIHTTP, sample, run.seed, rec.InputTokens, rec.OutputTokens)
}

// settleUsageScenarios waits, once for the whole table, until every driven
// row whose landed cells expect a challenge has its challenge tx on the
// diff log or a terminal record, and no longer than usageChallengeWindow
// after the last Finish. It returns the diff log of the rows' nonces.
func settleUsageScenarios(t *testing.T, stack *harness.Stack, cfg *config.File, eps harness.Endpoints, escrowID string, runs []*usageRun) []harness.DiffTx {
	t.Helper()
	client := harness.GatewayChatClient()
	adminKey := harness.TestenvAdminAPIKey
	var from uint64
	var lastFinish time.Time
	var waiting []*usageRun
	for _, run := range runs {
		if !run.driven {
			continue
		}
		if from == 0 || run.obs.nonce < from {
			from = run.obs.nonce
		}
		if run.finished.After(lastFinish) {
			lastFinish = run.finished
		}
		if run.row.want.gateway.now(todayCells.challenge) != reasonNone {
			waiting = append(waiting, run)
		}
	}
	if from == 0 {
		return nil
	}
	// The gateway sends a diff to one host and the others catch up by gossip,
	// so one host's log may lag; the longest log is the furthest along.
	fetch := func() []harness.DiffTx {
		latest := harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey).LatestNonce
		var longest []harness.DiffTx
		for _, host := range cfg.Hosts {
			if txs := harness.HostDiffTxs(t, stack, cfg, host.ID, escrowID, from, latest); len(txs) > len(longest) {
				longest = txs
			}
		}
		return longest
	}
	harness.Step(t, "settle: wait for the challenges of %d rows", len(waiting))
	for {
		txs := fetch()
		challenged := map[uint64]bool{}
		for _, tx := range txs {
			if id, ok := tx.InferenceID(); ok && tx.Kind == challengeTxKind {
				challenged[id] = true
			}
		}
		pending := 0
		for _, run := range waiting {
			if challenged[run.obs.nonce] {
				continue
			}
			if rec, ok := harness.GetGatewayInferenceRecord(t, client, eps.GatewayHTTP, adminKey, run.obs.nonce); ok && rec.Status != "finished" {
				continue
			}
			pending++
		}
		if pending == 0 {
			return txs
		}
		if time.Since(lastFinish) >= usageChallengeWindow {
			t.Logf("settle: %d of %d expected challenges missing %s after the last Finish", pending, len(waiting), usageChallengeWindow)
			return txs
		}
		time.Sleep(2 * time.Second)
	}
}

// observeUsageScenario reads the row's cells from its own inference: its
// record, the diff log txs that name it, the gateway's challenge log and the
// voters' stores.
func observeUsageScenario(t *testing.T, stack *harness.Stack, cfg *config.File, eps harness.Endpoints, escrowID string, run *usageRun, txs []harness.DiffTx, finished map[uint64]challengeFinished) usageObservation {
	t.Helper()
	client := harness.GatewayChatClient()
	obs := run.obs
	rec, ok := harness.GetGatewayInferenceRecord(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, obs.nonce)
	require.True(t, ok, "no record of inference %d", obs.nonce)
	obs.slot = rec.ExecutorSlot
	obs.status = rec.Status
	obs.inputTokens, obs.outputTokens = rec.InputTokens, rec.OutputTokens
	obs.reservedCost, obs.actualCost = rec.ReservedCost, rec.ActualCost
	obs.payment = paymentLost
	if rec.Status == "finished" && rec.ActualCost > 0 {
		obs.payment = paymentStands
	}
	// The executor's Invalid comes with the invalidation of the inference.
	obs.invalid = invalidSame
	if rec.Status == "invalidated" {
		obs.invalid = invalidUp
	}
	obs.sampledVotes = rec.VotesValid + rec.VotesInvalid

	challenged := false
	for _, tx := range txs {
		if id, ok := tx.InferenceID(); !ok || id != obs.nonce {
			continue
		}
		obs.diffTxKinds = append(obs.diffTxKinds, fmt.Sprintf("%d:%s", tx.Nonce, tx.Kind))
		if tx.Kind == challengeTxKind {
			challenged = true
			obs.fault = challengeFault(tx.Body)
		}
		if tx.Kind == "validation" || tx.Kind == "validationVote" {
			obs.sampledVotes++
		}
	}
	// MsgChallenge does not carry the reason; the gateway logs it.
	obs.challenge = finished[obs.nonce].reason
	obs.outcome, obs.votes, obs.unanswered = finished[obs.nonce].outcome, finished[obs.nonce].votes, finished[obs.nonce].unanswered
	if challenged {
		obs.voteRows, obs.voteRowsSettled = harness.ChallengeVoteRows(t, stack, cfg, escrowID, obs.nonce)
	}
	switch {
	case rec.Status == "invalidated":
		obs.replay = replayNone
	case challenged && obs.sampledVotes > 0:
		// The challenge mark raised the rate: the inference went to replay.
		obs.replay = replayRaised
	default:
		obs.replay = replaySampled
	}
	return obs
}

// postAndCut streams body through the gateway while the ML node holds the
// answer after its first token, closes the gateway's connections to the
// hosts, then lets the ML node finish. The executor's run outlives the request, so
// it stores and signs the whole answer the gateway never received.
func postAndCut(t *testing.T, stack *harness.Stack, client *http.Client, eps harness.Endpoints, body []byte, early bool) (harness.GatewayChatHTTPResult, time.Time) {
	t.Helper()
	pause := true
	patch := mockopenai.FaultPatch{PauseStream: &pause}
	if early {
		// The first token waits past the cut; the run goes on after it.
		ttft := 5000
		patch = mockopenai.FaultPatch{StreamTTFTMs: &ttft}
		defer func() {
			zero := 0
			harness.PatchMockOpenAIFault(t, client, eps.MockOpenAIHTTP, mockopenai.FaultPatch{StreamTTFTMs: &zero})
		}()
	}
	harness.PatchMockOpenAIFault(t, client, eps.MockOpenAIHTTP, patch)
	before := harness.GetMockOpenAIStats(t, client, eps.MockOpenAIHTTP).RequestsReceived
	done := make(chan harness.GatewayChatHTTPResult, 1)
	go func() {
		result, err := postGatewayChatRawSoft(client, eps.GatewayHTTP, body)
		if err != nil {
			result.Body = []byte(err.Error())
		}
		done <- result
	}()
	require.True(t, harness.AssertEventually(t, time.Minute, 100*time.Millisecond, func() bool {
		return harness.GetMockOpenAIStats(t, client, eps.MockOpenAIHTTP).RequestsReceived > before
	}), "the executor never reached the ML node")
	// Let the first token reach the gateway and the client.
	time.Sleep(time.Second)
	peers := harness.CutGatewayStreams(t, stack)
	cutAt := time.Now()
	t.Logf("cut the gateway's connections to %s", peers)
	harness.ReleaseMockOpenAIStreams(t, client, eps.MockOpenAIHTTP)
	select {
	case result := <-done:
		return result, cutAt
	case <-time.After(2 * time.Minute):
		t.Fatal("the gateway kept the client stream open two minutes after the cut")
	}
	return harness.GatewayChatHTTPResult{}, cutAt
}

// postAndStall streams body through the gateway while the ML node holds the
// answer after its first token, waits for the gateway to cancel the attempt
// at its hard timeout for a streaming attempt, then lets the ML node finish.
// As after a cut, the executor's run outlives the request, so it stores and
// signs the whole answer the gateway never received. The executor gives its
// ML node the execution timeout, 15 s on this stack, so the release must
// come well inside it.
func postAndStall(t *testing.T, stack *harness.Stack, client *http.Client, eps harness.Endpoints, body []byte) (harness.GatewayChatHTTPResult, time.Time) {
	t.Helper()
	redundancy := func(timeoutMS int64) map[string]any {
		return map[string]any{"redundancy": map[string]any{"streaming_attempt_hard_timeout_ms": timeoutMS}}
	}
	harness.PatchGatewayAdminSettings(t, client, eps.GatewayHTTP, redundancy(stallHardTimeout.Milliseconds()))
	// Back to the gateway's default.
	defer harness.PatchGatewayAdminSettings(t, client, eps.GatewayHTTP, redundancy(1_800_000))
	pause := true
	harness.PatchMockOpenAIFault(t, client, eps.MockOpenAIHTTP, mockopenai.FaultPatch{PauseStream: &pause})
	const cancelStage = "winner_stream_hard_timeout"
	cancelled := func() map[uint64]bool {
		seen := map[uint64]bool{}
		for _, entry := range gatewayLogStages(t, stack, cancelStage) {
			seen[entry.Nonce] = true
		}
		return seen
	}
	before := cancelled()
	done := make(chan harness.GatewayChatHTTPResult, 1)
	go func() {
		result, err := postGatewayChatRawSoft(client, eps.GatewayHTTP, body)
		if err != nil {
			result.Body = []byte(err.Error())
		}
		done <- result
	}()
	var nonce uint64
	require.True(t, harness.AssertEventually(t, 30*time.Second, 500*time.Millisecond, func() bool {
		for n := range cancelled() {
			if !before[n] {
				nonce = n
				return true
			}
		}
		return false
	}), "the gateway never cancelled the stalled attempt")
	cutAt := time.Now()
	t.Logf("the gateway cancelled the stalled attempt of inference %d", nonce)
	harness.ReleaseMockOpenAIStreams(t, client, eps.MockOpenAIHTTP)
	select {
	case result := <-done:
		return result, cutAt
	case <-time.After(2 * time.Minute):
		t.Fatal("the gateway kept the client stream open two minutes after cancelling the attempt")
	}
	return harness.GatewayChatHTTPResult{}, cutAt
}

// postGatewayChatRawSoft posts body as is to /v1/chat/completions as an
// ordinary streaming client and returns the raw response; a stream that ends
// in an error is reported, not a test failure.
func postGatewayChatRawSoft(client *http.Client, gatewayURL string, body []byte) (harness.GatewayChatHTTPResult, error) {
	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return harness.GatewayChatHTTPResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return harness.GatewayChatHTTPResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	result := harness.GatewayChatHTTPResult{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: raw, Header: resp.Header.Clone()}
	return result, err
}

// challengeFault is what the votes of a MsgChallenge found: the first fault
// other than NONE, or NONE when every vote found nothing. protojson leaves a
// vote's fault out when it is NONE, the enum's zero value.
func challengeFault(body map[string]any) string {
	votes, _ := body["votes"].([]any)
	found := faultNotCalled
	for _, v := range votes {
		vote, _ := v.(map[string]any)
		raw, _ := vote["fault"].(string)
		found = faultNone
		for _, name := range voterFaults {
			if name != faultNone && strings.HasSuffix(raw, "_"+name) {
				return name
			}
		}
	}
	return found
}

// challengeFinished is one challenge_finished line of the gateway's log.
type challengeFinished struct {
	reason     string
	outcome    string
	votes      int
	unanswered int
}

// gatewayChallengesFinished maps each inference of escrowID the gateway
// challenged to what its challenge_finished log line says.
func gatewayChallengesFinished(t *testing.T, stack *harness.Stack, escrowID string) map[uint64]challengeFinished {
	t.Helper()
	finished := map[uint64]challengeFinished{}
	for _, entry := range gatewayLogStages(t, stack, "challenge_finished") {
		if entry.Escrow != escrowID {
			continue
		}
		finished[entry.Nonce] = challengeFinished{reason: strings.ToUpper(entry.Reason), outcome: entry.Outcome, votes: entry.Votes, unanswered: entry.Unanswered}
	}
	return finished
}

// gatewayLogStage is a stage line of the gateway's log, as far as the table
// reads it.
type gatewayLogStage struct {
	Msg        string `json:"msg"`
	RequestID  string `json:"request_id"`
	Escrow     string `json:"escrow"`
	Nonce      uint64 `json:"nonce"`
	Reason     string `json:"reason"`
	Outcome    string `json:"outcome"`
	Votes      int    `json:"votes"`
	Unanswered int    `json:"unanswered"`
}

// gatewayLogStages lists the gateway's log lines with msg, oldest first.
func gatewayLogStages(t *testing.T, stack *harness.Stack, msg string) []gatewayLogStage {
	t.Helper()
	out, err := stack.ComposeLogsAll("devshardctl")
	require.NoError(t, err)
	needle := `"` + msg + `"`
	var stages []gatewayLogStage
	for _, line := range strings.Split(out, "\n") {
		i := strings.IndexByte(line, '{')
		if i < 0 || !strings.Contains(line, needle) {
			continue
		}
		var entry gatewayLogStage
		if json.Unmarshal([]byte(line[i:]), &entry) != nil || entry.Msg != msg {
			continue
		}
		stages = append(stages, entry)
	}
	return stages
}

// gatewayWinnerSelected is the attempt the gateway selected as the winner of
// requestID, or 0 when it never selected one.
func gatewayWinnerSelected(t *testing.T, stack *harness.Stack, requestID string) uint64 {
	t.Helper()
	for _, entry := range gatewayLogStages(t, stack, "winner_selected") {
		if entry.RequestID == requestID {
			return entry.Nonce
		}
	}
	return 0
}

// gatewayLoggedStage reports whether the gateway logged msg for the inference.
func gatewayLoggedStage(t *testing.T, stack *harness.Stack, nonce uint64, msg string) bool {
	t.Helper()
	for _, entry := range gatewayLogStages(t, stack, msg) {
		if entry.Nonce == nonce {
			return true
		}
	}
	return false
}

// servedTextBytes counts the bytes of text the client received on choice 0:
// every string field of delta that carries generated text, decoded.
func servedTextBytes(chunks []map[string]any) int {
	n := 0
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		for _, field := range []string{"content", "reasoning", "reasoning_content"} {
			if s, ok := delta[field].(string); ok {
				n += len(s)
			}
		}
		calls, _ := delta["tool_calls"].([]any)
		for _, call := range calls {
			fn, _ := call.(map[string]any)["function"].(map[string]any)
			for _, field := range []string{"name", "arguments"} {
				if s, ok := fn[field].(string); ok {
					n += len(s)
				}
			}
		}
	}
	return n
}

// replayVerdictToday runs the validator's replay check on the row's answer
// as the executor stored it: the recorded request goes to the stack's mock
// with the row's ML node behaviour, the executor's response processor builds
// the stored body, and the replay against the same mock gets the honest
// recording, as another host's ML node would answer.
func replayVerdictToday(t *testing.T, mockOpenAIURL string, sample replay.Sample, seed int, claimedInput, claimedOutput uint64) string {
	t.Helper()
	client := harness.GatewayChatClient()
	post := func(_ context.Context, body []byte) (*http.Response, error) {
		return client.Post(mockOpenAIURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	}
	// Under the row's seed the mock answers as the row's ML node did.
	var request map[string]any
	require.NoError(t, json.Unmarshal(sample.Request, &request))
	request["seed"] = seed
	mlRequest, err := json.Marshal(request)
	require.NoError(t, err)
	resp, err := post(context.Background(), mlRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	processor := completionapi.NewExecutorResponseProcessor("usage-scenario", true)
	require.NoError(t, completionapi.ProcessHTTPResponse(resp, processor))
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)

	result, err := validation.ExecuteValidation(context.Background(), "usage-scenario", sample.Request, stored, post,
		claimedInput, claimedOutput, "processed_logprobs", 0)
	require.NoError(t, err)
	if result.IsSuccessful() {
		return "pass"
	}
	if invalid, ok := result.(*validation.InvalidInferenceResult); ok {
		return invalid.Reason
	}
	return fmt.Sprintf("%T", result)
}
