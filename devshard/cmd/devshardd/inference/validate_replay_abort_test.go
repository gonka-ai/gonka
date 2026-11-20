package inference

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	commonvalidation "common/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
)

// replayStreamWithFinish is a complete replay stream (usage and [DONE]) whose
// output tokens end with a chunk carrying finishReason.
func replayStreamWithFinish(tokens []string, finishReason string, promptTokens int) []byte {
	var sb strings.Builder
	for _, token := range tokens {
		fmt.Fprintf(&sb, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"logprobs\":{\"content\":[{\"token\":%q,\"logprob\":-0.1,\"top_logprobs\":[{\"token\":%q,\"logprob\":-0.1}]}]}}]}\n\n", token, token)
	}
	fmt.Fprintf(&sb, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\n", finishReason)
	fmt.Fprintf(&sb, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d}}\n\n", promptTokens, len(tokens))
	sb.WriteString("data: [DONE]\n\n")
	return []byte(sb.String())
}

func TestClassifyExecuteValidationErr_AbortedReplayDefers(t *testing.T) {
	cause := fmt.Errorf("%w: inference 710, finish reasons [abort]", commonvalidation.ErrReplayAborted)
	got := classifyExecuteValidationErr(cause)
	require.ErrorIs(t, got, devshardpkg.ErrValidationDeferred)
	require.ErrorIs(t, got, commonvalidation.ErrReplayAborted)
	assert.Equal(t, "validation deferred: validator replay aborted by the ML node: inference 710, finish reasons [abort]", got.Error())

	// Any other replay error keeps its classification and does not defer.
	other := classifyExecuteValidationErr(errors.New("parse validation response"))
	assert.False(t, errors.Is(other, devshardpkg.ErrValidationDeferred))
}

// Test flow:
// 1. The executor stored 10 output tokens and claims 10.
// 2. The validator's ML node returns a complete replay stream with 2 tokens.
// 3. Ended with "abort" (dropped at PoC start): ErrValidationDeferred and no vote.
// 4. Ended with "length" or "stop": the short replay is still a Valid:false vote.
// 5. Only the executor's stored response ended with "abort" and the replay
// ended with "stop": still Valid:false, the deferral reads the replay only.
func TestValidator_Validate_AbortedReplayDefersInsteadOfVotingFalse(t *testing.T) {
	positions := make([]string, 10)
	for i := range positions {
		token := fmt.Sprint(i + 1)
		positions[i] = fmt.Sprintf(`{"token":%q,"logprob":-0.1,"top_logprobs":[{"token":%q,"logprob":-0.1}]}`, token, token)
	}

	for _, tc := range []struct {
		name         string
		storedFinish string
		finishReason string
		wantDeferred bool
	}{
		{name: "abort", storedFinish: "stop", finishReason: "abort", wantDeferred: true},
		{name: "length", storedFinish: "stop", finishReason: "length"},
		{name: "stop", storedFinish: "stop", finishReason: "stop"},
		{name: "executor_abort_replay_stop", storedFinish: "abort", finishReason: "stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := []byte(`{"id":"test","object":"chat.completion","choices":[{"index":0,"finish_reason":"` + tc.storedFinish + `","logprobs":{"content":[` + strings.Join(positions, ",") + `]}}]}`)
			fetch := func(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
				return []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`), stored, nil
			}
			replays := 0
			executeML := func(context.Context, string, string, []byte) (*http.Response, error) {
				replays++
				body := replayStreamWithFinish([]string{"1", "2"}, tc.finishReason, 12)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
			}
			validator := newFaultTestValidator(10, true, fetch, executeML, nil)
			req := faultReq(10)
			req.InputTokens = 12
			req.OutputTokens = 10

			result, err := validator.Validate(context.Background(), req)
			assert.Equal(t, 1, replays)
			if tc.wantDeferred {
				require.ErrorIs(t, err, devshardpkg.ErrValidationDeferred)
				require.ErrorIs(t, err, commonvalidation.ErrReplayAborted)
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.False(t, result.Valid)
		})
	}
}
