package e2e

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"
)

const (
	parityRequestCount  = 6
	parityBurstSize     = 12
	paritySettleWithin  = 30 * time.Second
	parityTextLimit     = 160
	parityClientTimeout = 45 * time.Second
	parityDriverLegacy  = "devshardctl"
	parityDriverGateway = "gateway"
)

// parityRequest is one request both drivers receive, named so their answers can be paired.
type parityRequest struct {
	name string
	body map[string]any
}

// parityScenario is one stand configuration and the traffic both drivers are put through.
type parityScenario struct {
	name             string
	hostEnvOverrides map[int]map[string]string
	mockChainParams  map[string]any
	stoppedHosts     []int
	requests         []parityRequest
	concurrent       bool
	expectServed     bool
	slow             bool
}

// parityReply is what one driver answered to one request; received is the body the host saw, when the host echoes it.
type parityReply struct {
	request  string
	status   int
	text     string
	received map[string]any
}

// parityObservation is what one driver did with a scenario's traffic.
type parityObservation struct {
	driver       string
	replies      []parityReply
	trafficTime  time.Duration
	balanceSpent uint64
	noncesSpent  uint64
	ledger       testutil.AccountingParticipantsResponse
}

// requireParityE2E keeps the comparison out of the default suite: it boots the stand twice per scenario.
func requireParityE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("DEVSHARD_E2E_PARITY") != "1" {
		t.Skip("set DEVSHARD_E2E_PARITY=1 to compare devshardctl and the gateway on the same stand")
	}
}

// repeatedRequests names count requests of one body shape.
func repeatedRequests(prefix string, count int, build func(content string) map[string]any) []parityRequest {
	requests := make([]parityRequest, 0, count)
	for index := range count {
		name := fmt.Sprintf("%s %d", prefix, index)
		requests = append(requests, parityRequest{name: name, body: build(name)})
	}
	return requests
}

func nonStreamingBody(content string) map[string]any {
	return testutil.ChatCompletionBody(content, false)
}

func streamingBody(content string) map[string]any { return testutil.ChatCompletionBody(content, true) }

func toolBody(content string) map[string]any { return testutil.ToolCompletionBody(content, false) }

// normalizationRequests are the body shapes whose forwarding to the host the two drivers may disagree on.
func normalizationRequests() []parityRequest {
	withoutMaxTokens := testutil.ChatCompletionBody("without max_tokens", false)
	delete(withoutMaxTokens, "max_tokens")
	pastTheCap := testutil.ChatCompletionBody("max_tokens past the cap", false)
	pastTheCap["max_tokens"] = 1_000_000
	sampling := testutil.ChatCompletionBody("sampling parameters", false)
	sampling["temperature"] = 0.3
	sampling["top_p"] = 0.9
	sampling["seed"] = 7
	sampling["stop"] = []string{"END"}
	conversation := testutil.ChatCompletionBody("", false)
	conversation["messages"] = []map[string]string{
		{"role": "system", "content": "answer briefly"},
		{"role": "user", "content": "first question"},
		{"role": "assistant", "content": "first answer"},
		{"role": "user", "content": "second question"},
	}
	return []parityRequest{
		{name: "plain", body: testutil.ChatCompletionBody("plain", false)},
		{name: "streaming", body: testutil.ChatCompletionBody("streaming", true)},
		{name: "tools", body: testutil.ToolCompletionBody("tools", false)},
		{name: "without max_tokens", body: withoutMaxTokens},
		{name: "sampling parameters", body: sampling},
		{name: "multi-turn conversation", body: conversation},
		{name: "max_tokens past the cap", body: pastTheCap},
	}
}

// hostsCarrying gives the named hosts the same stub settings.
func hostsCarrying(settings map[string]string, hostIndexes ...int) map[int]map[string]string {
	overrides := make(map[int]map[string]string, len(hostIndexes))
	for _, index := range hostIndexes {
		overrides[index] = settings
	}
	return overrides
}

func everyHost() []int { return []int{0, 1, 2} }

// parityScenarios is every fault the stand can put a host in, each driven through both drivers.
func parityScenarios() []parityScenario {
	shortDeadlines := map[string]any{"refusal_timeout": 5, "execution_timeout": 10}
	unavailable := map[string]string{e2econfig.StubInferenceHTTPStatusEnv: "503"}
	internalError := map[string]string{
		e2econfig.StubInferenceHTTPStatusEnv:  "500",
		e2econfig.StubInferenceHTTPMessageEnv: "stub host internal error",
	}
	toolChoiceRejected := map[string]string{
		e2econfig.StubInferenceHTTPStatusEnv:  "400",
		e2econfig.StubInferenceHTTPMessageEnv: testutil.ToolChoiceUnsupportedMessage,
	}
	streamBreaks := map[string]string{e2econfig.StubInferenceSSEErrorEnv: "stub stream broke"}
	return []parityScenario{
		{name: "healthy non-streaming", requests: repeatedRequests("healthy", parityRequestCount, nonStreamingBody), expectServed: true},
		{name: "healthy streaming", requests: repeatedRequests("streaming", parityRequestCount, streamingBody), expectServed: true},
		{name: "concurrent burst", requests: repeatedRequests("burst", parityBurstSize, nonStreamingBody), concurrent: true, expectServed: true},
		{
			name:             "request normalization",
			hostEnvOverrides: hostsCarrying(map[string]string{e2econfig.StubInferenceEchoRequestEnv: "1"}, everyHost()...),
			requests:         normalizationRequests(),
			expectServed:     true,
		},
		{name: "one host answers 503", hostEnvOverrides: hostsCarrying(unavailable, 1), requests: repeatedRequests("one 503", parityRequestCount, nonStreamingBody), expectServed: true},
		{name: "one host answers 500", hostEnvOverrides: hostsCarrying(internalError, 1), requests: repeatedRequests("one 500", parityRequestCount, nonStreamingBody), expectServed: true},
		{name: "every host answers 503", hostEnvOverrides: hostsCarrying(unavailable, everyHost()...), requests: repeatedRequests("every 503", parityRequestCount, nonStreamingBody)},
		{name: "every host rejects tool choice", hostEnvOverrides: hostsCarrying(toolChoiceRejected, everyHost()...), requests: repeatedRequests("tool choice", parityRequestCount, toolBody)},
		{name: "one host breaks its stream", hostEnvOverrides: hostsCarrying(streamBreaks, 1), requests: repeatedRequests("one broken stream", parityRequestCount, streamingBody), expectServed: true},
		{name: "every host breaks its stream", hostEnvOverrides: hostsCarrying(streamBreaks, everyHost()...), requests: repeatedRequests("every broken stream", parityRequestCount, streamingBody)},
		{
			name:             "one host infers slowly",
			hostEnvOverrides: hostsCarrying(map[string]string{e2econfig.StubInferenceDelayMillisEnv: "3000"}, 1),
			requests:         repeatedRequests("one slow", parityRequestCount, nonStreamingBody),
			expectServed:     true,
		},
		{
			name:             "every host infers slowly",
			hostEnvOverrides: hostsCarrying(map[string]string{e2econfig.StubInferenceDelayMillisEnv: "1500"}, everyHost()...),
			requests:         repeatedRequests("every slow", parityRequestCount, nonStreamingBody),
			expectServed:     true,
		},
		{
			name:             "one host receipts slowly",
			hostEnvOverrides: hostsCarrying(map[string]string{e2econfig.ReceiptDelayMillisEnv: "2000"}, 1),
			requests:         repeatedRequests("slow receipt", parityRequestCount, nonStreamingBody),
			expectServed:     true,
		},
		{
			name:            "one host is down",
			mockChainParams: shortDeadlines,
			stoppedHosts:    []int{1},
			requests:        repeatedRequests("host down", parityRequestCount, nonStreamingBody),
			expectServed:    true,
			slow:            true,
		},
		{
			name:             "one host hangs after its receipt",
			mockChainParams:  shortDeadlines,
			hostEnvOverrides: hostsCarrying(map[string]string{e2econfig.StubInferenceDelayMillisEnv: "600000"}, 1),
			requests:         repeatedRequests("hung host", parityRequestCount, nonStreamingBody),
			expectServed:     true,
			slow:             true,
		},
	}
}

// observeDriver runs a scenario in a subtest of its own, so one driver's stand is gone before the next one starts.
func observeDriver(t *testing.T, isGateway bool, scenario parityScenario) parityObservation {
	t.Helper()
	observation := parityObservation{driver: parityDriverLegacy}
	if isGateway {
		observation.driver = parityDriverGateway
	}
	completed := t.Run(observation.driver, func(t *testing.T) {
		options := e2eEnvOptions{hostEnvOverrides: scenario.hostEnvOverrides, mockChainParams: scenario.mockChainParams}
		var env *e2eEnv
		var client *http.Client
		if isGateway {
			env, client = startGatewayEnv(t, options)
		} else {
			env, client = startNonStreamingEnvWithOptions(t, options)
		}
		client.Timeout = parityClientTimeout
		for _, index := range scenario.stoppedHosts {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			env.stopHost(ctx, t, index)
			cancel()
		}
		balanceBefore, nonceBefore := testutil.EscrowSessionState(t, client, env.clientURL, defaultEscrowID, isGateway)
		trafficStarted := time.Now()
		observation.replies = sendParityTraffic(t, client, env.clientURL, scenario)
		observation.trafficTime = time.Since(trafficStarted).Round(time.Millisecond)
		served := slices.ContainsFunc(observation.replies, func(reply parityReply) bool { return reply.status == http.StatusOK })
		if scenario.expectServed {
			require.True(t, served, "%s served none of the traffic: %+v", observation.driver, observation.replies)
		}
		observation.ledger = testutil.SettledAccounting(t, client, env.statsURL, "model="+defaultStandModel, paritySettleWithin, served)
		balanceAfter, nonceAfter := testutil.EscrowSessionState(t, client, env.clientURL, defaultEscrowID, isGateway)
		require.LessOrEqual(t, balanceAfter, balanceBefore, "%s: the escrow balance grew over the traffic", observation.driver)
		observation.balanceSpent = balanceBefore - balanceAfter
		observation.noncesSpent = nonceAfter - nonceBefore
		testutil.RequireNonceAccountingBalanced(t, observation.ledger)
	})
	if !completed {
		t.FailNow()
	}
	return observation
}

// sendParityTraffic sends the scenario's requests in order, or all at once for a burst.
func sendParityTraffic(t *testing.T, client *http.Client, clientURL string, scenario parityScenario) []parityReply {
	t.Helper()
	if scenario.concurrent {
		return sendConcurrently(t, client, clientURL, scenario.requests)
	}
	replies := make([]parityReply, 0, len(scenario.requests))
	for _, request := range scenario.requests {
		replies = append(replies, sendParityRequest(t, client, clientURL, request))
	}
	return replies
}

func sendParityRequest(t *testing.T, client *http.Client, clientURL string, request parityRequest) parityReply {
	t.Helper()
	url := clientURL + "/v1/chat/completions"
	reply := parityReply{request: request.name}
	if streaming, _ := request.body["stream"].(bool); streaming {
		stream := testutil.SendStreamingRaw(t, client, url, request.body, testutil.AdminAPIKey)
		reply.status, reply.text = stream.StatusCode, testutil.StreamText(stream)
	} else {
		response, err := testutil.PostJSONRawE(client, url, request.body, testutil.AdminAPIKey)
		reply.status, reply.text = response.StatusCode, testutil.ReplyText(response)
		if err != nil {
			reply.text = "no answer: " + err.Error()
		}
	}
	if reply.status == http.StatusOK && json.Unmarshal([]byte(reply.text), &reply.received) != nil {
		reply.received = nil
	}
	return reply
}

func sendConcurrently(t *testing.T, client *http.Client, clientURL string, requests []parityRequest) []parityReply {
	t.Helper()
	results := make([]testutil.RawResponseResult, len(requests))
	var group sync.WaitGroup
	for index, request := range requests {
		group.Go(func() {
			response, err := testutil.PostJSONRawE(client, clientURL+"/v1/chat/completions", request.body, testutil.AdminAPIKey)
			results[index] = testutil.RawResponseResult{Response: response, Err: err}
		})
	}
	group.Wait()
	replies := make([]parityReply, 0, len(requests))
	for index, result := range results {
		reply := parityReply{request: requests[index].name, status: result.Response.StatusCode, text: testutil.ReplyText(result.Response)}
		if result.Err != nil {
			reply.text = "no answer: " + result.Err.Error()
		}
		replies = append(replies, reply)
	}
	return replies
}

// replyStatuses is what the client saw, in request order, or sorted when the requests raced each other.
func replyStatuses(observation parityObservation, concurrent bool) []int {
	statuses := make([]int, 0, len(observation.replies))
	for _, reply := range observation.replies {
		statuses = append(statuses, reply.status)
	}
	if concurrent {
		slices.Sort(statuses)
	}
	return statuses
}

// reportParity logs both drivers side by side: spending, ledger, then every request whose answer or forwarded body differs.
func reportParity(t *testing.T, scenario parityScenario, legacy, gateway parityObservation) {
	t.Helper()
	var report strings.Builder
	fmt.Fprintf(&report, "\n== %s", scenario.name)
	fmt.Fprintf(&report, "\n%-12s %-10s %10s %8s %10s %10s %10s %10s %7s %9s %8s", "driver", "served", "time", "nonces", "money", "reserved", "actual", "refunded", "misses", "in_flight", "vote_due")
	for _, observation := range []parityObservation{legacy, gateway} {
		served := 0
		var reserved, actual, refunded, misses, inFlight, voteDue uint64
		for _, reply := range observation.replies {
			if reply.status == http.StatusOK {
				served++
			}
		}
		for _, participant := range observation.ledger.Participants {
			reserved += participant.ReservedCost
			actual += participant.ActualCost
			refunded += participant.RefundedCost
			misses += participant.ProtocolMisses
			inFlight += participant.InFlight
			voteDue += participant.TimeoutPending
		}
		fmt.Fprintf(&report, "\n%-12s %-10s %10s %8d %10d %10d %10d %10d %7d %9d %8d", observation.driver,
			fmt.Sprintf("%d/%d", served, len(observation.replies)), observation.trafficTime, observation.noncesSpent,
			observation.balanceSpent, reserved, actual, refunded, misses, inFlight, voteDue)
	}
	writeCountTable(&report, "dispositions", legacy.driver, testutil.AccountingDispositions(legacy.ledger), gateway.driver, testutil.AccountingDispositions(gateway.ledger))
	writeCountTable(&report, "timeout outcomes", legacy.driver, testutil.AccountingTimeoutOutcomes(legacy.ledger), gateway.driver, testutil.AccountingTimeoutOutcomes(gateway.ledger))
	if !scenario.concurrent {
		writeReplyDifferences(&report, legacy, gateway)
	}
	t.Log(report.String())
}

func writeCountTable(report *strings.Builder, title, leftName string, left map[string]uint64, rightName string, right map[string]uint64) {
	var keys []string
	for key := range left {
		keys = append(keys, key)
	}
	for key := range right {
		if _, shared := left[key]; !shared {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return
	}
	slices.Sort(keys)
	fmt.Fprintf(report, "\n%s:", title)
	for _, key := range keys {
		marker := " "
		if left[key] != right[key] {
			marker = "≠"
		}
		fmt.Fprintf(report, "\n  %s %-40s %s=%-6d %s=%d", marker, key, leftName, left[key], rightName, right[key])
	}
}

func writeReplyDifferences(report *strings.Builder, legacy, gateway parityObservation) {
	for index := range min(len(legacy.replies), len(gateway.replies)) {
		left, right := legacy.replies[index], gateway.replies[index]
		if left.status != right.status || left.text != right.text {
			fmt.Fprintf(report, "\nreply %q:\n  %s %d %s\n  %s %d %s", left.request,
				legacy.driver, left.status, clipped(left.text), gateway.driver, right.status, clipped(right.text))
		}
		if left.received != nil || right.received != nil {
			differences := testutil.DiffJSON("body", left.received, right.received)
			if len(differences) > 0 {
				fmt.Fprintf(report, "\nforwarded %q (left %s, right %s):\n  %s", left.request, legacy.driver, gateway.driver, strings.Join(differences, "\n  "))
			}
		}
	}
}

func clipped(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= parityTextLimit {
		return cmp.Or(text, "<empty>")
	}
	return text[:parityTextLimit] + "…"
}

// Test flow:
//  1. For every scenario, start the three-host stand with its host faults and drive it with devshardctl, then with the gateway.
//  2. Send the scenario's requests, stop the named hosts first where the scenario says so.
//  3. Read each driver's escrow balance and latest nonce around the traffic and its nonce ledger once it settles.
//  4. Assert both drivers answered with the same statuses and both ledgers balance.
//  5. Log spending, ledger dispositions, timeout outcomes, differing answers and differing forwarded bodies side by side.
func TestE2E_DriverParity(t *testing.T) {
	requireParityE2E(t)

	for _, scenario := range parityScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			if scenario.slow {
				requireSlowE2E(t)
			}

			legacy := observeDriver(t, false, scenario)
			gateway := observeDriver(t, true, scenario)

			reportParity(t, scenario, legacy, gateway)
			require.Equal(t, replyStatuses(legacy, scenario.concurrent), replyStatuses(gateway, scenario.concurrent),
				"devshardctl and the gateway answered the same traffic with different statuses")
		})
	}
}
