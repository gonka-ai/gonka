package host

import (
	"context"
	"time"

	"common/completionapi"

	"devshard"
	"devshard/observability"
	"devshard/types"
)

// ChallengeRejectNoContextLimit: the governance --max-model-len of the model
// could not be read, so an empty body cannot be told from a refusal.
const ChallengeRejectNoContextLimit = "no_context_limit"

// ContextLimits returns a model's governance --max-model-len in an epoch: 0
// when the model's args set none, an error when the chain could not be read.
type ContextLimits interface {
	Resolve(ctx context.Context, epochID uint64, model string) (uint64, error)
}

// WithContextLimits lets challenge votes tell a context-length refusal from
// an empty stream.
func WithContextLimits(l ContextLimits) HostOption {
	return func(h *Host) { h.contextLimits = l }
}

// emptyBodyVerdict votes on a body with no positions. A request whose prompt
// plus max_tokens exceeds the governance limit is refused before generating:
// no EMPTY_STREAM, but the input claim is still held to the render.
func (h *Host) emptyBodyVerdict(ctx context.Context, fetch devshard.ChallengeFetch, rec types.InferenceRecord, count *usageCount) (keptChallengeVote, string, string) {
	empty := keptChallengeVote{
		Fault:     types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM,
		Check:     types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS,
		Deviation: int64(rec.OutputTokens),
	}
	// A refused request generated nothing: a claim of output keeps the vote.
	if h.contextLimits == nil || fetch.Request == nil || rec.OutputTokens > 0 {
		return empty, "", ""
	}
	maxTokens, err := completionapi.EffectiveMaxTokens(fetch.Request)
	if err != nil {
		return empty, "", ""
	}
	limit, err := h.contextLimits.Resolve(ctx, h.epochID, rec.Model)
	if err != nil {
		return keptChallengeVote{}, ChallengeRejectNoContextLimit, err.Error()
	}
	if limit == 0 {
		return empty, "", ""
	}
	if h.renderer == nil || !h.renderer.Serves(rec.Model) {
		return keptChallengeVote{}, ChallengeRejectNoRender, "no render server for the model"
	}
	started := time.Now()
	renderCtx := observability.WithRenderNodeObserver(ctx, func(id, endpoint string) {
		count.inputRenderNodes = append(count.inputRenderNodes, id+"@"+endpoint)
	})
	rendered, source, err := h.renderer.CountPrompt(renderCtx, rec.Model, fetch.Request)
	count.inputRender = source
	observeRender("input", rec.Model, started, err)
	if err != nil {
		return keptChallengeVote{}, renderReject(err), err.Error()
	}
	count.input = rendered
	if rendered+maxTokens <= limit {
		return empty, "", ""
	}
	terms, err := readExecutedTerms(fetch.Request)
	allowance, known := types.UsageAllowanceFor(rec.Model)
	switch {
	case !known:
		count.inputSkipped = "model_not_in_table"
	case err != nil || !terms.textOnly:
		count.inputSkipped = "non_text_request"
	default:
		count.inputLimit = rendered + allowance.InputSpread
		if rec.InputTokens > count.inputLimit {
			return inflated(types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_INPUT, rec.InputTokens, rendered), "", ""
		}
	}
	return keptChallengeVote{
		Fault: types.ChallengeFault_CHALLENGE_FAULT_NONE,
		Check: types.ChallengeCheck_CHALLENGE_CHECK_CONTEXT_LIMIT,
	}, "", ""
}
