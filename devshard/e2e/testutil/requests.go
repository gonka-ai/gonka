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
	DebugLogf(t, "sending streaming completion request content=%q", content)

	data, err := json.Marshal(ChatCompletionBody(content, true))
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, clientURL+"/v1/chat/completions", strings.NewReader(string(data)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+AdminAPIKey)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, events := readSSEEvents(t, resp.Body)
	DebugLogf(t, "streaming completion status=%d content_type=%q body=%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	require.Less(t, resp.StatusCode, 300, "streaming completion returned %d: %s", resp.StatusCode, body)

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
	diffs := GetJSONArray(t, client, fmt.Sprintf("%s%s/sessions/%s/diffs?from=1&to=%d", hostURL, routePrefix, escrowID, toNonce))
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

func FinalizeSession(t *testing.T, client *http.Client, clientURL string) map[string]any {
	t.Helper()
	DebugLogf(t, "finalizing devshard session")
	settlement := PostJSON(t, client, clientURL+"/v1/finalize", map[string]any{})
	settlementJSON, err := json.MarshalIndent(settlement, "", "  ")
	require.NoError(t, err)
	t.Logf("SettlementContract:\n%s", settlementJSON)
	return settlement
}
