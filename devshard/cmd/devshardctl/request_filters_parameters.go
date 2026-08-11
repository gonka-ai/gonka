package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"devshard"
	"devshard/cmd/devshardctl/filtercore"
	"devshard/cmd/devshardctl/paramvalidators"
)

var bytesReaderPool = sync.Pool{
	New: func() any { return new(bytes.Reader) },
}

type RequestFilterStage int

const (
	// PreValidation rules run on the raw request document before we decode and validate it.
	RequestFilterStagePreValidation RequestFilterStage = iota
	// PostLimits rules run after max token defaults/caps are resolved back into the document.
	RequestFilterStagePostLimits
)

// ParameterRule describes one transformation for a field at a specific pipeline stage.
type ParameterRule struct {
	Stage   RequestFilterStage
	Handler ParameterHandler
}

type VLLMParameter struct {
	Name  string
	Rules []ParameterRule
}

type ParameterHandler interface {
	Apply(*RequestFilterContext, VLLMParameter) error
}

// ParameterHandlerAdapter wraps a paramvalidators.ParameterHandler (which operates on a
// raw map[string]any) so the catalog can drive it through the standard ParameterHandler
// contract. Mirrors DocumentValidatorHandler's role for DocumentValidator.
type ParameterHandlerAdapter struct {
	Handler paramvalidators.ParameterHandler
}

func (h ParameterHandlerAdapter) Apply(ctx *RequestFilterContext, parameter VLLMParameter) error {
	if h.Handler == nil {
		return nil
	}
	var handlerErr error
	ctx.Document.LockedScope(func(raw map[string]any) {
		handlerErr = h.Handler.HandleParameter(paramvalidators.ParameterContext{
			Document:    raw,
			Parameter:   parameter.Name,
			RoutedModel: ctx.RoutedModel,
		})
	})
	if handlerErr != nil {
		return wrapBadChatRequest(handlerErr)
	}
	return nil
}

// ModelScopedParameterHandler runs Handler when ctx.RoutedModel matches one of Models
// (exact, case-sensitive), and UnmatchedHandler otherwise. Either handler may be nil for
// a no-op on that path.
type ModelScopedParameterHandler struct {
	Models           []string
	Handler          ParameterHandler
	UnmatchedHandler ParameterHandler
}

func (h ModelScopedParameterHandler) Apply(ctx *RequestFilterContext, parameter VLLMParameter) error {
	if filtercore.MatchesModel(ctx.RoutedModel, h.Models) {
		if h.Handler == nil {
			return nil
		}
		return h.Handler.Apply(ctx, parameter)
	}
	if h.UnmatchedHandler == nil {
		return nil
	}
	return h.UnmatchedHandler.Apply(ctx, parameter)
}

// DocumentValidator: validators in paramvalidators expose this contract. May mutate
// vctx.Document for per-model rewrites alongside shape checks.
type DocumentValidator interface {
	Validate(paramvalidators.ValidatorContext) error
}

type DocumentValidatorHandler struct {
	Validator DocumentValidator
}

func (h DocumentValidatorHandler) Apply(ctx *RequestFilterContext, _ VLLMParameter) error {
	if h.Validator == nil {
		return nil
	}
	var validateErr error
	ctx.Document.LockedScope(func(raw map[string]any) {
		validateErr = h.Validator.Validate(paramvalidators.ValidatorContext{
			Document:    raw,
			RoutedModel: ctx.RoutedModel,
		})
	})
	if validateErr != nil {
		return wrapBadChatRequest(validateErr)
	}
	return nil
}

// ChatRequestDocument is not safe to share across goroutines without the mutex;
// use LockedScope / RLockedScope for multi-key access.
type ChatRequestDocument struct {
	mu  sync.RWMutex
	raw map[string]any
}

func (d *ChatRequestDocument) Keys() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	keys := make([]string, 0, len(d.raw))
	for key := range d.raw {
		keys = append(keys, key)
	}
	return keys
}

func (d *ChatRequestDocument) LockedScope(fn func(map[string]any)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d.raw)
}

func (d *ChatRequestDocument) RLockedScope(fn func(map[string]any)) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	fn(d.raw)
}

func decodeChatRequestDocument(body []byte) (*ChatRequestDocument, error) {
	raw, err := decodeChatRequestRaw(body)
	if err != nil {
		return nil, err
	}
	return &ChatRequestDocument{raw: raw}, nil
}

func decodeChatRequestRaw(body []byte) (map[string]any, error) {
	if err := ensureRequestNestingDepth(body, MaxRequestNestingDepth); err != nil {
		return nil, err
	}
	reader := bytesReaderPool.Get().(*bytes.Reader)
	reader.Reset(body)
	defer func() {
		// Drop body reference so the pool doesn't pin 10 MiB slices.
		reader.Reset(nil)
		bytesReaderPool.Put(reader)
	}()
	var raw map[string]any
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, badChatRequest("parse request: %v", err)
	}
	return raw, nil
}

// ensureRequestNestingDepth performs a byte-level scan that bounds JSON nesting before any
// allocation-heavy decode happens. It tracks quoted strings and escape sequences but
// otherwise ignores semantic structure -- the goal is to bound the decoder, not to validate
// JSON shape; malformed JSON still flows through to the regular parser and gets a normal
// HTTP 400.
func ensureRequestNestingDepth(body []byte, maxDepth int) error {
	depth := 0
	inString := false
	escaped := false
	for _, b := range body {
		if escaped {
			escaped = false
			continue
		}
		if inString {
			switch b {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maxDepth {
				return badChatRequest("request nesting depth exceeds limit %d", maxDepth)
			}
		case '}', ']':
			depth--
			if depth < 0 {
				// More closers than openers. The decoder will reject the malformed body
				// with a normal parse error later; rebase to 0 so subsequent valid blocks
				// are still bounded by maxDepth instead of needing maxDepth+|imbalance|
				// extra opens before tripping the cap.
				depth = 0
			}
		}
	}
	return nil
}

func (d *ChatRequestDocument) Marshal() ([]byte, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	updatedBody, err := json.Marshal(d.raw)
	if err != nil {
		return nil, badChatRequest("marshal request: %v", err)
	}
	return updatedBody, nil
}

func (d *ChatRequestDocument) Has(name string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.raw[name]
	return ok
}

func (d *ChatRequestDocument) Get(name string) (any, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	value, ok := d.raw[name]
	return value, ok
}

func (d *ChatRequestDocument) Set(name string, value any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.raw[name] = value
}

func (d *ChatRequestDocument) Delete(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.raw, name)
}

func (d *ChatRequestDocument) String(name string) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	value, ok := d.raw[name].(string)
	return value, ok
}

// Object and Array return references into the document; do not retain them past
// the immediate use or mutate them concurrently with other writers.
func (d *ChatRequestDocument) Object(name string) (map[string]any, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	value, ok := d.raw[name].(map[string]any)
	return value, ok
}

func (d *ChatRequestDocument) Array(name string) ([]any, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	value, ok := d.raw[name].([]any)
	return value, ok
}

type RequestFilterContext struct {
	Document           ChatRequestDocument
	OutputLimits       outputTokenLimits
	AdminAuthenticated bool
	Request            chatRequest
	RoutedModel        string
}

func newRequestFilterContext(body []byte, adminAuthenticated bool, limits outputTokenLimits) (*RequestFilterContext, error) {
	raw, err := decodeChatRequestRaw(body)
	if err != nil {
		return nil, err
	}
	return &RequestFilterContext{
		Document:           ChatRequestDocument{raw: raw},
		OutputLimits:       normalizedOutputTokenLimits(limits),
		AdminAuthenticated: adminAuthenticated,
	}, nil
}

// ResolveRoutedModel sets ctx.RoutedModel: trimmed body.model wins, else the fallback.
func (ctx *RequestFilterContext) ResolveRoutedModel(fallback string) {
	if m, ok := ctx.Document.String("model"); ok {
		if trimmed := strings.TrimSpace(m); trimmed != "" {
			ctx.RoutedModel = trimmed
			return
		}
	}
	ctx.RoutedModel = fallback
}

// DecodeRequest populates ctx.Request from ctx.Document via direct field reads. Previously
// this was a json.Marshal + json.Unmarshal round-trip just to project the document into a
// 5-field struct -- that doubled the allocation count on every request. Direct reads keep
// the behavior (strict types, null-tolerant) but skip the round-trip.
func (ctx *RequestFilterContext) DecodeRequest() error {
	var req chatRequest
	if err := readChatRequestFields(&ctx.Document, &req); err != nil {
		return err
	}
	ctx.Request = req
	return nil
}

// SyncRequestView refreshes ctx.Request after PostLimits rules ran. The token fields
// are preserved from ctx.Request rather than re-read because applyOutputTokenLimits is
// their source of truth; all four of its branches now write the same max_tokens /
// max_completion_tokens into the document (the max-completion-only branch mirrors into
// max_tokens too), so this preservation is a harmless no-op safety net.
//
// Other fields are re-read so caps applied by PostLimits rules (for example `n` via
// paramvalidators.CapUintParameter through the adapter) propagate into the projection.
func (ctx *RequestFilterContext) SyncRequestView() error {
	var req chatRequest
	if err := readChatRequestFields(&ctx.Document, &req); err != nil {
		return err
	}
	req.MaxTokens = ctx.Request.MaxTokens
	req.MaxCompletionTokens = ctx.Request.MaxCompletionTokens
	// Preserve the client's ORIGINAL logprobs intent. PostLimits force-sets
	// logprobs=true / top_logprobs=<forced> in the document for validation, so
	// re-reading them here would capture the forced values, not what the client
	// asked. DecodeRequest already captured the original before PostLimits ran.
	req.Logprobs = ctx.Request.Logprobs
	req.TopLogprobs = ctx.Request.TopLogprobs
	ctx.Request = req
	return nil
}

func readChatRequestFields(doc *ChatRequestDocument, req *chatRequest) error {
	if raw, ok := doc.Get("model"); ok && raw != nil {
		s, ok := raw.(string)
		if !ok {
			return badChatRequest("parse request: model must be a string")
		}
		req.Model = s
	}
	if raw, ok := doc.Get("stream"); ok && raw != nil {
		b, ok := raw.(bool)
		if !ok {
			return badChatRequest("parse request: stream must be a boolean")
		}
		req.Stream = b
	}
	if err := readUint64Field(doc, "max_tokens", &req.MaxTokens); err != nil {
		return err
	}
	if err := readUint64Field(doc, "max_completion_tokens", &req.MaxCompletionTokens); err != nil {
		return err
	}
	if err := readUint64Field(doc, "n", &req.N); err != nil {
		return err
	}
	// logprobs/top_logprobs: lenient capture of the client's original intent.
	// Only an explicit boolean true (and a positive top_logprobs) counts as a
	// request; any other shape is treated as "not requested" so we never reject a
	// request the gateway previously accepted -- the PostLimits ForceLiteral
	// rules overwrite both fields regardless of incoming type. Cf.
	// logprobClientIntent and conditional response stripping.
	if raw, ok := doc.Get("logprobs"); ok {
		if b, isBool := raw.(bool); isBool {
			req.Logprobs = b
		}
	}
	if raw, ok := doc.Get("top_logprobs"); ok {
		if n, isNum := devshard.JSONNumericUint64(raw); isNum {
			req.TopLogprobs = n
		}
	}
	return nil
}

func readUint64Field(doc *ChatRequestDocument, name string, dst *uint64) error {
	raw, ok := doc.Get(name)
	if !ok || raw == nil {
		return nil
	}
	n, ok := devshard.JSONNumericUint64(raw)
	if !ok {
		return badChatRequest("parse request: %s must be a non-negative integer", name)
	}
	*dst = n
	return nil
}

type VLLMParameterCatalog struct {
	parameters []VLLMParameter
	known      map[string]struct{}
}

var defaultParameterCatalog = defaultVLLMParameterCatalog()

// Shared stateless parameter handlers reused across catalog entries. The rejectNumber gates
// enforce exclusive-lower-bound ranges (clamping to the bound would itself be an illegal
// value, so reject instead); the mustBe*/elementsMustBe* validators reject wrong-typed
// scalars and array elements at the gateway boundary rather than forwarding them for an
// opaque upstream 400.
var (
	rejectNonPositiveNumber = ParameterHandlerAdapter{Handler: paramvalidators.RejectNumberParameter{
		Allow:   func(value float64) bool { return value > 0 },
		Message: "must be greater than 0",
	}}
	rejectInvalidTopK = ParameterHandlerAdapter{Handler: paramvalidators.RejectNumberParameter{
		Allow:   func(value float64) bool { return value == -1 || value >= 1 },
		Message: "must be -1 or a positive integer",
	}}
	mustBeBool           = ParameterHandlerAdapter{Handler: paramvalidators.ValidateScalarParameter{Valid: paramvalidators.IsJSONBool, Message: "must be a boolean"}}
	mustBeUint           = ParameterHandlerAdapter{Handler: paramvalidators.ValidateUintParameter{}}
	elementsMustBeUint   = ParameterHandlerAdapter{Handler: paramvalidators.ValidateListElementsParameter{Valid: paramvalidators.IsJSONUint, Message: "must be an integer token id"}}
	elementsMustBeString = ParameterHandlerAdapter{Handler: paramvalidators.ValidateListElementsParameter{Valid: paramvalidators.IsJSONString, Message: "must be a string"}}
)

// The catalog is the single source of truth for how each supported OpenAI/vLLM field is treated.
func defaultVLLMParameterCatalog() VLLMParameterCatalog {
	parameters := slices.Concat(
		[]VLLMParameter{
			newParameter("messages").
				withRule(RequestFilterStagePreValidation, ParameterHandlerAdapter{Handler: paramvalidators.LengthCapListParameter{MaxEntries: MessagesMaxEntries}}),
			newParameter("seed").
				withRule(RequestFilterStagePreValidation, mustBeUint),
			newParameter("n").
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.CapUintParameter{Min: 1, Max: MaxChatRequestChoices}}).
				withRule(RequestFilterStagePostLimits, DocumentValidatorHandler{
					Validator: paramvalidators.GreedySamplingValidator{},
				}),
			newParameter("temperature").
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.SanitizeFloatParameter{StripNonFinite: true, Min: floatPointer(MinTemperature), Max: floatPointer(MaxTemperature)}}),
			newParameter("repetition_penalty").
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.SanitizeFloatParameter{StripNonFinite: true, Max: floatPointer(MaxRepetitionPenalty)}}).
				withRule(RequestFilterStagePostLimits, rejectNonPositiveNumber),
			newParameter("logit_bias").
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.SanitizeFloatMapParameter{StripNonFinite: true, Min: floatPointer(LogitBiasMinValue), Max: floatPointer(LogitBiasMaxValue), DropFieldIfEmpty: true, MaxEntries: LogitBiasMaxEntries}}),
			newParameter("stop").
				withRule(RequestFilterStagePreValidation, elementsMustBeString).
				withRule(RequestFilterStagePreValidation, ParameterHandlerAdapter{Handler: paramvalidators.LengthCapListParameter{MaxEntries: StopMaxEntries, MaxEntryLen: StopMaxEntryLen}}),
			newParameter("stop_token_ids").
				withRule(RequestFilterStagePreValidation, ParameterHandlerAdapter{Handler: paramvalidators.LengthCapListParameter{MaxEntries: StopTokenIdsMaxEntries}}).
				withRule(RequestFilterStagePreValidation, elementsMustBeUint),
			newParameter("reasoning").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.ReasoningValidator{},
				}),
			// reasoning_effort: enum-validate then strip. Models: nil keeps the strip
			// universal until a reasoning-capable model is routed. List models in Models
			// to start forwarding.
			newParameter("reasoning_effort").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.ReasoningEffortValidator{},
				}).
				withRule(RequestFilterStagePreValidation, ModelScopedParameterHandler{
					Models:           nil,
					UnmatchedHandler: ParameterHandlerAdapter{Handler: paramvalidators.StripParameter{}},
				}),
			// MiniMax-M2.7 has no chat_template knob for enable_thinking (vLLM #36778);
			// strip on this route before EnableThinkingValidator runs.
			newParameter("enable_thinking").
				withRule(RequestFilterStagePreValidation, ModelScopedParameterHandler{
					Models:  []string{miniMaxM27ModelID},
					Handler: ParameterHandlerAdapter{Handler: paramvalidators.StripParameter{}},
				}).
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.EnableThinkingValidator{},
				}),
			// thinking: Kimi mirrors to chat_template_kwargs.thinking; MiniMax-M2.7 has no
			// equivalent knob (interleaved thinking is structural to the chat template) so
			// strip the field before ThinkingValidator runs. Other routes normalize+keep.
			newParameter("thinking").
				withRule(RequestFilterStagePreValidation, ModelScopedParameterHandler{
					Models:  []string{miniMaxM27ModelID},
					Handler: ParameterHandlerAdapter{Handler: paramvalidators.StripParameter{}},
				}).
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.ThinkingValidator{
						MirrorToTemplateKwargsForModels: []string{kimiK26ModelID},
					},
				}),
			newParameter("chat_template_kwargs").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.ChatTemplateKwargsValidator{
						MaxDepth: ChatTemplateKwargsMaxDepth,
						MaxSize:  ChatTemplateKwargsMaxSize,
						MaxNodes: ChatTemplateKwargsMaxNodes,
					},
				}),
			newParameter("thinking_token_budget").
				withRule(RequestFilterStagePreValidation, ModelScopedParameterHandler{
					Models:           []string{kimiK26ModelID},
					UnmatchedHandler: ParameterHandlerAdapter{Handler: paramvalidators.StripParameter{}},
				}).
				withRule(RequestFilterStagePostLimits, DocumentValidatorHandler{
					Validator: paramvalidators.KimiThinkingTokenBudgetValidator{
						Model:                   kimiK26ModelID,
						DefaultDivisor:          kimiThinkingTokenBudgetDefaultDivisor,
						AbsoluteMax:             kimiThinkingTokenBudgetMax,
						ContentHeadroom:         kimiContentHeadroomMin,
						ForceZeroBelowMaxTokens: kimiSmallMaxTokensForceNoThinking,
					},
				}),
			newParameter("tools").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.ToolsValidator{
						MaxDepth:          ToolsMaxDepth,
						MaxSize:           ToolsMaxSize,
						MaxNodes:          ToolsMaxNodes,
						MaxBranch:         ToolsMaxBranch,
						MaxEnum:           ToolsMaxEnum,
						MaxPatternLen:     ToolsMaxPatternLen,
						DefaultToolChoice: "auto",
					},
				}),
			newParameter("tool_choice").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.ToolChoiceValidator{MaxNameLen: ToolChoiceMaxNameLen},
				}),
			newParameter("min_tokens").
				withRule(RequestFilterStagePreValidation, mustBeUint).
				withRule(RequestFilterStagePreValidation, ParameterHandlerAdapter{Handler: paramvalidators.ConditionalStripParameter{
					Predicate: func(ctx paramvalidators.ParameterContext) bool {
						_, ok := ctx.Document["stop_token_ids"]
						return ok
					},
				}}).
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.ClampUintToFieldParameter{MaxField: "max_tokens"}}),
			newParameter("bad_words").
				withRule(RequestFilterStagePreValidation, elementsMustBeString).
				withRule(RequestFilterStagePreValidation, ParameterHandlerAdapter{Handler: paramvalidators.SanitizeStringListParameter{
					Keep: func(value string) bool {
						return strings.TrimSpace(value) != ""
					},
					DropFieldIfEmpty: true,
				}}).
				withRule(RequestFilterStagePreValidation, ParameterHandlerAdapter{Handler: paramvalidators.LengthCapListParameter{MaxEntries: BadWordsMaxEntries, MaxEntryLen: BadWordsMaxEntryLen}}),
			// OpenAI Chat Completions standard observability fields. No inference-side
			// semantics on the vLLM upstream; clients send them for end-user tracking,
			// distributed tracing, agent control, and streaming token accounting.
			// `user`: type-checked and byte-capped at the gateway boundary so a non-string
			// payload (number, object, …) and an over-long string are caught early instead
			// of being forwarded as a no-op carrier under the 10 MiB body cap.
			newParameter("user").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.StringFieldValidator{
						FieldName:     "user",
						DefaultMaxLen: UserMaxLen,
					},
				}),
			// metadata: OpenAI bounds it to 16 keys × 64-char keys × 512-char string values;
			// we enforce the same bounds at the gateway boundary as a free defensive cap.
			newParameter("metadata").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.MetadataValidator{},
				}),
			// stream_options: sub-field whitelist. Only `include_usage` survives;
			// `continuous_usage_stats` is stripped to neutralize vLLM-project/vllm#9028
			// (per-chunk usage counter is wrong under chunked prefill), and any other /
			// future sub-field is default-stripped. If nothing remains the field is dropped
			// so the upstream does not receive an empty `{}` object.
			newParameter("stream_options").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.StreamOptionsValidator{},
				}),
			newParameter("return_token_ids").
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.ForceLiteralParameter{Value: true}}),
			newParameter("logprobs").
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.ForceLiteralParameter{Value: true}}),
			newParameter("top_logprobs").
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.ForceLiteralParameter{Value: TopLogprobsForcedValue}}),
			newParameter("response_format").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.ResponseFormatValidator{
						MaxDepth:      ResponseFormatMaxDepth,
						MaxSize:       ResponseFormatMaxSize,
						MaxNodes:      ResponseFormatMaxNodes,
						MaxBranch:     ResponseFormatMaxBranch,
						MaxEnum:       ResponseFormatMaxEnum,
						MaxNameLen:    ResponseFormatMaxNameLen,
						MaxPatternLen: ResponseFormatMaxPatternLen,
					},
				}),
			newParameter("structured_outputs").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.StructuredOutputsValidator{
						RejectedModels:      []string{kimiK26ModelID},
						MaxDepth:            StructuredOutputsMaxDepth,
						MaxSize:             StructuredOutputsMaxSize,
						MaxNodes:            StructuredOutputsMaxNodes,
						MaxBranch:           StructuredOutputsMaxBranch,
						MaxEnum:             StructuredOutputsMaxEnum,
						MaxPatternLen:       StructuredOutputsMaxPatternLen,
						MaxChoiceEntries:    StructuredOutputsMaxChoiceEntries,
						MaxChoiceEntryLen:   StructuredOutputsMaxChoiceEntryLen,
						MaxGrammarLen:       StructuredOutputsMaxGrammarLen,
						MaxGrammarNesting:   StructuredOutputsMaxGrammarNesting,
						MaxStructuralTagLen: StructuredOutputsMaxStructuralTagLen,
					},
				}),
			newParameter("safety_identifier").
				withRule(RequestFilterStagePreValidation, ModelScopedParameterHandler{
					Models: []string{kimiK26ModelID},
					Handler: DocumentValidatorHandler{
						Validator: paramvalidators.StringFieldValidator{
							FieldName:     "safety_identifier",
							DefaultMaxLen: SafetyIdentifierMaxLen,
						},
					},
					UnmatchedHandler: ParameterHandlerAdapter{Handler: paramvalidators.StripParameter{}},
				}),
			// MiniMax-M2.7 native extension lifted from extra_body. On the MiniMax
			// route the field passes through to vLLM verbatim (controls whether
			// reasoning is emitted as inline <think>...</think> in content or as a
			// separate reasoning_details[] array). Stripped on other routes since
			// Kimi/Qwen vLLM serves don't know the field. See docs/chat-api/minimax-m2.7.md.
			newParameter("reasoning_split").
				withRule(RequestFilterStagePreValidation, ModelScopedParameterHandler{
					Models:           []string{miniMaxM27ModelID},
					UnmatchedHandler: ParameterHandlerAdapter{Handler: paramvalidators.StripParameter{}},
				}),
			// PreValidation so the floor lands before applyOutputTokenLimits caps down.
			// One validator covers both max_tokens and max_completion_tokens.
			newParameter("max_tokens").
				withRule(RequestFilterStagePreValidation, DocumentValidatorHandler{
					Validator: paramvalidators.KimiMaxTokensFloorValidator{
						Model: kimiK26ModelID,
						Min:   kimiMaxTokensMin,
					},
				}).
				withRule(RequestFilterStagePreValidation, ModelScopedParameterHandler{
					Models:           []string{kimiK26ModelID},
					UnmatchedHandler: rejectNonPositiveNumber,
				}),
			newParameter("max_completion_tokens").
				withRule(RequestFilterStagePreValidation, ModelScopedParameterHandler{
					Models:           []string{kimiK26ModelID},
					UnmatchedHandler: rejectNonPositiveNumber,
				}),
			// Sampling knobs with per-field ranges: min_p clamps into [0, 1]; top_p clamps
			// down to 1 but rejects a non-positive value (exclusive lower bound); top_k
			// accepts -1 (disabled) or any integer >= 1.
			newParameter("min_p").
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.SanitizeFloatParameter{StripNonFinite: true, Min: floatPointer(MinPMin), Max: floatPointer(MinPMax)}}),
			newParameter("top_p").
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.SanitizeFloatParameter{StripNonFinite: true, Max: floatPointer(TopPMax)}}).
				withRule(RequestFilterStagePostLimits, rejectNonPositiveNumber),
			newParameter("top_k").
				withRule(RequestFilterStagePostLimits, ParameterHandlerAdapter{Handler: paramvalidators.SanitizeFloatParameter{StripNonFinite: true}}).
				withRule(RequestFilterStagePostLimits, rejectInvalidTopK),
		},
		// model and stream are type-checked during the typed parse (string / bool); register
		// them as known so the whitelist keeps them.
		newParameters([]string{
			"model",
			"stream",
		}),
		// The remaining boolean flags are pass-through fields, so validate their type here.
		newParameters([]string{"skip_special_tokens", "detokenize", "parallel_tool_calls"},
			ParameterRule{Stage: RequestFilterStagePreValidation, Handler: mustBeBool},
		),
		newParameters([]string{"service_tier", "store", "provider", "plugins", "prompt_cache_key", "cache_key", "extra_headers", "thinking_config", "think"},
			ParameterRule{Stage: RequestFilterStagePreValidation, Handler: ParameterHandlerAdapter{Handler: paramvalidators.StripParameter{}}},
		),
		// frequency_penalty / presence_penalty share identical rules: catalog clamp
		// [-2, 2] for all models + per-Kimi force-rewrite to 0.0 (Moonshot's K2.6 wire
		// accepts only 0.0 -- model-side constraint).
		newParameters([]string{"frequency_penalty", "presence_penalty"},
			ParameterRule{
				Stage:   RequestFilterStagePostLimits,
				Handler: ParameterHandlerAdapter{Handler: paramvalidators.SanitizeFloatParameter{StripNonFinite: true, Min: floatPointer(PenaltyMin), Max: floatPointer(PenaltyMax)}},
			},
			ParameterRule{
				Stage: RequestFilterStagePostLimits,
				Handler: ModelScopedParameterHandler{
					Models:  []string{kimiK26ModelID},
					Handler: ParameterHandlerAdapter{Handler: paramvalidators.ForceLiteralParameter{Value: KimiK2PenaltyForcedValue, OverwriteOnly: true}},
				},
			},
		),
	)
	known := make(map[string]struct{}, len(parameters))
	for _, p := range parameters {
		known[p.Name] = struct{}{}
	}
	return VLLMParameterCatalog{parameters: parameters, known: known}
}

func (c VLLMParameterCatalog) Apply(stage RequestFilterStage, ctx *RequestFilterContext) error {
	if stage == RequestFilterStagePreValidation {
		// PreValidation pre-passes run before rejectUnknownParameters so lifted keys are
		// subject to the standard whitelist. Keep them side-effect-light and ordered.
		c.unwrapExtraBody(ctx)
		if err := c.rejectUnknownParameters(ctx); err != nil {
			return err
		}
	}
	for _, parameter := range c.parameters {
		for _, rule := range parameter.Rules {
			if rule.Stage != stage || rule.Handler == nil {
				continue
			}
			if err := rule.Handler.Apply(ctx, parameter); err != nil {
				return err
			}
		}
	}
	return nil
}

// unwrapExtraBody flattens an OpenAI-SDK-style `extra_body` envelope into top-level
// fields before the unknown-parameter check runs. Lifted keys then flow through the
// catalog's normal validation. Top-level keys always win on conflict; non-object
// envelopes and nested `extra_body` keys are dropped without surfacing.
func (c VLLMParameterCatalog) unwrapExtraBody(ctx *RequestFilterContext) {
	if ctx == nil {
		return
	}
	ctx.Document.LockedScope(func(raw map[string]any) {
		envelope, exists := raw["extra_body"]
		if !exists {
			return
		}
		delete(raw, "extra_body")
		inner, ok := envelope.(map[string]any)
		if !ok {
			return
		}
		for key, value := range inner {
			if key == "extra_body" {
				continue
			}
			if _, alreadyTop := raw[key]; alreadyTop {
				continue
			}
			raw[key] = value
		}
	})
}

func (c VLLMParameterCatalog) rejectUnknownParameters(ctx *RequestFilterContext) error {
	if ctx == nil {
		return nil
	}
	var rejectErr error
	ctx.Document.RLockedScope(func(raw map[string]any) {
		for key := range raw {
			if _, ok := c.known[key]; ok {
				continue
			}
			if key == "" {
				rejectErr = badChatRequest("request body contains a field with an empty name")
				return
			}
			rejectErr = badChatRequest("%s", unsupportedChatParameterMessage(key))
			return
		}
	})
	return rejectErr
}

func newParameter(name string) VLLMParameter {
	return VLLMParameter{Name: name}
}

func (p VLLMParameter) withRule(stage RequestFilterStage, handler ParameterHandler) VLLMParameter {
	p.Rules = append(p.Rules, ParameterRule{Stage: stage, Handler: handler})
	return p
}

func newParameters(names []string, rules ...ParameterRule) []VLLMParameter {
	out := make([]VLLMParameter, len(names))
	for i, name := range names {
		copied := append([]ParameterRule(nil), rules...)
		out[i] = VLLMParameter{Name: name, Rules: copied}
	}
	return out
}

func floatPointer(value float64) *float64 {
	return &value
}
