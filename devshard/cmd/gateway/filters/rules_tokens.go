package filters

import (
	"common/completionapi"

	"devshard"
)

// Output-token limits: the package defaults config.Defaults reads, and MaxOutputTokens above them all. See README.md, "Output token limits".
const (
	DefaultRequestMaxTokens uint64 = 4_096
	RequestMaxTokensCap     uint64 = 4_096

	MaxOutputTokens uint64 = 10_000_000
)

type outputTokenLimits struct {
	DefaultMaxTokens uint64
	MaxTokensCap     uint64
}

func normalizedOutputTokenLimits(limits outputTokenLimits) outputTokenLimits {
	if limits.DefaultMaxTokens == 0 {
		limits.DefaultMaxTokens = DefaultRequestMaxTokens
	}
	if limits.MaxTokensCap == 0 {
		limits.MaxTokensCap = RequestMaxTokensCap
	}
	return limits
}

// resolveOutputTokenLimits lets a per-model override replace either global limit; a zero keeps the global one.
func resolveOutputTokenLimits(options Options, routedModel string) outputTokenLimits {
	limits := outputTokenLimits{DefaultMaxTokens: options.DefaultMaxTokens, MaxTokensCap: options.MaxTokensCap}
	if options.ModelTokenLimits == nil {
		return limits
	}
	modelDefault, modelCap := options.ModelTokenLimits(routedModel)
	if modelDefault > 0 {
		limits.DefaultMaxTokens = modelDefault
	}
	if modelCap > 0 {
		limits.MaxTokensCap = modelCap
	}
	return limits
}

func flooredWithinCap(floor uint64, limits outputTokenLimits, admin bool) uint64 {
	if admin {
		return floor
	}
	return min(floor, normalizedOutputTokenLimits(limits).MaxTokensCap)
}

// capOutputTokens: 0 means no client budget and takes the default uncapped; a nonzero value clamps unless bypassed.
func capOutputTokens(value uint64, bypassLimit bool, limits outputTokenLimits) uint64 {
	limits = normalizedOutputTokenLimits(limits)
	if value == 0 {
		return min(limits.DefaultMaxTokens, MaxOutputTokens)
	}
	if !bypassLimit && value > limits.MaxTokensCap {
		return min(limits.MaxTokensCap, MaxOutputTokens)
	}
	return min(value, MaxOutputTokens)
}

// requestView is the typed projection of the 5 fields the pipeline needs outside the raw document.
type requestView struct {
	Model               string
	Stream              bool
	MaxTokens           uint64
	MaxCompletionTokens uint64
	N                   uint64
}

func decodeRequestView(document *Document) (requestView, error) {
	var view requestView
	if raw, ok := document.Get("model"); presentField(raw, ok) {
		modelName, isString := raw.(string)
		if !isString {
			return requestView{}, Reject("parse request: model must be a string")
		}
		view.Model = modelName
	}
	if raw, ok := document.Get("stream"); presentField(raw, ok) {
		streamFlag, isBool := raw.(bool)
		if !isBool {
			return requestView{}, Reject("parse request: stream must be a boolean")
		}
		view.Stream = streamFlag
	}
	if err := decodeUint64Field(document, "max_tokens", &view.MaxTokens); err != nil {
		return requestView{}, err
	}
	if err := decodeUint64Field(document, "max_completion_tokens", &view.MaxCompletionTokens); err != nil {
		return requestView{}, err
	}
	if err := decodeUint64Field(document, "n", &view.N); err != nil {
		return requestView{}, err
	}
	return view, nil
}

// syncRequestView re-decodes view post-StagePostLimits, preserving the token fields.
func syncRequestView(document *Document, view *requestView) error {
	refreshed, err := decodeRequestView(document)
	if err != nil {
		return err
	}
	refreshed.MaxTokens = view.MaxTokens
	refreshed.MaxCompletionTokens = view.MaxCompletionTokens
	*view = refreshed
	return nil
}

func decodeUint64Field(document *Document, name string, dst *uint64) error {
	raw, ok := document.Get(name)
	if !presentField(raw, ok) {
		return nil
	}
	value, ok := devshard.JSONNumericUint64(raw)
	if !ok {
		return Reject("parse request: %s must be a non-negative integer", name)
	}
	*dst = value
	return nil
}

// applyOutputTokenLimits resolves max_tokens from whichever field(s) the client sent. See README.md, "Output token limits".
func applyOutputTokenLimits(document *Document, view *requestView, options Options, routedModel string, profile *Profile) {
	_, hasMaxTokens := document.Get("max_tokens")
	_, hasMaxCompletionTokens := document.Get("max_completion_tokens")
	limits := resolveOutputTokenLimits(options, routedModel)
	limits.DefaultMaxTokens = max(limits.DefaultMaxTokens, flooredWithinCap(profile.outputTokensFloor(), limits, options.Admin))

	var resolved uint64
	switch {
	case hasMaxTokens && hasMaxCompletionTokens:
		resolved = min(
			capOutputTokens(view.MaxTokens, options.Admin, limits),
			capOutputTokens(view.MaxCompletionTokens, options.Admin, limits),
		)
	case hasMaxTokens:
		resolved = capOutputTokens(view.MaxTokens, options.Admin, limits)
	case hasMaxCompletionTokens:
		resolved = capOutputTokens(view.MaxCompletionTokens, options.Admin, limits)
	default:
		resolved = capOutputTokens(0, options.Admin, limits)
	}
	document.Set("max_tokens", resolved)
	view.MaxTokens = resolved
	keepCallerMinTokens(document, resolved)
	if hasMaxCompletionTokens {
		document.Set("max_completion_tokens", resolved)
		view.MaxCompletionTokens = resolved
	} else {
		view.MaxCompletionTokens = 0
	}
}

// keepCallerMinTokens forwards only a min_tokens the client sent, clamped to max_tokens, and none on a grammar-constrained request. See README.md, "Output token limits".
func keepCallerMinTokens(document *Document, maxTokens uint64) {
	requested, sent := document.Uint("min_tokens")
	if !sent || requested == 0 || completionapi.GrammarConstrained(document.Raw()) {
		document.Delete("min_tokens")
		return
	}
	document.Set("min_tokens", min(requested, maxTokens))
}

// raiseOutputTokensToProfileFloor raises a budget under the profile's own floor to it, ahead of the refusal below. See README.md, "Output token limits".
func raiseOutputTokensToProfileFloor() RuleFunc {
	return func(ctx RuleContext) error {
		floor := ctx.Profile.outputTokensFloor()
		if floor == outputTokensFloorNone {
			return nil
		}
		raw, exists := ctx.Document.Get(ctx.Param)
		if !exists {
			return nil
		}
		if number, ok := devshard.JSONNumericFloat64(raw); !ok || number >= float64(floor) {
			return nil
		}
		ctx.Document.Set(ctx.Param, floor)
		return nil
	}
}

// rejectNonPositiveOutputTokens: a zero output budget makes no answer, and redundancy waits out a winner that cannot come.
func rejectNonPositiveOutputTokens() RuleFunc {
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get(ctx.Param)
		if !exists {
			return nil
		}
		number, ok := devshard.JSONNumericFloat64(raw)
		if !ok {
			return nil
		}
		if number > 0 {
			return nil
		}
		return Reject("%s: must be greater than 0", ctx.Param)
	}
}
