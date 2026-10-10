package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

type fakeStopTokenVocabulary map[string]int

func (f fakeStopTokenVocabulary) VocabularySize(model string) int {
	return f[model]
}

func withStopTokenVocabulary(t *testing.T, vocabulary StopTokenVocabulary) {
	t.Helper()
	prev := stopTokenVocabulary
	stopTokenVocabulary = vocabulary
	t.Cleanup(func() { stopTokenVocabulary = prev })
}

func normalizedFields(t *testing.T, body string) (map[string]any, chatRequest, error) {
	t.Helper()
	normalized, req, err := normalizeChatRequestForModel([]byte(body), "m")
	if err != nil {
		return nil, req, err
	}
	var raw map[string]any
	require.NoError(t, json.Unmarshal(normalized, &raw))
	return raw, req, nil
}

func TestApplyCallerMinTokensTable(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantMax uint64
		wantMin any // nil: absent
	}{
		{"keeps short max", `{"max_tokens":8}`, 8, nil},
		{"injects no min", `{"max_tokens":200}`, 200, nil},
		{"keeps caller min", `{"max_tokens":200,"min_tokens":4}`, 200, float64(4)},
		{"clamps caller min to max", `{"max_tokens":8,"min_tokens":16}`, 8, float64(8)},
		{"drops zero min", `{"max_tokens":8,"min_tokens":0}`, 8, nil},
		{"mirrors max_completion_tokens", `{"max_completion_tokens":5}`, 5, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fields map[string]any
			require.NoError(t, json.Unmarshal([]byte(tt.body), &fields))
			fields["messages"] = []any{map[string]any{"role": "user", "content": "hi"}}
			body, err := json.Marshal(fields)
			require.NoError(t, err)

			raw, req, err := normalizedFields(t, string(body))
			require.NoError(t, err)
			require.Equal(t, tt.wantMax, req.MaxTokens)
			require.EqualValues(t, tt.wantMax, raw["max_tokens"])
			if tt.wantMin == nil {
				require.NotContains(t, raw, "min_tokens")
			} else {
				require.Equal(t, tt.wantMin, raw["min_tokens"])
			}
			effective, err := completionapi.EffectiveMaxTokens(body)
			require.NoError(t, err)
			require.LessOrEqual(t, req.MaxTokens, effective, "the reservation covers what the node will produce")
		})
	}
}

func TestStopTokenIDsVocabularyCheck(t *testing.T) {
	tests := []struct {
		name       string
		vocabulary StopTokenVocabulary
		ids        string
		wantKept   bool
		wantStatus int
	}{
		{"in vocab kept", fakeStopTokenVocabulary{"m": 1000}, `[0,7,999]`, true, 0},
		{"at vocab size refused", fakeStopTokenVocabulary{"m": 1000}, `[1000]`, false, http.StatusBadRequest},
		{"far out of range refused", fakeStopTokenVocabulary{"m": 1000}, `[7,9999999]`, false, http.StatusBadRequest},
		{"negative refused", fakeStopTokenVocabulary{"m": 1000}, `[-1]`, false, http.StatusBadRequest},
		{"non-integer refused", fakeStopTokenVocabulary{"m": 1000}, `["7"]`, false, http.StatusBadRequest},
		{"unknown model vocab refused", fakeStopTokenVocabulary{"other": 1000}, `[7]`, false, http.StatusBadRequest},
		{"no vocabulary source refused", nil, `[7]`, false, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withStopTokenVocabulary(t, tt.vocabulary)

			raw, _, err := normalizedFields(t, `{"messages":[{"role":"user","content":"hi"}],"max_tokens":8,"stop_token_ids":`+tt.ids+`}`)
			if tt.wantStatus != 0 {
				require.Error(t, err)
				require.Equal(t, tt.wantStatus, chatRequestErrorStatus(err, 0))
				require.True(t, errors.Is(err, completionapi.ErrStopTokenIDs), "got %v", err)
				return
			}
			require.NoError(t, err)
			if tt.wantKept {
				var want []any
				require.NoError(t, json.Unmarshal([]byte(tt.ids), &want))
				require.Equal(t, want, raw["stop_token_ids"])
			} else {
				require.NotContains(t, raw, "stop_token_ids")
			}
		})
	}
}

type fakeVocabularyResolver map[uint64]map[string]int

func (f fakeVocabularyResolver) Resolve(_ context.Context, epochID uint64, model string) int {
	return f[epochID][model]
}

// The wired source resolves the routed model's vocab in the current epoch, the same (epoch, model)
// key the executor checks: in-vocab ids reach the engine, out-of-vocab ids are refused with 400
// and never forwarded, and an unknown epoch fails closed.
func TestStopTokenIDsEpochVocabulary(t *testing.T) {
	epoch := uint64(7)
	withStopTokenVocabulary(t, epochStopTokenVocabulary{
		resolver: fakeVocabularyResolver{7: {"m": 1000}},
		epoch:    func() uint64 { return epoch },
	})
	request := func(ids string) (map[string]any, error) {
		raw, _, err := normalizedFields(t, `{"messages":[{"role":"user","content":"hi"}],"max_tokens":8,"stop_token_ids":`+ids+`}`)
		return raw, err
	}

	raw, err := request(`[0,999]`)
	require.NoError(t, err)
	require.Equal(t, []any{float64(0), float64(999)}, raw["stop_token_ids"])
	require.NotContains(t, raw, "min_tokens")

	_, err = request(`[5,1000]`)
	require.ErrorIs(t, err, completionapi.ErrStopTokenIDs)
	require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, 0))
	require.ErrorContains(t, err, "entry 1 is not a token id in [0, 1000)")

	epoch = 0
	_, err = request(`[5]`)
	require.ErrorIs(t, err, completionapi.ErrStopTokenIDs)
	require.Equal(t, http.StatusBadRequest, chatRequestErrorStatus(err, 0))
}

func TestWireStopTokenVocabularyInstallsChainSource(t *testing.T) {
	withStopTokenVocabulary(t, nil)
	wireStopTokenVocabulary(nil, func() uint64 { return 0 })
	source, ok := stopTokenVocabulary.(epochStopTokenVocabulary)
	require.True(t, ok, "got %T", stopTokenVocabulary)
	require.NotNil(t, source.resolver)
	require.Zero(t, source.VocabularySize("m"), "no epoch yet: unknown, so refused")
}
