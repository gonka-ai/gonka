package inference

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"common/completionapi"
	devshardpkg "devshard"

	"github.com/stretchr/testify/require"
)

// The executor re-checks stop_token_ids against the vocabulary before the ML node sees them, and
// runs a short max_tokens as asked.
func TestExecuteInferenceShortOutput(t *testing.T) {
	request := func(prompt string) devshardpkg.ExecuteRequest {
		return devshardpkg.ExecuteRequest{InferenceID: 1, EscrowID: "e", Model: "m", Prompt: []byte(prompt)}
	}
	refused := errors.New("stop after capture")

	for name, tc := range map[string]struct {
		prompt         string
		vocabularySize int
		wantSent       bool
		wantMin        any
		wantStopID     bool
	}{
		"in-vocab stop ids reach the node": {`{"model":"m","stream":true,"max_tokens":8,"stop_token_ids":[7],"messages":[{"role":"user","content":"hi"}]}`, 1000, true, nil, true},
		"out-of-range stop id refused":     {`{"model":"m","stream":true,"max_tokens":8,"stop_token_ids":[1000],"messages":[{"role":"user","content":"hi"}]}`, 1000, false, nil, false},
		"unknown vocab refused":            {`{"model":"m","stream":true,"max_tokens":8,"stop_token_ids":[7],"messages":[{"role":"user","content":"hi"}]}`, 0, false, nil, false},
		"caller min_tokens kept":           {`{"model":"m","stream":true,"max_tokens":8,"min_tokens":4,"messages":[{"role":"user","content":"hi"}]}`, 0, true, float64(4), false},
	} {
		t.Run(name, func(t *testing.T) {
			var sent map[string]any
			_, err := executeInference(context.Background(), request(tc.prompt), &recordingPayloadStore{}, 1,
				func(_ context.Context, _ string, body []byte) (*http.Response, error) {
					require.NoError(t, json.Unmarshal(body, &sent))
					return nil, refused
				}, fixedChainParams{}, true, tc.vocabularySize)
			require.Error(t, err)
			if !tc.wantSent {
				require.Nil(t, sent, "the ML node must not see the request")
				require.ErrorIs(t, err, completionapi.ErrStopTokenIDs)
				return
			}
			require.ErrorIs(t, err, refused)
			require.EqualValues(t, 8, sent["max_tokens"])
			if tc.wantMin == nil {
				require.NotContains(t, sent, "min_tokens")
			} else {
				require.Equal(t, tc.wantMin, sent["min_tokens"])
			}
			if tc.wantStopID {
				require.Equal(t, []any{float64(7)}, sent["stop_token_ids"])
			} else {
				require.NotContains(t, sent, "stop_token_ids")
			}
		})
	}
}
