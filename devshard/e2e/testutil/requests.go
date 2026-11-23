package testutil

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/storage"
	"devshard/types"
)

func SendCompletion(t *testing.T, client *http.Client, clientURL, content string) map[string]any {
	t.Helper()
	DebugLogf(t, "sending completion request content=%q", content)
	resp := PostJSON(t, client, clientURL+"/v1/chat/completions", ChatCompletionBody(content, false))
	require.NotEmpty(t, resp["choices"], "completion response should include choices")
	return resp
}

func SendCompletionRaw(t *testing.T, client *http.Client, clientURL, content, bearerToken string) RawResponse {
	t.Helper()
	DebugLogf(t, "sending raw completion request content=%q bearer_set=%t", content, bearerToken != "")
	return PostJSONRaw(t, client, clientURL+"/v1/chat/completions", ChatCompletionBody(content, false), bearerToken)
}

func SendCompletionRawE(client *http.Client, clientURL, content, bearerToken string) (RawResponse, error) {
	return PostJSONRawE(client, clientURL+"/v1/chat/completions", ChatCompletionBody(content, false), bearerToken)
}

type StreamResponse struct {
	ContentType string
	Events      []string
}

func SendStreamingCompletion(t *testing.T, client *http.Client, clientURL, content string) StreamResponse {
	t.Helper()
	return SendStreamingBody(t, client, clientURL, ChatCompletionBody(content, true))
}

// SendStreamingCompletionWithLogprobs asks for the host's own positions, which a plain request never does.
func SendStreamingCompletionWithLogprobs(t *testing.T, client *http.Client, clientURL, content string, topLogprobs int) StreamResponse {
	t.Helper()
	body := ChatCompletionBody(content, true)
	body["logprobs"] = true
	body["top_logprobs"] = topLogprobs
	return SendStreamingBody(t, client, clientURL, body)
}

// SendStreamingBody streams whatever body the caller composed, for requests ChatCompletionBody cannot express.
func SendStreamingBody(t *testing.T, client *http.Client, clientURL string, body map[string]any) StreamResponse {
	t.Helper()
	DebugLogf(t, "sending streaming completion request body=%v", body)

	data, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, clientURL+"/v1/chat/completions", strings.NewReader(string(data)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+AdminAPIKey)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	rawBody, events := readSSEEvents(t, resp.Body)
	DebugLogf(t, "streaming completion status=%d content_type=%q body=%s", resp.StatusCode, resp.Header.Get("Content-Type"), rawBody)
	require.Less(t, resp.StatusCode, 300, "streaming completion returned %d: %s", resp.StatusCode, rawBody)

	return StreamResponse{
		ContentType: resp.Header.Get("Content-Type"),
		Events:      events,
	}
}

func SendCompletions(t *testing.T, client *http.Client, clientURL, contentPrefix string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		SendCompletion(t, client, clientURL, fmt.Sprintf("%s %d", contentPrefix, i+1))
	}
}

func ChatCompletionBody(content string, stream bool) map[string]any {
	body := map[string]any{
		"model": "stub-model",
		"messages": []map[string]string{
			{"role": "user", "content": content},
		},
		"max_tokens": 32,
	}
	if stream {
		body["stream"] = true
	}
	return body
}

const ToolChoiceUnsupportedMessage = "tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set"

// The gateway classifies a state divergence off this wording, so a stub host has to reproduce it verbatim.
const StateRootDivergenceMessage = "apply diff nonce 1: post_state_root does not match computed state root: diff 00, computed 11"

func ToolCompletionBody(content string, stream bool) map[string]any {
	body := ChatCompletionBody(content, stream)
	body["tool_choice"] = "auto"
	body["tools"] = []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "lookup_status",
			"description": "Return a test status string.",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}}
	return body
}

func readSSEEvents(t *testing.T, body io.Reader) (string, []string) {
	t.Helper()
	var raw strings.Builder
	var events []string
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		raw.WriteString(line)
		raw.WriteByte('\n')
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		events = append(events, strings.TrimPrefix(line, "data: "))
	}
	require.NoError(t, scanner.Err())
	return raw.String(), events
}

// DriveUntilValidationObserved waits until one slot appears in validated_by on
// at least two inferences. validateAsync publishes MsgValidation after the
// HTTP response, so each check drains host mempools with SyncHosts instead of
// only spraying extra completions at 250ms.
func DriveUntilValidationObserved(t *testing.T, client *http.Client, clientURL string) {
	t.Helper()
	const maxExtraCompletions = 20
	const validationTarget = 2
	const drainTimeout = 3 * time.Second
	const drainInterval = 250 * time.Millisecond

	for extra := 0; extra <= maxExtraCompletions; extra++ {
		reached, summary := drainUntilInferenceValidationTarget(t, client, clientURL, validationTarget, drainTimeout, drainInterval)
		if reached {
			return
		}
		if extra == maxExtraCompletions {
			t.Fatalf("no host reached at least %d completed validations before finalize after %d extra completion rounds: %s",
				validationTarget, maxExtraCompletions, summary)
		}
		SendCompletion(t, client, clientURL, fmt.Sprintf("validation probe %d", extra+1))
	}
}

func drainUntilInferenceValidationTarget(t *testing.T, client *http.Client, clientURL string, target uint64, timeout, interval time.Duration) (bool, string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	check := func() (bool, string) {
		t.Helper()
		state := GetJSON(t, client, clientURL+"/v1/debug/inferences")
		reached, summary := HasInferenceValidationTarget(t, state, target)
		DebugLogf(t, "inference validation evidence before finalize target=%d reached=%t (%s)",
			target, reached, summary)
		return reached, summary
	}
	for {
		reached, summary := check()
		if reached {
			return true, summary
		}
		expired := !time.Now().Before(deadline)
		// Compose mempool validations/finishes. The final SyncHosts after the
		// drain window closes the hole where validateAsync published too late
		// for the previous compose. Best-effort: a transient 500 should not
		// abort a drain that can succeed on the next tick.
		resp := PostJSONRaw(t, client, clientURL+"/v1/debug/sync-hosts", map[string]any{}, AdminAPIKey)
		if resp.StatusCode >= 300 {
			DebugLogf(t, "sync-hosts during validation drain status=%d body=%s", resp.StatusCode, resp.Body)
		}
		if expired {
			return check()
		}
		time.Sleep(interval)
	}
}

func LatestSessionNonce(t *testing.T, client *http.Client, clientURL string) uint64 {
	t.Helper()
	state := GetJSON(t, client, clientURL+"/v1/state")
	session, ok := state["session"].(map[string]any)
	require.True(t, ok, "state session should be an object")
	return NumericField(t, session, "latest_nonce")
}

// EscrowSessionState reads an escrow's balance and latest nonce by id from either driver: devshardctl nests them under session, the gateway does not.
func EscrowSessionState(t *testing.T, client *http.Client, clientURL, escrowID string, isGateway bool) (balance, latestNonce uint64) {
	t.Helper()
	state := GetJSON(t, client, clientURL+"/devshard/"+escrowID+"/v1/state")
	if isGateway {
		return NumericField(t, state, "balance"), NumericField(t, state, "latest_nonce")
	}
	session, ok := state["session"].(map[string]any)
	require.True(t, ok, "state session should be an object")
	return NumericField(t, session, "balance"), NumericField(t, session, "latest_nonce")
}

func GetSignatureStatus(t *testing.T, client *http.Client, clientURL string) map[string]any {
	t.Helper()
	return GetJSON(t, client, clientURL+"/v1/debug/signatures")
}

func GetStatus(t *testing.T, client *http.Client, clientURL string) map[string]any {
	t.Helper()
	return GetJSON(t, client, clientURL+"/v1/status")
}

func CollectSignatures(t *testing.T, client *http.Client, clientURL string, nonce uint64) map[string]any {
	t.Helper()
	return PostJSON(t, client, fmt.Sprintf("%s/v1/debug/signatures/collect?nonce=%d", clientURL, nonce), map[string]any{})
}

type GossipNonceStatus struct {
	Nonce      uint64
	Seen       bool
	StateHash  string
	StateSig   string
	SenderSlot uint64
}

type TimeoutInferenceTransaction struct {
	Nonce       uint64
	InferenceID uint64
	Reason      types.TimeoutReason
	VoterSlots  []uint32
}

func FindTimeoutInferenceTransaction(t *testing.T, client *http.Client, hostURL, routePrefix, escrowID string, toNonce uint64) (TimeoutInferenceTransaction, bool) {
	t.Helper()
	for from := uint64(1); from <= toNonce; {
		to := from + uint64(storage.DiffPageMaxNonces) - 1
		if to > toNonce {
			to = toNonce
		}
		diffs := GetJSONArray(t, client, fmt.Sprintf("%s%s/sessions/%s/diffs?from=%d&to=%d", hostURL, routePrefix, escrowID, from, to))
		if tx, found := timeoutInferenceInDiffs(t, diffs); found {
			return tx, true
		}
		if to == toNonce {
			break
		}
		from = to + 1
	}
	return TimeoutInferenceTransaction{}, false
}

func timeoutInferenceInDiffs(t *testing.T, diffs []any) (TimeoutInferenceTransaction, bool) {
	t.Helper()
	for _, raw := range diffs {
		record, ok := raw.(map[string]any)
		require.True(t, ok, "host diff record should be an object")
		diff, ok := record["diff"].(map[string]any)
		require.True(t, ok, "host diff record should include a diff object")
		rawTxs, ok := diff["txs"].(string)
		require.True(t, ok, "host diff txs should be base64")
		txs, err := base64.StdEncoding.DecodeString(rawTxs)
		require.NoError(t, err, "decode host diff txs")

		var content types.DiffContent
		require.NoError(t, proto.Unmarshal(txs, &content), "decode host diff content")
		for _, tx := range content.Txs {
			timeout := tx.GetTimeoutInference()
			if timeout == nil {
				continue
			}
			voterSlots := make([]uint32, len(timeout.Votes))
			for i, vote := range timeout.Votes {
				voterSlots[i] = vote.VoterSlot
			}
			return TimeoutInferenceTransaction{
				Nonce:       content.Nonce,
				InferenceID: timeout.InferenceId,
				Reason:      timeout.Reason,
				VoterSlots:  voterSlots,
			}, true
		}
	}
	return TimeoutInferenceTransaction{}, false
}

func InferenceStatus(t *testing.T, client *http.Client, clientURL string, inferenceID uint64) (map[string]any, bool) {
	t.Helper()
	state := GetJSON(t, client, clientURL+"/v1/debug/inferences")
	inferences, ok := state["inferences"].(map[string]any)
	require.True(t, ok, "debug inferences should be an object")
	inference, ok := inferences[fmt.Sprintf("%d", inferenceID)].(map[string]any)
	return inference, ok
}

func GetGossipNonceStatus(t *testing.T, client *http.Client, hostURL, routePrefix string, nonce uint64) GossipNonceStatus {
	t.Helper()
	status := GetJSON(t, client, fmt.Sprintf("%s%s/debug/gossip?nonce=%d", hostURL, routePrefix, nonce))
	seen, ok := status["seen"].(bool)
	require.True(t, ok, "gossip status seen should be a boolean")
	return GossipNonceStatus{
		Nonce:      NumericField(t, status, "nonce"),
		Seen:       seen,
		StateHash:  fmt.Sprint(status["state_hash"]),
		StateSig:   fmt.Sprint(status["state_sig"]),
		SenderSlot: NumericField(t, status, "sender_slot"),
	}
}

// The gateway refuses finalize with 409 while the escrow still has work in
// flight. Race cleanup outlives the winning completion: it waits up to
// SecondaryWaitAfterWinner plus a short cancel join for speculative losers.
// Not-yet-due HandleTimeout waits sleep off pendingRaceCleanup and only hold
// it briefly for the vote at the protocol deadline. Wait for the drain, then
// finalize.
const (
	finalizeDrainWait = 6 * time.Minute
	finalizeDrainPoll = 100 * time.Millisecond
)

func FinalizeSession(t *testing.T, client *http.Client, clientURL string) map[string]any {
	t.Helper()
	DebugLogf(t, "finalizing devshard session")
	settlement := postFinalizeRetryingConflict(t, client, clientURL+"/v1/finalize")
	settlementJSON, err := json.MarshalIndent(settlement, "", "  ")
	require.NoError(t, err)
	t.Logf("SettlementContract:\n%s", settlementJSON)
	return settlement
}

// LeakyHostAnswer carries every field the response side hides, at the depths vLLM writes them.
const LeakyHostAnswer = `{"choices":[{"message":{"content":"stub"},"logprobs":{"content":[{"logprob":-0.5,"top_logprobs":[]}]},"token_ids":[1,2,3]}],"prompt_token_ids":[4,5],"prompt_logprobs":[null],"usage":{"prompt_tokens":80,"completion_tokens":40}}`

// EchoedRequest reads the request body back out of an echoing host's answer.
func EchoedRequest(t *testing.T, resp RawResponse) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, resp.StatusCode, "completion should be served: %s", resp.Body)
	choices, ok := resp.JSON["choices"].([]any)
	require.True(t, ok, "echoed answer should carry choices: %s", resp.Body)
	require.NotEmpty(t, choices, "echoed answer should carry a choice: %s", resp.Body)
	choice, ok := choices[0].(map[string]any)
	require.True(t, ok, "echoed choice should be an object: %s", resp.Body)
	message, ok := choice["message"].(map[string]any)
	require.True(t, ok, "echoed choice should carry a message: %s", resp.Body)
	content, ok := message["content"].(string)
	require.True(t, ok, "echoed message should carry string content: %s", resp.Body)

	var received map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &received), "echoed content should be the request body: %s", content)
	return received
}

// EscrowBalance reads what the live escrow session has left; the stored record carries no balance.
func EscrowBalance(t *testing.T, client *http.Client, clientURL, escrowID, bearerToken string) uint64 {
	t.Helper()
	resp := GetRaw(t, client, clientURL+"/v1/admin/state", bearerToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, "reading gateway state: %s", resp.Body)
	var state struct {
		Status struct {
			Devshards []struct {
				EscrowID string `json:"id"`
				Balance  uint64 `json:"balance"`
			} `json:"devshards"`
		} `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Body), &state), "parsing gateway state: %s", resp.Body)
	for _, escrow := range state.Status.Devshards {
		if escrow.EscrowID == escrowID {
			return escrow.Balance
		}
	}
	t.Fatalf("gateway state carries no escrow %s: %s", escrowID, resp.Body)
	return 0
}

// DevshardRow is one escrow as the gateway's admin listing stores it.
type DevshardRow struct {
	EscrowID          string `json:"id"`
	Active            bool   `json:"active"`
	SettlementPending bool   `json:"settlement_pending"`
	GoneFromChain     bool   `json:"gone_from_chain"`
}

// ListedDevshard reads one escrow's row from GET /v1/admin/devshards.
func ListedDevshard(t *testing.T, client *http.Client, clientURL, escrowID, bearerToken string) DevshardRow {
	t.Helper()
	resp := GetRaw(t, client, clientURL+"/v1/admin/devshards", bearerToken)
	require.Equal(t, http.StatusOK, resp.StatusCode, "listing escrows: %s", resp.Body)
	var listing struct {
		Devshards []DevshardRow `json:"devshards"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Body), &listing), "parsing the escrow listing: %s", resp.Body)
	for _, row := range listing.Devshards {
		if row.EscrowID == escrowID {
			return row
		}
	}
	t.Fatalf("the escrow listing carries no escrow %s: %s", escrowID, resp.Body)
	return DevshardRow{}
}

func postFinalizeRetryingConflict(t *testing.T, client *http.Client, url string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(finalizeDrainWait)
	for {
		resp := PostJSONRaw(t, client, url, map[string]any{}, AdminAPIKey)
		if resp.StatusCode != http.StatusConflict || !time.Now().Before(deadline) {
			require.Less(t, resp.StatusCode, 300, "POST %s returned %d: %s", url, resp.StatusCode, resp.Body)
			require.NotNil(t, resp.JSON, "response body should be JSON: %s", resp.Body)
			return resp.JSON
		}
		DebugLogf(t, "finalize refused with 409, retrying: %s", resp.Body)
		time.Sleep(finalizeDrainPoll)
	}
}
