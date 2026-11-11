package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonvalidation "common/validation"
	devshardpkg "devshard"
)

// An inference whose logprobs a replay reproduces (the logprob comparison
// passes it) and whose tokens are never the tape's winners (the echo fails it).
type echoModeEngine struct {
	stored          []byte
	replays, echoes int
	echoStatus      int
}

func newEchoModeEngine() *echoModeEngine {
	positions := make([]interface{}, 64)
	for i := range positions {
		alternatives := []interface{}{
			map[string]interface{}{"token": "5", "logprob": -0.5},
			map[string]interface{}{"token": "6", "logprob": -1.5},
		}
		positions[i] = map[string]interface{}{"token": "5", "logprob": -0.5, "top_logprobs": alternatives}
	}
	stored, _ := json.Marshal(map[string]interface{}{"id": "test", "object": "chat.completion",
		"choices": []interface{}{map[string]interface{}{"index": 0, "finish_reason": "length",
			"message":  map[string]interface{}{"role": "assistant", "content": "x"},
			"logprobs": map[string]interface{}{"content": positions}}},
		"usage": map[string]interface{}{"prompt_tokens": 3, "completion_tokens": 64, "total_tokens": 67}})
	return &echoModeEngine{stored: stored, echoStatus: http.StatusOK}
}

func (e *echoModeEngine) fetch(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
	prompt := `{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}],"max_tokens":64,"temperature":1,"top_p":1,"top_k":0}`
	return []byte(prompt), e.stored, nil
}

func (e *echoModeEngine) executeML(_ context.Context, _, _, path string, body []byte) (*http.Response, error) {
	respond := func(status int, payload []byte) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(payload))}, nil
	}
	switch {
	case path == commonvalidation.EchoPath:
		e.echoes++
		if e.echoStatus != http.StatusOK {
			return respond(e.echoStatus, []byte(`{"error":"down"}`))
		}
		// The answer's token is far below another one at every position.
		entries := make([]interface{}, 3+64)
		for i := 3; i < len(entries); i++ {
			entries[i] = map[string]interface{}{"5": map[string]float64{"logprob": -30}, "6": map[string]float64{"logprob": -0.0001}}
		}
		payload, _ := json.Marshal(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"prompt_logprobs": entries}}})
		return respond(http.StatusOK, payload)
	case strings.Contains(string(body), "enforced_tokens"):
		e.replays++
		return respond(http.StatusOK, e.stored)
	default: // the echo's render of the request
		return respond(http.StatusOK, []byte(`{"prompt_token_ids":[10,11,12],"choices":[{"token_ids":[0],"finish_reason":"length"}]}`))
	}
}

func echoModeValidator(mode string, engine *echoModeEngine) (*Validator, devshardpkg.ValidateRequest) {
	v := newFaultTestValidator(10, true, engine.fetch, engine.executeML, nil)
	v.echoMode = mode
	v.pool = newFlipPool(poolWindow, 24*time.Hour)
	req := faultReq(10)
	req.Model = "Qwen/Qwen3-0.6B"
	req.InputTokens, req.OutputTokens = 3, 64
	return v, req
}

// In shadow mode the logprob comparison casts the vote and the echo only runs beside it.
func TestValidator_EchoShadowDoesNotChangeTheVote(t *testing.T) {
	engine := newEchoModeEngine()
	v, req := echoModeValidator(EchoShadow, engine)
	verdict, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.True(t, verdict.Valid)
	require.Empty(t, verdict.Reason)
	require.Equal(t, 1, engine.replays)
	require.Equal(t, 1, engine.echoes)

	// The echo itself would have voted the other way.
	prompt, stored, _ := engine.fetch(context.Background(), req, "", 0)
	echo, err := v.echoVerdict(context.Background(), req, "42", prompt, stored, 10)
	require.NoError(t, err)
	require.False(t, echo.Valid)
	require.Equal(t, "echo_fail", echo.Reason)
}

func TestValidator_EchoShadowFailureDoesNotReachTheVote(t *testing.T) {
	engine := newEchoModeEngine()
	engine.echoStatus = http.StatusInternalServerError
	v, req := echoModeValidator(EchoShadow, engine)
	verdict, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.True(t, verdict.Valid)
	require.Equal(t, 1, engine.echoes)
}

func TestValidator_EchoEnforceVotesByTheEcho(t *testing.T) {
	engine := newEchoModeEngine()
	v, req := echoModeValidator(EchoEnforce, engine)
	verdict, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.False(t, verdict.Valid)
	require.Equal(t, "echo_fail", verdict.Reason)
	require.Zero(t, engine.replays)
}

func TestValidator_EchoOffIsTheLogprobComparisonAlone(t *testing.T) {
	engine := newEchoModeEngine()
	v, req := echoModeValidator(EchoOff, engine)
	verdict, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.True(t, verdict.Valid)
	require.Zero(t, engine.echoes)
}
