package host

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/types"
)

type fixedLimits struct {
	limit uint64
	err   error
}

func (l fixedLimits) Resolve(context.Context, uint64, string) (uint64, error) { return l.limit, l.err }

type promptCounter struct {
	count uint64
	calls int
}

func (r *promptCounter) Serves(string) bool { return true }

func (r *promptCounter) CountPrompt(context.Context, string, []byte) (uint64, string, error) {
	r.calls++
	return r.count, "test", nil
}

func (r *promptCounter) CountTurn(context.Context, string, []byte, map[string]any) (uint64, string, error) {
	return 0, "test", errors.New("not used")
}

func TestEmptyBodyVerdictContextLimit(t *testing.T) {
	model := types.MiniMaxM27ModelID // InputSpread 8
	request := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"x"}],"max_tokens":8192}`)
	fetch := devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchFetched, Request: request}
	cases := map[string]struct {
		limits      ContextLimits
		prompt      uint64
		inputClaim  uint64
		outputClaim uint64
		wantFault   types.ChallengeFault
		wantCheck   types.ChallengeCheck
		wantReject  string
		wantRenders int
	}{
		"request above the limit is the client's refusal": {
			limits: fixedLimits{limit: 400000}, prompt: 392499, inputClaim: 392499,
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_NONE, wantCheck: types.ChallengeCheck_CHALLENGE_CHECK_CONTEXT_LIMIT, wantRenders: 1,
		},
		"a refusal with an inflated input claim stays a fault": {
			limits: fixedLimits{limit: 400000}, prompt: 392499, inputClaim: 392499 + 9,
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_USAGE_INFLATED, wantCheck: types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_INPUT, wantRenders: 1,
		},
		"a refusal with output claimed keeps the empty-stream vote": {
			limits: fixedLimits{limit: 400000}, prompt: 392499, inputClaim: 392499, outputClaim: 8192,
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM, wantCheck: types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS,
		},
		"a request within InputSpread of the limit is the client's refusal": {
			limits: fixedLimits{limit: 400000}, prompt: 391801, inputClaim: 391801,
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_NONE, wantCheck: types.ChallengeCheck_CHALLENGE_CHECK_CONTEXT_LIMIT, wantRenders: 1,
		},
		"request that fits keeps the empty-stream vote": {
			limits: fixedLimits{limit: 400000}, prompt: 391800, inputClaim: 0,
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM, wantCheck: types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS, wantRenders: 1,
		},
		"no governance limit keeps the empty-stream vote": {
			limits: fixedLimits{}, prompt: 392499,
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM, wantCheck: types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS,
		},
		"an unread limit does not vote": {
			limits: fixedLimits{err: errors.New("chain down")}, prompt: 392499,
			wantReject: ChallengeRejectNoContextLimit,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			renderer := &promptCounter{count: tc.prompt}
			h := &Host{epochID: 7, renderer: renderer, contextLimits: tc.limits}
			rec := types.InferenceRecord{Model: model, InputTokens: tc.inputClaim, OutputTokens: tc.outputClaim}
			verdict, reject, _ := h.emptyBodyVerdict(context.Background(), fetch, rec, &usageCount{})
			require.Equal(t, tc.wantReject, reject)
			require.Equal(t, tc.wantFault, verdict.Fault)
			require.Equal(t, tc.wantCheck, verdict.Check)
			require.Equal(t, tc.wantRenders, renderer.calls)
		})
	}

	t.Run("without limits wired an empty body is an empty stream", func(t *testing.T) {
		h := &Host{renderer: &promptCounter{count: 392499}}
		verdict, reject, _ := h.emptyBodyVerdict(context.Background(), fetch, types.InferenceRecord{Model: model}, &usageCount{})
		require.Empty(t, reject)
		require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM, verdict.Fault)
	})
}
