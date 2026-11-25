package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
)

type processedChainParams struct{ stubChainParams }

func (processedChainParams) LogprobsMode() string { return "processed_logprobs" }

// An inference whose logprobs the replay reproduces, so the logprob comparison
// passes it, with one rival token at every position: close to the answer's
// token the noise of the seed often prefers it, far below it never does.
// replay, when set, is what the engine answers the replay with in place of
// the stored logprobs.
type tapeModeEngine struct {
	model   string
	stored  []byte
	replay  []byte
	replays int
}

func tapeModeAnswer(own, rival float64) []byte {
	positions := make([]interface{}, 64)
	for i := range positions {
		alternatives := []interface{}{
			map[string]interface{}{"token": "5", "logprob": own},
			map[string]interface{}{"token": "6", "logprob": rival},
		}
		positions[i] = map[string]interface{}{"token": "5", "logprob": own, "top_logprobs": alternatives}
	}
	answer, _ := json.Marshal(map[string]interface{}{"id": "test", "object": "chat.completion",
		"choices": []interface{}{map[string]interface{}{"index": 0, "finish_reason": "length",
			"message":  map[string]interface{}{"role": "assistant", "content": "x"},
			"logprobs": map[string]interface{}{"content": positions}}},
		"usage": map[string]interface{}{"prompt_tokens": 3, "completion_tokens": 64, "total_tokens": 67}})
	return answer
}

func newTapeModeEngine(rival float64) *tapeModeEngine {
	return &tapeModeEngine{model: "Qwen/Qwen3-0.6B", stored: tapeModeAnswer(-0.5, rival)}
}

func (e *tapeModeEngine) fetch(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
	prompt := `{"model":"` + e.model + `","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`
	return []byte(prompt), e.stored, nil
}

func (e *tapeModeEngine) executeML(_ context.Context, _, _ string, body []byte) (*http.Response, error) {
	if !strings.Contains(string(body), "enforced_tokens") {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	}
	e.replays++
	replay := e.stored
	if e.replay != nil {
		replay = e.replay
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(replay))}, nil
}

func tapeModeValidator(mode string, engine *tapeModeEngine) (*Validator, devshardpkg.ValidateRequest) {
	v := newFaultTestValidator(10, true, engine.fetch, engine.executeML, nil)
	v.chainParams = processedChainParams{}
	v.tapeMode = mode
	req := faultReq(10)
	req.Model = engine.model
	req.InputTokens, req.OutputTokens = 3, 64
	return v, req
}

const (
	offTheTape = -0.6
	onTheTape  = -30
)

// The tape casts the vote both ways: an answer off it is voted invalid though
// the logprob comparison passed it, an answer on it valid though the
// comparison rejected the logprobs the executor stored.
func TestValidator_TapeVoteCastsTheVote(t *testing.T) {
	engine := newTapeModeEngine(offTheTape)
	v, req := tapeModeValidator(TapeVote, engine)
	verdict, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.False(t, verdict.Valid)
	require.Equal(t, "tape_fail", verdict.Reason)
	require.Equal(t, 1, engine.replays)

	for mode, valid := range map[string]bool{TapeShadow: false, TapeVote: true} {
		rejected := &tapeModeEngine{model: "Qwen/Qwen3-0.6B", stored: tapeModeAnswer(-9, -0.001), replay: tapeModeAnswer(-0.5, onTheTape)}
		v, req = tapeModeValidator(mode, rejected)
		verdict, err = v.Validate(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, valid, verdict.Valid, mode)
		require.Empty(t, verdict.Reason, mode)
	}
}

// What the tape cannot judge keeps the vote of the logprob comparison.
func TestValidator_TapeVoteKeepsTheVoteOfWhatItCannotJudge(t *testing.T) {
	engine := newTapeModeEngine(offTheTape)
	engine.model = "other/model"
	v, req := tapeModeValidator(TapeVote, engine)
	verdict, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.True(t, verdict.Valid)

	rejected := &tapeModeEngine{model: "other/model", stored: tapeModeAnswer(-9, -0.001), replay: tapeModeAnswer(-0.5, onTheTape)}
	v, req = tapeModeValidator(TapeVote, rejected)
	verdict, err = v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.False(t, verdict.Valid)
}

// In shadow the logprob comparison casts the vote; the tape is read from the
// comparison's own replay, so the ML node gets that one call.
func TestValidator_TapeShadowDoesNotChangeTheVote(t *testing.T) {
	engine := newTapeModeEngine(offTheTape)
	v, req := tapeModeValidator(TapeShadow, engine)
	verdict, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.True(t, verdict.Valid)
	require.Empty(t, verdict.Reason)
	require.Equal(t, 1, engine.replays)
}

func TestValidator_TapeEnforceVotesAnAnswerOffItsTapeInvalid(t *testing.T) {
	engine := newTapeModeEngine(offTheTape)
	v, req := tapeModeValidator(TapeEnforce, engine)
	verdict, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.False(t, verdict.Valid)
	require.Equal(t, "tape_fail", verdict.Reason)
	require.Equal(t, 1, engine.replays)

	honest := newTapeModeEngine(onTheTape)
	v, req = tapeModeValidator(TapeEnforce, honest)
	verdict, err = v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.True(t, verdict.Valid)
	require.Empty(t, verdict.Reason)
}

// What the tape cannot judge keeps the vote of the logprob comparison.
func TestValidator_TapeEnforceKeepsTheVoteOfWhatItCannotJudge(t *testing.T) {
	engine := newTapeModeEngine(offTheTape)
	engine.model = "other/model"
	v, req := tapeModeValidator(TapeEnforce, engine)
	verdict, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.True(t, verdict.Valid)

	raw := newTapeModeEngine(offTheTape)
	v, req = tapeModeValidator(TapeEnforce, raw)
	v.chainParams = stubChainParams{}
	verdict, err = v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.True(t, verdict.Valid)
}

func TestValidator_TapeOffIsTheLogprobComparisonAlone(t *testing.T) {
	engine := newTapeModeEngine(offTheTape)
	v, req := tapeModeValidator(TapeOff, engine)
	verdict, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.True(t, verdict.Valid)
}
