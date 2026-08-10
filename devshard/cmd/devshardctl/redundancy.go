package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devshard"
	"devshard/accounting"
	"devshard/host"
	"devshard/logging"
	"devshard/transport"
	"devshard/types"
	"devshard/user"
)

// errEmptyStream marks an attempt that completed successfully at the transport
// layer but produced no content tokens. The host returned only protocol/SSE
// boilerplate (role chunk, [DONE]) without any actual delta content. We treat
// this as a failure so redundancy can retry on a different host and the
// offending host is recorded as non-responsive in the local PerfTracker.
var errEmptyStream = errors.New("empty content stream")

const emptyStreamBodySampleLimit = 256 * 1024

const longResponseFailureExemption = 280 * time.Second

var (
	nonStreamingReducedMaxTokensFallbackDelay = 140 * time.Second
	nonStreamingNoContentTimeout              = 30 * time.Minute
	nonStreamingMaxAttemptWait                = 30 * time.Minute
	InterChunkStallLogThreshold               = 30 * time.Second
	StreamingAttemptHardTimeout               = 30 * time.Minute
)

const toolChoiceUnsupportedMessage = "tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set"

type nonStreamingReducedMaxTokensTimeoutError struct{}

func (e *nonStreamingReducedMaxTokensTimeoutError) Error() string {
	return "inference: no non-empty response after retrying with reduced max_tokens"
}

// sseChunkHasContent reports whether the given bytes contain at least one SSE
// data event carrying a non-empty payload that an OpenAI-compatible client can
// surface. `content`, `reasoning`, `reasoning_content`, non-empty
// `tool_calls`, and a stopped completion with generated tokens all qualify in
// both streaming `delta` and non-streaming `message` shapes.
//
// Deliberately NOT treated as content (even though earlier versions did):
//   - `choices[].text` — the legacy `/v1/completions` shape. The proxy's
//     streaming path only serves `/v1/chat/completions`; a host emitting
//     `text` here produces the same "1 chunk, 0 rendered tokens" failure.
//
// Role-only chunks, empty deltas, finish-only chunks, and `[DONE]` markers
// continue to return false.
func sseChunkHasContent(p []byte) bool {
	_, ok := sseChunkContentSource(p)
	return ok
}

var sseUsageKeyMarker = []byte(`"usage"`)

// sseChunkUsageCompletionTokens reads usage.completion_tokens from an SSE
// data event. Includes tokens vLLM stripped from `content` (e.g. </think>).
// Fast-skips chunks that do not contain the `"usage"` key — vLLM emits the
// usage object only in the final chunk of a stream, so 99%+ of chunks avoid
// the json.Unmarshal cost entirely.
func sseChunkUsageCompletionTokens(p []byte) (int64, bool) {
	if len(p) == 0 || !bytes.Contains(p, sseUsageKeyMarker) {
		return 0, false
	}
	for _, line := range bytes.Split(p, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var evt struct {
			Usage *struct {
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(payload, &evt); err != nil {
			continue
		}
		if evt.Usage != nil && evt.Usage.CompletionTokens > 0 {
			return evt.Usage.CompletionTokens, true
		}
	}
	return 0, false
}

// sseChunkContentSource is the classifying variant of sseChunkHasContent: when
// content is present it returns a short label identifying the field that
// carried it. The second return value is false when no accepted content was
// found. Used for forensic logging so we can tell, after the fact, exactly
// which field a short-content winner was emitting.
func sseChunkContentSource(p []byte) (string, bool) {
	if len(p) == 0 {
		return "", false
	}
	for _, line := range bytes.Split(p, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var evt struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content          string          `json:"content"`
					Reasoning        string          `json:"reasoning"`
					ReasoningContent string          `json:"reasoning_content"`
					ToolCalls        json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
				Message struct {
					Content          string          `json:"content"`
					Reasoning        string          `json:"reasoning"`
					ReasoningContent string          `json:"reasoning_content"`
					ToolCalls        json.RawMessage `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(payload, &evt); err != nil {
			continue
		}
		for _, c := range evt.Choices {
			if c.Delta.Content != "" {
				return "delta.content", true
			}
			if c.Delta.Reasoning != "" {
				return "delta.reasoning", true
			}
			if c.Delta.ReasoningContent != "" {
				return "delta.reasoning_content", true
			}
			if hasJSONArrayElements(c.Delta.ToolCalls) {
				return "delta.tool_calls", true
			}
			if c.Message.Content != "" {
				return "message.content", true
			}
			if c.Message.Reasoning != "" {
				return "message.reasoning", true
			}
			if c.Message.ReasoningContent != "" {
				return "message.reasoning_content", true
			}
			if hasJSONArrayElements(c.Message.ToolCalls) {
				return "message.tool_calls", true
			}
			if c.FinishReason == "stop" && evt.Usage.CompletionTokens > 0 {
				return "message.empty_stop_completion_tokens", true
			}
		}
	}
	return "", false
}

// sseChunkErrorSource reports whether the bytes contain an OpenAI-style
// top-level error response in an SSE data event. These responses are failures,
// but not empty streams: the host did send a meaningful application response.
func sseChunkErrorSource(p []byte) (string, bool) {
	details, ok := sseChunkErrorDetails(p)
	if !ok {
		return "", false
	}
	if details.Type != "" {
		return "error." + details.Type, true
	}
	return "error", true
}

type sseErrorDetails struct {
	Code    string
	Type    string
	Message string
}

type hostApplicationError struct {
	details sseErrorDetails
	payload []byte
}

func (e *hostApplicationError) Error() string {
	if e == nil {
		return "host application error"
	}
	if e.details.Message != "" {
		return e.details.Message
	}
	if len(e.payload) > 0 {
		return string(e.payload)
	}
	return "host application error"
}

func (e *hostApplicationError) statusCode() int {
	if e == nil {
		return http.StatusBadGateway
	}
	status, err := strconv.Atoi(e.details.Code)
	if err == nil && status >= 400 && status <= 599 {
		return status
	}
	if strings.Contains(strings.ToLower(e.details.Type), "badrequest") {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func (e *hostApplicationError) jsonPayload() []byte {
	if e == nil {
		return nil
	}
	if len(e.payload) > 0 {
		return append([]byte(nil), e.payload...)
	}
	errorBody := map[string]any{
		"message": e.Error(),
	}
	if e.details.Type != "" {
		errorBody["type"] = e.details.Type
	}
	if e.details.Code != "" {
		errorBody["code"] = e.details.Code
	}
	body := map[string]any{"error": errorBody}
	data, err := json.Marshal(body)
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":{"message":%q}}`, e.Error()))
	}
	return data
}

// sseChunkErrorDetails extracts the first OpenAI-compatible top-level error
// from an SSE data event. The raw body is still logged separately, but these
// fields make later grep/aggregation possible without decoding JSON by hand.
func sseChunkErrorDetails(p []byte) (sseErrorDetails, bool) {
	details, _, ok := sseChunkErrorPayload(p)
	return details, ok
}

func sseChunkErrorPayload(p []byte) (sseErrorDetails, []byte, bool) {
	if len(p) == 0 {
		return sseErrorDetails{}, nil, false
	}
	for _, line := range bytes.Split(p, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var evt struct {
			Error *struct {
				Type    string `json:"type"`
				Code    any    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Object  string `json:"object"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(payload, &evt); err != nil {
			continue
		}
		if evt.Error != nil {
			details := sseErrorDetails{
				Type:    evt.Error.Type,
				Code:    fmt.Sprint(evt.Error.Code),
				Message: evt.Error.Message,
			}
			if evt.Error.Code == nil {
				details.Code = ""
			}
			return details, append([]byte(nil), payload...), true
		}
		if evt.Object == "error" && evt.Message != "" {
			details := sseErrorDetails{
				Type:    evt.Type,
				Code:    fmt.Sprint(evt.Code),
				Message: evt.Message,
			}
			if evt.Code == nil {
				details.Code = ""
			}
			return details, append([]byte(nil), payload...), true
		}
	}
	return sseErrorDetails{}, nil, false
}

// hasJSONArrayElements returns true if raw is a JSON array with at least one
// element. Returns false for null/empty/[]/non-array values.
func hasJSONArrayElements(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	if !bytes.HasPrefix(trimmed, []byte("[")) {
		return false
	}
	inner := bytes.TrimSpace(trimmed[1 : len(trimmed)-1])
	return len(inner) > 0
}

func bodySampleForLog(p []byte, limit int) (string, bool) {
	if len(p) == 0 {
		return "", false
	}
	if limit <= 0 {
		limit = emptyStreamBodySampleLimit
	}
	truncated := len(p) > limit
	if truncated {
		p = p[:limit]
	}
	return string(bytes.ToValidUTF8(p, []byte("\uFFFD"))), truncated
}

func requestBodySampleForLog(params user.InferenceParams) (string, bool) {
	return bodySampleForLog(params.Prompt, emptyStreamBodySampleLimit)
}

func requestFlagsForLog(params user.InferenceParams) string {
	type requestFlags struct {
		Model               string `json:"model,omitempty"`
		Stream              *bool  `json:"stream,omitempty"`
		MaxTokens           any    `json:"max_tokens,omitempty"`
		MaxCompletionTokens any    `json:"max_completion_tokens,omitempty"`
		ToolChoice          any    `json:"tool_choice,omitempty"`
		ToolsCount          int    `json:"tools_count,omitempty"`
		MessagesCount       int    `json:"messages_count,omitempty"`
		ParallelToolCalls   any    `json:"parallel_tool_calls,omitempty"`
		Temperature         any    `json:"temperature,omitempty"`
		TopP                any    `json:"top_p,omitempty"`
		InputTokens         uint64 `json:"input_tokens,omitempty"`
		SignedMaxTokens     uint64 `json:"signed_max_tokens,omitempty"`
		StartedAt           int64  `json:"started_at,omitempty"`
		ParseError          string `json:"parse_error,omitempty"`
	}

	flags := requestFlags{
		Model:           params.Model,
		InputTokens:     params.InputLength,
		SignedMaxTokens: params.MaxTokens,
		StartedAt:       params.StartedAt,
	}

	var raw map[string]any
	if err := json.Unmarshal(params.Prompt, &raw); err != nil {
		flags.ParseError = err.Error()
		return marshalRequestFlags(flags)
	}

	if model, ok := raw["model"].(string); ok {
		flags.Model = model
	}
	if stream, ok := raw["stream"].(bool); ok {
		flags.Stream = &stream
	}
	flags.MaxTokens = raw["max_tokens"]
	flags.MaxCompletionTokens = raw["max_completion_tokens"]
	flags.ToolChoice = raw["tool_choice"]
	flags.ParallelToolCalls = raw["parallel_tool_calls"]
	flags.Temperature = raw["temperature"]
	flags.TopP = raw["top_p"]
	if tools, ok := raw["tools"].([]any); ok {
		flags.ToolsCount = len(tools)
	}
	if messages, ok := raw["messages"].([]any); ok {
		flags.MessagesCount = len(messages)
	}
	return marshalRequestFlags(flags)
}

func marshalRequestFlags(flags any) string {
	data, err := json.Marshal(flags)
	if err != nil {
		return fmt.Sprintf(`{"parse_error":%q}`, err.Error())
	}
	return string(data)
}

// Tuning knobs — exported so they can be adjusted without code changes.
var (
	ReceiptTimeout             = 5 * time.Second
	ParallelAdvantageThreshold = 0.5 // 50% better estimated time
	UnresponsiveThreshold      = 1.0 // any non-responsive history → start secondary
	MinSamplesForDecision      = 3
	LogHeartbeatInterval       = time.Minute
	FirstTokenTimeoutCap       = time.Second
	PerInputTokenFirstTokenLag = 10 * time.Millisecond
	// InterChunkStallTimeout caps how long the crowned winner may go silent
	// between forwarded chunks before we abort the stream as stalled.
	InterChunkStallTimeout   = time.Minute
	NonStreamResponseFloor   = 20 * time.Second
	PerInputTokenResponseLag = 20 * time.Millisecond
	SecondaryWaitAfterWinner = 5 * time.Minute

	// Same-nonce reconnect knobs (R3/R7). AttemptReconnectEnabled is also
	// gated on session protocol ≥ v5 (R0) via attemptReconnectAllowed.
	AttemptReconnectEnabled    = false
	AttemptReconnectBudget     = time.Second
	AttemptReconnectMaxTries   = 2
	AllowStreamResetOnFailover = false
	reconnectTryBackoff        = 50 * time.Millisecond
)

func DefaultRedundancySettings() RedundancySettings {
	return RedundancySettings{
		ReceiptTimeoutMS:              5000,
		FirstTokenTimeoutFloorMS:      1000,
		PerInputTokenFirstTokenLagMS:  10,
		InterChunkStallTimeoutMS:      60000,
		StreamingAttemptHardTimeoutMS: 1800000,
		NonStreamResponseFloorMS:      20000,
		NonStreamNoContentTimeoutMS:   1800000,
		NonStreamMaxAttemptWaitMS:     1800000,
		PerInputTokenResponseLagMS:    20,
		SecondaryWaitAfterWinnerMS:    600000,
		ParallelAdvantageThreshold:    0.5,
		UnresponsiveThreshold:         1.0,
		SpeedPolicy:                   RedundancySpeedPolicyHybrid,
		PairwiseBudgetPercentile:      0.90,
		PairwiseMaxProactiveAttempts:  3,
		PairwiseMinDirectComparisons:  4,
		PairwiseWinnerHoldMS:          500,
		PairwiseWinnerHoldMinSpeedup:  0.10,
		PairwiseWinnerHoldMinSamples:  6,
		AttemptReconnectEnabled:       false,
		AttemptReconnectBudgetMS:      1000,
		AttemptReconnectMaxTries:      2,
		AllowStreamResetOnFailover:    false,
	}
}

func ApplyRedundancySettings(settings RedundancySettings) {
	defaults := DefaultRedundancySettings()
	if settings.ReceiptTimeoutMS <= 0 {
		settings.ReceiptTimeoutMS = defaults.ReceiptTimeoutMS
	}
	if settings.FirstTokenTimeoutFloorMS <= 0 {
		settings.FirstTokenTimeoutFloorMS = defaults.FirstTokenTimeoutFloorMS
	}
	if settings.PerInputTokenFirstTokenLagMS < 0 {
		settings.PerInputTokenFirstTokenLagMS = defaults.PerInputTokenFirstTokenLagMS
	}
	if settings.InterChunkStallTimeoutMS < 0 {
		settings.InterChunkStallTimeoutMS = defaults.InterChunkStallTimeoutMS
	}
	if settings.StreamingAttemptHardTimeoutMS <= 0 {
		settings.StreamingAttemptHardTimeoutMS = defaults.StreamingAttemptHardTimeoutMS
	}
	if settings.NonStreamResponseFloorMS <= 0 {
		settings.NonStreamResponseFloorMS = defaults.NonStreamResponseFloorMS
	}
	if settings.NonStreamNoContentTimeoutMS <= 0 {
		settings.NonStreamNoContentTimeoutMS = defaults.NonStreamNoContentTimeoutMS
	}
	if settings.NonStreamMaxAttemptWaitMS <= 0 {
		settings.NonStreamMaxAttemptWaitMS = defaults.NonStreamMaxAttemptWaitMS
	}
	if settings.PerInputTokenResponseLagMS < 0 {
		settings.PerInputTokenResponseLagMS = defaults.PerInputTokenResponseLagMS
	}
	if settings.SecondaryWaitAfterWinnerMS <= 0 {
		settings.SecondaryWaitAfterWinnerMS = defaults.SecondaryWaitAfterWinnerMS
	}
	if settings.ParallelAdvantageThreshold <= 0 || settings.ParallelAdvantageThreshold >= 1 {
		settings.ParallelAdvantageThreshold = defaults.ParallelAdvantageThreshold
	}
	if settings.UnresponsiveThreshold <= 0 || settings.UnresponsiveThreshold > 1 {
		settings.UnresponsiveThreshold = defaults.UnresponsiveThreshold
	}
	settings.SpeedPolicy = normalizeRedundancySpeedPolicy(settings.SpeedPolicy)
	if settings.SpeedPolicy == "" {
		settings.SpeedPolicy = defaults.SpeedPolicy
	}
	if settings.PairwiseBudgetPercentile <= 0 || settings.PairwiseBudgetPercentile >= 1 {
		settings.PairwiseBudgetPercentile = defaults.PairwiseBudgetPercentile
	}
	if settings.PairwiseMaxProactiveAttempts <= 0 {
		settings.PairwiseMaxProactiveAttempts = defaults.PairwiseMaxProactiveAttempts
	}
	if settings.PairwiseMinDirectComparisons <= 0 {
		settings.PairwiseMinDirectComparisons = defaults.PairwiseMinDirectComparisons
	}
	if settings.PairwiseWinnerHoldMS < 0 {
		settings.PairwiseWinnerHoldMS = defaults.PairwiseWinnerHoldMS
	}
	if settings.PairwiseWinnerHoldMinSpeedup <= 0 || settings.PairwiseWinnerHoldMinSpeedup >= 1 {
		settings.PairwiseWinnerHoldMinSpeedup = defaults.PairwiseWinnerHoldMinSpeedup
	}
	if settings.PairwiseWinnerHoldMinSamples <= 0 {
		settings.PairwiseWinnerHoldMinSamples = defaults.PairwiseWinnerHoldMinSamples
	}
	if settings.AttemptReconnectBudgetMS <= 0 {
		settings.AttemptReconnectBudgetMS = defaults.AttemptReconnectBudgetMS
	}
	if settings.AttemptReconnectMaxTries <= 0 {
		settings.AttemptReconnectMaxTries = defaults.AttemptReconnectMaxTries
	}
	ReceiptTimeout = time.Duration(settings.ReceiptTimeoutMS) * time.Millisecond
	FirstTokenTimeoutCap = time.Duration(settings.FirstTokenTimeoutFloorMS) * time.Millisecond
	PerInputTokenFirstTokenLag = time.Duration(settings.PerInputTokenFirstTokenLagMS) * time.Millisecond
	InterChunkStallTimeout = time.Duration(settings.InterChunkStallTimeoutMS) * time.Millisecond
	StreamingAttemptHardTimeout = time.Duration(settings.StreamingAttemptHardTimeoutMS) * time.Millisecond
	NonStreamResponseFloor = time.Duration(settings.NonStreamResponseFloorMS) * time.Millisecond
	nonStreamingNoContentTimeout = time.Duration(settings.NonStreamNoContentTimeoutMS) * time.Millisecond
	nonStreamingMaxAttemptWait = time.Duration(settings.NonStreamMaxAttemptWaitMS) * time.Millisecond
	PerInputTokenResponseLag = time.Duration(settings.PerInputTokenResponseLagMS) * time.Millisecond
	SecondaryWaitAfterWinner = time.Duration(settings.SecondaryWaitAfterWinnerMS) * time.Millisecond
	ParallelAdvantageThreshold = settings.ParallelAdvantageThreshold
	UnresponsiveThreshold = settings.UnresponsiveThreshold
	RedundancySpeedPolicy = settings.SpeedPolicy
	PairwiseBudgetPercentile = settings.PairwiseBudgetPercentile
	PairwiseMaxProactiveAttempts = settings.PairwiseMaxProactiveAttempts
	PairwiseMinDirectComparisons = settings.PairwiseMinDirectComparisons
	PairwiseWinnerHold = time.Duration(settings.PairwiseWinnerHoldMS) * time.Millisecond
	PairwiseWinnerHoldMinSpeedup = settings.PairwiseWinnerHoldMinSpeedup
	PairwiseWinnerHoldMinSamples = settings.PairwiseWinnerHoldMinSamples
	AttemptReconnectEnabled = settings.AttemptReconnectEnabled
	AttemptReconnectBudget = time.Duration(settings.AttemptReconnectBudgetMS) * time.Millisecond
	AttemptReconnectMaxTries = settings.AttemptReconnectMaxTries
	AllowStreamResetOnFailover = settings.AllowStreamResetOnFailover
}

func normalizeRedundancySpeedPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", RedundancySpeedPolicyHybrid:
		return RedundancySpeedPolicyHybrid
	case RedundancySpeedPolicyLegacy:
		return RedundancySpeedPolicyLegacy
	case RedundancySpeedPolicyPairwise:
		return RedundancySpeedPolicyPairwise
	default:
		return policy
	}
}

var maxSpeculativeAttempts atomic.Int64

func SetMaxSpeculativeAttempts(v int) {
	maxSpeculativeAttempts.Store(int64(v))
}

func CurrentMaxSpeculativeAttempts() int {
	return int(maxSpeculativeAttempts.Load())
}

// Decision describes whether and when to start a parallel secondary inference.
type Decision struct {
	RunSecondary      bool
	Delay             time.Duration // 0 = immediate
	Reason            string
	ImmediateAttempts int
}

// Redundancy runs one request reliably, using extra attempts when needed.
// It sits between Proxy and Session: Proxy delegates request execution here,
// and Redundancy decides whether to use just one nonce or several.
type Redundancy struct {
	session              *user.Session
	perf                 *PerfTracker
	groupSize            int
	devshardID           string
	model                string // escrow's registered model; used for ghost probes when no real request is around
	metrics              *DevshardMetrics
	accounting           *accounting.Recorder
	onEscrowMissing      func() // called (at most once per request) when a host reports escrow not found
	onBalanceExhausted   func() // called (once) when local state hits insufficient balance
	balanceExhaustedOnce sync.Once
	picker               *sessionPicker
	participantLimiter   *ParticipantRequestLimiter
	stateBlockMu         sync.RWMutex
	stateBlockedHosts    map[string]string // escrow-local participant blocks for non-recoverable state divergence

	onRaceCleanupStart func()
	onRaceCleanupDone  func()

	suspiciousParticipant func(participantKey string) bool
}

// ErrAllHostsExcluded is returned by prepareInflight when the request
// has already tried every distinct participant in the escrow. The
// caller (RunInference or startAdditionalInflight) treats it as
// exhaustion: no further attempts are scheduled, existing in-flight
// attempts finish naturally. "Distinct participant" matters when one
// participant occupies multiple group slots -- they are counted once.
var ErrAllHostsExcluded = errors.New("redundancy: request has tried every host in escrow")

// ErrNoAvailableHost is returned by prepareInflight when the picker
// drops the request because every currently-available (non-PoC) host
// is in its exclude set. Distinct from ErrAllHostsExcluded: that one
// fires when the request has already tried every slot in the group;
// this one fires when slots it has not tried are temporarily
// unusable (PoC-required) and the picker chose not to wait.
//
// Treated identically by callers: redundancy stops scheduling more
// attempts, lets existing in-flights finish, and surfaces this error
// to the user only when there is no other attempt to wait on.
var ErrNoAvailableHost = errors.New("redundancy: no currently-available host outside the request's exclude set")

var pairwiseABRandom = rand.Float64

func NewRedundancy(session *user.Session, perf *PerfTracker, groupSize int, model string) *Redundancy {
	return NewRedundancyWithThrottle(session, perf, groupSize, model, nil)
}

// NewRedundancyWithThrottle is the production constructor that wires
// in the reactive-throttle checker so the picker can short-circuit a
// throttled host's next nonce as a no-send ghost probe (see
// session_picker.go branch 1b). Tests that don't care about throttle
// behavior can use NewRedundancy and the picker treats every host as
// non-throttled (everything flows through real dispatch + the
// transport-layer admission gate as before).
func NewRedundancyWithThrottle(session *user.Session, perf *PerfTracker, groupSize int, model string, throttleBlocked func(participantKey string) bool) *Redundancy {
	e := &Redundancy{
		session:   session,
		perf:      perf,
		groupSize: groupSize,
		model:     model,
	}
	e.picker = newSessionPicker(session, model, e.runGhostProbe, throttleBlocked, e.capabilityBlocked)
	e.picker.start()
	return e
}

// Stop terminates the dispatcher goroutine. Production callers do not
// invoke this (process lifetime). Tests should defer it for clean
// teardown.
func (e *Redundancy) Stop() {
	if e == nil || e.picker == nil {
		return
	}
	e.picker.stop()
}

func (e *Redundancy) Decide(primaryHostIdx int, inputTokens uint64) Decision {
	secondaryHostIdx := (primaryHostIdx + 1) % e.groupSize
	primaryParticipant := e.participantKeyForHost(primaryHostIdx)
	secondaryParticipant := e.participantKeyForHost(secondaryHostIdx)

	// Rule 1: primary is known unresponsive → immediate parallel
	if e.perf.IsUnresponsiveParticipant(primaryParticipant) {
		return Decision{RunSecondary: true, Delay: 0, Reason: "primary_unresponsive", ImmediateAttempts: 1}
	}

	// Reconnect blips are recorded for observability only (R8). They must not
	// change routing: forcing an immediate secondary would buy a second full
	// generation and a second MsgStartInference for a transient TCP drop.

	if RedundancySpeedPolicy != RedundancySpeedPolicyLegacy {
		if decision, ok := e.decidePairwiseSpeedup(primaryHostIdx, inputTokens); ok {
			return decision
		}
		if RedundancySpeedPolicy == RedundancySpeedPolicyPairwise {
			return Decision{RunSecondary: true, Delay: receiptTimeoutForInput(inputTokens), Reason: "pairwise_insufficient_data"}
		}
	}

	return e.decideLegacySecondaryFaster(primaryParticipant, secondaryParticipant, inputTokens)
}

func (e *Redundancy) decidePairwiseSpeedup(primaryHostIdx int, inputTokens uint64) (Decision, bool) {
	if e == nil || e.perf == nil || e.perf.pairwise == nil || e.groupSize <= 1 {
		return Decision{}, false
	}
	primary := e.participantKeyForHost(primaryHostIdx)
	if !e.pairwiseParticipantAvailable(primary) {
		return Decision{}, false
	}
	candidates := e.pairwiseCandidateParticipants(primaryHostIdx, primary)
	if len(candidates) == 0 {
		return Decision{}, false
	}
	cutoff, cutoffOK := e.perf.pairwise.SpeedupCutoffForParticipants(e.model, inputTokens, e.pairwiseParticipantAvailable)
	bestCost := 1.0
	accepted := 0
	acceptedPath := []string{}
	deterministicAccepted := false
	if cutoffOK && cutoff > 0 {
		for idx, candidate := range candidates {
			if accepted >= PairwiseMaxProactiveAttempts {
				break
			}
			step := idx + 1
			ratio, confidence, ok := e.perf.pairwise.EstimateRatio(e.model, inputTokens, primary, candidate, acceptedPath)
			if !ok || ratio <= 1 {
				continue
			}
			candidateCost := 1 / ratio
			var score float64
			if accepted == 0 && step > 1 {
				score = ((bestCost - candidateCost) / bestCost) * confidence / float64(step)
			} else {
				score = ((bestCost - candidateCost) / bestCost) * confidence
			}
			if score < cutoff {
				if accepted == 0 {
					acceptedPath = append(acceptedPath, candidate)
				}
				continue
			}
			if accepted == 0 && step > 1 {
				accepted = step
			} else {
				accepted++
			}
			deterministicAccepted = true
			if candidateCost < bestCost {
				bestCost = candidateCost
			}
			acceptedPath = append(acceptedPath, candidate)
		}
	}
	sampledAttempts := e.pairwiseABAdditionalAttempts(primary, candidates, accepted, inputTokens)
	if sampledAttempts > 0 {
		accepted += sampledAttempts
	}
	if accepted <= 0 {
		return Decision{}, false
	}
	if accepted > PairwiseMaxProactiveAttempts {
		accepted = PairwiseMaxProactiveAttempts
	}
	reason := "pairwise_budgeted_speedup"
	if sampledAttempts > 0 && !deterministicAccepted {
		reason = "pairwise_ab_sample"
	}
	return Decision{
		RunSecondary:      true,
		Delay:             0,
		Reason:            reason,
		ImmediateAttempts: accepted,
	}, true
}

func (e *Redundancy) pairwiseCandidateParticipants(primaryHostIdx int, primary string) []string {
	seen := map[string]bool{primary: true}
	candidates := make([]string, 0, e.groupSize-1)
	for step := 1; step <= e.groupSize-1; step++ {
		candidateIdx := (primaryHostIdx + step) % e.groupSize
		candidate := e.participantKeyForHost(candidateIdx)
		if candidate == "" || seen[candidate] || !e.pairwiseParticipantAvailable(candidate) {
			continue
		}
		seen[candidate] = true
		candidates = append(candidates, candidate)
	}
	return candidates
}

func (e *Redundancy) pairwiseABAdditionalAttempts(primary string, candidates []string, accepted int, inputTokens uint64) int {
	if accepted < 0 || accepted >= PairwiseMaxProactiveAttempts || accepted >= len(candidates) {
		return 0
	}
	additional := 0
	currentAccepted := accepted

	if currentAccepted == 0 && e.perf.pairwise.DirectSampleCount(e.model, inputTokens, primary, candidates[0]) < PairwiseMinDirectComparisons {
		additional++
		currentAccepted++
	}

	if currentAccepted == 1 && currentAccepted < len(candidates) && currentAccepted < PairwiseMaxProactiveAttempts {
		failedB := candidates[0]
		c := candidates[1]
		if e.perf.pairwise.NeedsFailedComparisonFollowUp(e.model, inputTokens, primary, failedB, c) {
			additional++
			currentAccepted++
		}
	}

	if currentAccepted >= PairwiseMaxProactiveAttempts || currentAccepted >= len(candidates) {
		return additional
	}

	latest := primary
	if currentAccepted > 0 {
		latest = candidates[currentAccepted-1]
	}
	next := candidates[currentAccepted]
	samples := e.perf.pairwise.DirectSampleCount(e.model, inputTokens, latest, next)
	rate := PairwiseABSampleRate
	if samples < PairwiseABSparseSampleThreshold {
		rate = PairwiseABSparseSampleRate
	}
	if rate <= 0 {
		return additional
	}
	if rate >= 1 {
		return additional + 1
	}
	if pairwiseABRandom() < rate {
		return additional + 1
	}
	return additional
}

func (e *Redundancy) pairwiseParticipantAvailable(participantKey string) bool {
	if participantKey == "" {
		return false
	}
	if shouldUseProbeForParticipant(e.model, participantKey) {
		return false
	}
	if e != nil {
		if e.perf != nil && e.perf.IsUnresponsiveParticipant(participantKey) {
			return false
		}
		if e.participantLimiter != nil && (e.participantLimiter.IsBlockedForModel(participantKey, e.model) || e.participantLimiter.IsRecentlyQuarantinedForModel(participantKey, e.model)) {
			return false
		}
	}
	return true
}

// Deprecated fallback: retained while pairwise routing warms up. Remove after
// pairwise speed policy has enough production coverage and legacy fallback has
// been disabled for one release.
func (e *Redundancy) decideLegacySecondaryFaster(primaryParticipant, secondaryParticipant string, inputTokens uint64) Decision {
	primaryEst := e.perf.EstimatedTimeMsForParticipant(primaryParticipant, inputTokens)
	secondaryEst := e.perf.EstimatedTimeMsForParticipant(secondaryParticipant, inputTokens)
	if primaryEst > 0 && secondaryEst > 0 && secondaryEst < primaryEst*(1-ParallelAdvantageThreshold) {
		return Decision{RunSecondary: true, Delay: 0, Reason: "secondary_faster", ImmediateAttempts: 1}
	}

	// Rule 3: default — start secondary after the request-sized receipt timeout.
	return Decision{RunSecondary: true, Delay: receiptTimeoutForInput(inputTokens), Reason: "receipt_timeout"}
}

func (e *Redundancy) participantKeyForHost(hostIdx int) string {
	if e != nil && e.session != nil {
		if key := e.session.HostParticipantKey(hostIdx); key != "" {
			return key
		}
	}
	return legacyHostPerfKey(hostIdx)
}

// inflight tracks one in-flight inference and its timing.
type inflight struct {
	prepared                   *user.PreparedInference
	hostIdx                    int
	hostID                     string
	nonce                      uint64
	escrowID                   string
	sendTime                   time.Time
	escalated                  bool
	probe                      bool
	excludePairwise            bool
	startedBeforePoCGeneration bool
	phaseTransitionAborted     bool

	suspicious bool

	noWinnerReason         string
	noWinnerQuarantineMode string
	noWinnerFailureStrikes int

	role        string
	startReason string

	receiptOnce     sync.Once
	receiptTimeNano atomic.Int64  // unix nano; 0 means not received
	receiptCh       chan struct{} // closed when receipt arrives

	tokenOnce       sync.Once
	firstTokenNano  atomic.Int64 // unix nano; 0 means no content yet
	firstTokenCh    chan struct{}
	outputChunks    atomic.Int64
	contentChunks   atomic.Int64
	outputBytes     atomic.Int64
	lastChunkAt     atomic.Int64
	stallMu         sync.Mutex
	stallActive     bool
	stalls          []attemptStall
	forwardedLog    sync.Once
	suppressedLog   sync.Once
	ctxCancelledLog sync.Once
	hardTimeoutLog  sync.Once
	sampleOnce      sync.Once
	processOnce     sync.Once
	processErr      error

	// pendingBuf holds bytes received before any content event was observed.
	// Each attempt has at most one writer goroutine driving Write/Flush, so no
	// mutex is required. The buffer is flushed in order to the race group
	// writer when this attempt becomes the winner; it is discarded if a
	// different attempt wins or the attempt ends with no content.
	pendingBuf []byte

	// deliveredEvents / deliveredPartial track the upstream SSE prefix actually
	// forwarded to the client (race.w). Used by same-nonce reconnect (R2/R6).
	// Counted only on successful client writes — not pendingBuf, probes, or losers.
	// Blank SSE separators (empty lines from "\n\n" framing) are not events.
	// deliveredPartial is bytes of the currently incomplete event (0 on a
	// newline boundary). Updated on the attempt's writer goroutine only.
	deliveredEvents       int64
	deliveredPartial      int64
	deliveredForming      []byte // incomplete line for delivered-prefix accounting
	deliveredFirstEventFP uint64 // fnv of first delivered content line (R2 probe)

	// classifyPartial keeps the tail after the last '\n' from prior Writes;
	// used to reassemble fragmented SSE events for the classifier.
	classifyPartial []byte
	classifyCapLog  sync.Once
	// participantClassifyBytes is the shared per-participant byte counter this
	// attempt contributes to; resolved at creation. Nil disables the cap.
	participantClassifyBytes *atomic.Int64

	usageComplTokens atomic.Int64

	suspiciousWinnerDeferredLog sync.Once

	// shortContentResponseBody optionally preserves raw streamed bytes so
	// anomalously low-content attempts can be inspected offline. It is only
	// populated when DEVSHARD_CAPTURE_SHORT_CONTENT_RESPONSES is enabled.
	shortContentResponseBody          []byte
	shortContentResponseBodyTruncated bool

	// contentSource labels the field that produced the first content event
	// ("delta.content", "delta.reasoning_content", "delta.tool_calls", or the
	// streaming-only convertible shape "message.content"). Set exactly once
	// when sseChunkContentSource* first returns true. Empty string means no
	// accepted content was ever observed.
	contentSource string

	// errorSource labels the first OpenAI-style SSE error event observed. Such
	// attempts are valid terminal responses, not empty streams for participant
	// quarantine. Keep a small copy for later logging because winner bytes are
	// forwarded immediately and pendingBuf is cleared.
	errorSource     string
	errorCode       string
	errorType       string
	errorMessage    string
	errorBodySample []byte

	// emptyResponseBodySample preserves the pre-content bytes for file-based
	// capture after pendingBuf is discarded to avoid accidental forwarding.
	emptyResponseBodySample          string
	emptyResponseBodySampleTruncated bool

	resp *host.HostResponse
	err  error
	done chan struct{}

	// reconnectTries counts same-nonce transport retries (R8). Distinct from
	// attempt accounting — reconnect must not call RealSend again.
	reconnectTries atomic.Int64
	// reconnectLadderUsed ensures the post-content reconnect ladder runs at
	// most once per attempt (further drops after a resume fall through to
	// today's failure / R5 path).
	reconnectLadderUsed  atomic.Bool
	reconnectPenaltyOnce sync.Once
	// resuming is set while a same-nonce reconnect ladder owns this attempt.
	// done is already closed by then, so inflightDone consults this flag to
	// keep the attempt "pending" for the race loop. Never mutate done itself:
	// monitorInflight and the background finalizers read it concurrently.
	// resumeDone is closed when the ladder goroutine exits; finalizers wait on
	// it via waitInflightSettled so resp/err are stable before ProcessResponse.
	resuming   atomic.Bool
	resumeMu   sync.Mutex
	resumeDone chan struct{}

	// cancel unwinds the per-attempt context used by SendOnly. The background
	// finalizer invokes it on losers that are still running SecondaryWaitAfterWinner
	// after the winner has settled, so their transport goroutines return
	// promptly and HandleTimeout can run against the abandoned nonce.
	// Reconnect replaces it mid-flight, so all access goes through
	// setCancel / cancelAttempt.
	cancelMu sync.Mutex
	cancel   context.CancelFunc
}

func (inf *inflight) setCancel(fn context.CancelFunc) {
	inf.cancelMu.Lock()
	inf.cancel = fn
	inf.cancelMu.Unlock()
}

// cancelAttempt unwinds the attempt's current context, if any.
func (inf *inflight) cancelAttempt() {
	if inf == nil {
		return
	}
	inf.cancelMu.Lock()
	fn := inf.cancel
	inf.cancelMu.Unlock()
	if fn != nil {
		fn()
	}
}

func (inf *inflight) receiptAt() time.Time {
	if n := inf.receiptTimeNano.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

func (inf *inflight) hasReceipt() bool {
	return inf.receiptTimeNano.Load() != 0
}

func (inf *inflight) setReceiptAt(t time.Time) {
	if t.IsZero() {
		inf.receiptTimeNano.Store(0)
		return
	}
	inf.receiptTimeNano.Store(t.UnixNano())
}

func (inf *inflight) firstTokenAt() time.Time {
	if n := inf.firstTokenNano.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

func (inf *inflight) hasFirstToken() bool {
	return inf.firstTokenNano.Load() != 0
}

func (inf *inflight) setFirstTokenAt(t time.Time) {
	if t.IsZero() {
		inf.firstTokenNano.Store(0)
		return
	}
	inf.firstTokenNano.Store(t.UnixNano())
}

func (inf *inflight) captureShortContentResponseChunk(p []byte) {
	if inf == nil || len(p) == 0 {
		return
	}
	opts := currentRequestCaptureOptions()
	if !opts.shortContentAttempts || !opts.shortContentResponses || opts.shortContentMaxResponse <= 0 || inf.shortContentResponseBodyTruncated {
		return
	}
	remaining := int(opts.shortContentMaxResponse) - len(inf.shortContentResponseBody)
	if remaining <= 0 {
		inf.shortContentResponseBodyTruncated = true
		return
	}
	if len(p) > remaining {
		inf.shortContentResponseBody = append(inf.shortContentResponseBody, p[:remaining]...)
		inf.shortContentResponseBodyTruncated = true
		return
	}
	inf.shortContentResponseBody = append(inf.shortContentResponseBody, p...)
}

type attemptStall struct {
	StartTime           time.Time
	DetectedTime        time.Time
	EndTime             time.Time
	OutputChunksBefore  int64
	ContentChunksBefore int64
	OutputBytesBefore   int64
}

type attemptStallLog struct {
	StartMS             int64 `json:"start_ms"`
	DetectedMS          int64 `json:"detected_ms"`
	EndMS               int64 `json:"end_ms"`
	DurationMS          int64 `json:"duration_ms"`
	OutputChunksBefore  int64 `json:"output_chunks_before"`
	ContentChunksBefore int64 `json:"content_chunks_before"`
	OutputBytesBefore   int64 `json:"output_bytes_before"`
	OutputChunksAfter   int64 `json:"output_chunks_after"`
	ContentChunksAfter  int64 `json:"content_chunks_after"`
	OutputBytesAfter    int64 `json:"output_bytes_after"`
}

func (inf *inflight) finishActiveStall(now time.Time) {
	if inf == nil {
		return
	}
	inf.stallMu.Lock()
	defer inf.stallMu.Unlock()
	inf.finishActiveStallLocked(now)
}

func (inf *inflight) finishActiveStallLocked(now time.Time) {
	if !inf.stallActive || len(inf.stalls) == 0 {
		return
	}
	inf.stallActive = false
	idx := len(inf.stalls) - 1
	if inf.stalls[idx].EndTime.IsZero() {
		inf.stalls[idx].EndTime = now
	}
}

func (inf *inflight) startInterChunkStall(now time.Time) (attemptStall, bool) {
	if inf == nil || inf.probe || inflightDone(inf) || InterChunkStallLogThreshold <= 0 {
		return attemptStall{}, false
	}
	lastChunkAt := inf.lastChunkAt.Load()
	if lastChunkAt <= 0 {
		return attemptStall{}, false
	}
	start := time.Unix(0, lastChunkAt)
	if now.Sub(start) < InterChunkStallLogThreshold {
		return attemptStall{}, false
	}
	if inf.contentChunks.Load() == 0 {
		return attemptStall{}, false
	}

	inf.stallMu.Lock()
	defer inf.stallMu.Unlock()
	if inf.stallActive {
		return attemptStall{}, false
	}
	rec := attemptStall{
		StartTime:           start,
		DetectedTime:        now,
		OutputChunksBefore:  inf.outputChunks.Load(),
		ContentChunksBefore: inf.contentChunks.Load(),
		OutputBytesBefore:   inf.outputBytes.Load(),
	}
	inf.stalls = append(inf.stalls, rec)
	inf.stallActive = true
	return rec, true
}

func (inf *inflight) hasRecordedStall() bool {
	if inf == nil {
		return false
	}
	inf.stallMu.Lock()
	defer inf.stallMu.Unlock()
	return len(inf.stalls) > 0
}

func (inf *inflight) stallLogFields(now time.Time) []any {
	if inf == nil {
		return nil
	}
	inf.finishActiveStall(now)

	inf.stallMu.Lock()
	stalls := append([]attemptStall(nil), inf.stalls...)
	inf.stallMu.Unlock()
	if len(stalls) == 0 {
		return nil
	}

	finalOutputChunks := inf.outputChunks.Load()
	finalContentChunks := inf.contentChunks.Load()
	finalOutputBytes := inf.outputBytes.Load()
	summary := make([]attemptStallLog, 0, len(stalls))
	for i, stall := range stalls {
		end := stall.EndTime
		if end.IsZero() {
			end = now
		}
		nextOutputChunks := finalOutputChunks
		nextContentChunks := finalContentChunks
		nextOutputBytes := finalOutputBytes
		if i+1 < len(stalls) {
			nextOutputChunks = stalls[i+1].OutputChunksBefore
			nextContentChunks = stalls[i+1].ContentChunksBefore
			nextOutputBytes = stalls[i+1].OutputBytesBefore
		}
		summary = append(summary, attemptStallLog{
			StartMS:             stall.StartTime.UnixMilli(),
			DetectedMS:          stall.DetectedTime.UnixMilli(),
			EndMS:               end.UnixMilli(),
			DurationMS:          end.Sub(stall.StartTime).Milliseconds(),
			OutputChunksBefore:  stall.OutputChunksBefore,
			ContentChunksBefore: stall.ContentChunksBefore,
			OutputBytesBefore:   stall.OutputBytesBefore,
			OutputChunksAfter:   nextOutputChunks - stall.OutputChunksBefore,
			ContentChunksAfter:  nextContentChunks - stall.ContentChunksBefore,
			OutputBytesAfter:    nextOutputBytes - stall.OutputBytesBefore,
		})
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return []any{"stall_count", len(stalls), "stall_summary_error", err}
	}
	return []any{"stall_count", len(stalls), "stalls", string(encoded)}
}

// raceGroup arbitrates which inflight's stream is forwarded to the client.
type raceGroup struct {
	mu             sync.Mutex
	clientMu       sync.Mutex
	winner         uint64 // 0 = undecided
	winnerCh       chan struct{}
	w              io.Writer
	holdCandidates map[uint64]*inflight
	decided        atomic.Bool
	clientDetached atomic.Bool
	logCtx         context.Context
	writeCtx       context.Context
	escrow         string

	// reservedWinner holds the client stream for a resumable crowned attempt
	// during the same-nonce reconnect ladder (R4). While set, setWinner refuses
	// any other nonce so a hedge secondary cannot splice a different generation
	// onto an already-delivered prefix. Cleared explicitly when the ladder ends
	// (not on a wall-clock expiry — the hedge may still be racing after budget).
	reservedWinner uint64
}

func newRaceGroup(logCtx, writeCtx context.Context, escrow string, w io.Writer) *raceGroup {
	return &raceGroup{
		winnerCh: make(chan struct{}),
		logCtx:   logCtx,
		writeCtx: writeCtx,
		escrow:   escrow,
		w:        w,
	}
}

func (rg *raceGroup) setWinner(nonce uint64) {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	if rg.reservedWinner != 0 && nonce != rg.reservedWinner {
		logInferenceStage(rg.logCtx, rg.escrow, nonce, "winner_blocked_by_reservation",
			"reserved_winner", rg.reservedWinner,
		)
		return
	}
	if rg.winner == 0 {
		rg.winner = nonce
		rg.decided.Store(true)
		close(rg.winnerCh)
		logInferenceStage(rg.logCtx, rg.escrow, nonce, "winner_selected")
	}
}

func (rg *raceGroup) reserveWinner(nonce uint64) {
	if rg == nil || nonce == 0 {
		return
	}
	rg.mu.Lock()
	defer rg.mu.Unlock()
	rg.reservedWinner = nonce
	logInferenceStage(rg.logCtx, rg.escrow, nonce, "winner_reserved")
}

func (rg *raceGroup) clearReservation() {
	if rg == nil {
		return
	}
	rg.mu.Lock()
	defer rg.mu.Unlock()
	if rg.reservedWinner == 0 {
		return
	}
	nonce := rg.reservedWinner
	rg.reservedWinner = 0
	logInferenceStage(rg.logCtx, rg.escrow, nonce, "winner_reservation_cleared")
}

func (rg *raceGroup) hasReservation() bool {
	if rg == nil {
		return false
	}
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return rg.reservedWinner != 0
}

func (rg *raceGroup) reservedWinnerNonce() uint64 {
	if rg == nil {
		return 0
	}
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return rg.reservedWinner
}

func (rg *raceGroup) promoteFallbackWinner(inf *inflight) error {
	if rg == nil || inf == nil {
		return nil
	}
	rg.setWinner(inf.nonce)
	rg.clientMu.Lock()
	defer rg.clientMu.Unlock()
	if rg.isClientDetached() || len(inf.pendingBuf) == 0 || rg.w == nil {
		inf.pendingBuf = nil
		return nil
	}
	if rg.writeCtx != nil {
		if err := rg.writeCtx.Err(); err != nil {
			inf.pendingBuf = nil
			return err
		}
	}
	if _, err := rg.w.Write(inf.pendingBuf); err != nil {
		inf.pendingBuf = nil
		return err
	}
	inf.recordDeliveredForward(inf.pendingBuf)
	inf.pendingBuf = nil
	return nil
}

// recordDeliveredForward updates deliveredEvents/deliveredPartial for bytes
// successfully written to the client. Non-empty newline-terminated lines count
// as complete upstream events; blank separator lines are skipped; a trailing
// fragment without '\n' is tracked in deliveredPartial.
func (inf *inflight) recordDeliveredForward(p []byte) {
	if len(p) == 0 {
		return
	}
	inf.deliveredForming = append(inf.deliveredForming, p...)
	for {
		nl := bytes.IndexByte(inf.deliveredForming, '\n')
		if nl == -1 {
			inf.deliveredPartial = int64(len(inf.deliveredForming))
			return
		}
		line := inf.deliveredForming[:nl]
		inf.deliveredForming = inf.deliveredForming[nl+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if inf.deliveredEvents == 0 {
			inf.deliveredFirstEventFP = sseEventFingerprint(line)
		}
		inf.deliveredEvents++
		inf.deliveredPartial = 0
	}
}

func (rg *raceGroup) addWinnerHoldCandidate(inf *inflight) {
	if rg == nil || inf == nil || PairwiseWinnerHold <= 0 {
		return
	}
	rg.mu.Lock()
	defer rg.mu.Unlock()
	if rg.holdCandidates == nil {
		rg.holdCandidates = make(map[uint64]*inflight)
	}
	rg.holdCandidates[inf.nonce] = inf
}

func (rg *raceGroup) maybeHoldWinnerCandidate(candidate *inflight) {
	if rg == nil || candidate == nil || PairwiseWinnerHold <= 0 {
		return
	}
	rg.mu.Lock()
	if rg.winner != 0 {
		rg.mu.Unlock()
		return
	}
	holdUntil := time.Now().Add(PairwiseWinnerHold)
	preferred := make([]*inflight, 0, len(rg.holdCandidates))
	for nonce, inf := range rg.holdCandidates {
		if nonce == candidate.nonce || inf == nil || inflightDone(inf) {
			continue
		}
		preferred = append(preferred, inf)
	}
	rg.mu.Unlock()
	if len(preferred) == 0 {
		return
	}
	logInferenceStage(rg.logCtx, candidate.escrowID, candidate.nonce, "pairwise_winner_hold_started",
		"host", candidate.hostID,
		"preferred_attempts", len(preferred),
		"hold_ms", PairwiseWinnerHold.Milliseconds(),
	)
	timer := time.NewTimer(time.Until(holdUntil))
	defer timer.Stop()
	for {
		rg.mu.Lock()
		winner := rg.winner
		rg.mu.Unlock()
		if winner != 0 {
			logInferenceStage(rg.logCtx, candidate.escrowID, candidate.nonce, "pairwise_winner_hold_resolved",
				"host", candidate.hostID,
				"winner_nonce", winner,
			)
			return
		}
		if allInflightsDone(preferred) {
			logInferenceStage(rg.logCtx, candidate.escrowID, candidate.nonce, "pairwise_winner_hold_no_preferred",
				"host", candidate.hostID,
			)
			return
		}
		select {
		case <-rg.winnerCh:
			logInferenceStage(rg.logCtx, candidate.escrowID, candidate.nonce, "pairwise_winner_hold_resolved",
				"host", candidate.hostID,
				"winner_nonce", rg.winnerNonce(),
			)
			return
		case <-timer.C:
			logInferenceStage(rg.logCtx, candidate.escrowID, candidate.nonce, "pairwise_winner_hold_expired",
				"host", candidate.hostID,
			)
			return
		case <-candidate.done:
			return
		}
	}
}

func (rg *raceGroup) hasDecided() bool {
	return rg.decided.Load()
}

func (rg *raceGroup) winnerNonce() uint64 {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return rg.winner
}

func (rg *raceGroup) winnerSignal() <-chan struct{} {
	if rg == nil {
		return nil
	}
	return rg.winnerCh
}

func (rg *raceGroup) detachClient() {
	if rg != nil {
		rg.clientMu.Lock()
		defer rg.clientMu.Unlock()
		rg.clientDetached.Store(true)
	}
}

func (rg *raceGroup) isClientDetached() bool {
	return rg != nil && rg.clientDetached.Load()
}

const (
	defaultMaxClassifyPartial            = 1 << 20   // 1 MiB per attempt
	defaultMaxClassifyPartialParticipant = 10 << 20  // 10 MiB per participant
	defaultMaxClassifyPartialGlobal      = 100 << 20 // 100 MiB process-wide
)

// Reassembly-buffer caps, tunable at startup via configureClassifyCapsFromEnv.
// These are machine-scale memory knobs, not governance policy.
var (
	maxClassifyPartial                  = defaultMaxClassifyPartial
	maxClassifyPartialParticipant int64 = defaultMaxClassifyPartialParticipant
	maxClassifyPartialGlobal      int64 = defaultMaxClassifyPartialGlobal
)

// configureClassifyCapsFromEnv overrides the reassembly caps from the
// environment; unset variables keep the conservative defaults.
func configureClassifyCapsFromEnv() {
	maxClassifyPartial = int(readInt64Env("GATEWAY_CLASSIFY_MAX_ATTEMPT_BYTES", int64(defaultMaxClassifyPartial)))
	maxClassifyPartialParticipant = readInt64Env("GATEWAY_CLASSIFY_MAX_PARTICIPANT_BYTES", defaultMaxClassifyPartialParticipant)
	maxClassifyPartialGlobal = readInt64Env("GATEWAY_CLASSIFY_MAX_GLOBAL_BYTES", defaultMaxClassifyPartialGlobal)
}

// classifyPartialBytes is the live total of every inflight's classifyPartial.
var classifyPartialBytes atomic.Int64

// participantClassify bounds live reassembly bytes per participant so one
// hostile participant can't exhaust the shared global pool and starve others.
var participantClassify = &participantClassifyTracker{counters: map[string]*atomic.Int64{}}

type participantClassifyTracker struct {
	mu       sync.Mutex
	counters map[string]*atomic.Int64
}

// counterFor returns the participant's byte counter, creating it on first use.
// Entries are never removed; the participant set is the bounded validator set.
func (t *participantClassifyTracker) counterFor(participantKey string) *atomic.Int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	counter := t.counters[participantKey]
	if counter == nil {
		counter = &atomic.Int64{}
		t.counters[participantKey] = counter
	}
	return counter
}

// takeParseable returns the prefix of (classifyPartial + p) ending at the last
// '\n'; the trailing fragment is stashed for the next call.
func (rw *raceWriter) takeParseable(p []byte) []byte {
	lastNL := bytes.LastIndexByte(p, '\n')
	if lastNL == -1 {
		if rw.growClassify(p) {
			return nil
		}
		// Cap hit: classify the raw chunk best-effort (incomplete fragments won't parse).
		rw.dropClassify()
		return p
	}

	var parseable []byte
	if len(rw.inf.classifyPartial) == 0 {
		parseable = p[:lastNL+1]
	} else {
		buf := make([]byte, 0, len(rw.inf.classifyPartial)+lastNL+1)
		buf = append(buf, rw.inf.classifyPartial...)
		buf = append(buf, p[:lastNL+1]...)
		parseable = buf
	}
	if !rw.replaceClassify(p[lastNL+1:]) {
		rw.dropClassify()
	}
	return parseable
}

// classifyParseable records the first content/non-retriable-error signal for
// this attempt, returning whether the buffer carried either.
func (rw *raceWriter) classifyParseable(parseable []byte) (hasContent, hasError bool) {
	if len(parseable) == 0 {
		return false, false
	}
	if src, ok := sseChunkContentSource(parseable); ok {
		hasContent = true
		if rw.inf.contentSource == "" {
			rw.inf.contentSource = src
		}
	} else if details, ok := sseChunkErrorDetails(parseable); ok {
		src := "error"
		if details.Type != "" {
			src = "error." + details.Type
		}
		hasError = !isRetriableCapabilityErrorMessage(details.Message)
		if rw.inf.errorSource == "" {
			rw.inf.errorSource = src
			rw.inf.errorCode = details.Code
			rw.inf.errorType = details.Type
			rw.inf.errorMessage = details.Message
			rw.inf.errorBodySample = append(rw.inf.errorBodySample, parseable...)
		}
	}
	return hasContent, hasError
}

// flushClassify classifies a newline-less final SSE event left in classifyPartial
// once the stream ends, so a truncated tail isn't misread as empty_stream.
func (rw *raceWriter) flushClassify() {
	if rw.inf.probe || len(rw.inf.classifyPartial) == 0 {
		return
	}
	// Extract usage from the final newline-less event too, mirroring Write, so a
	// truncated usage chunk still feeds isModelBurnEmpty.
	if tokens, ok := sseChunkUsageCompletionTokens(rw.inf.classifyPartial); ok {
		rw.inf.usageComplTokens.Store(tokens)
	}
	hasContent, hasError := rw.classifyParseable(rw.inf.classifyPartial)
	rw.dropClassify()
	if hasContent || hasError {
		rw.inf.contentChunks.Add(1)
	}
}

// flushClassifyAndCheckEmpty flushes a buffered newline-less final event before deciding empty, so a truncated final content/error event isn't misread as empty_stream. Call on the send goroutine after SendOnly returns.
func (rw *raceWriter) flushClassifyAndCheckEmpty() bool {
	rw.flushClassify()
	return isEmptyStreamAttempt(rw.inf)
}

// logClassifyDrop reports the first reassembly-buffer drop for this attempt.
// Capped at once per attempt so a persistently newline-less or oversized
// transport can't spam the log.
func (rw *raceWriter) logClassifyDrop(reason string) {
	rw.inf.classifyCapLog.Do(func() {
		ctx := context.Background()
		if rw.group != nil {
			ctx = rw.group.logCtx
		}
		logInferenceStage(ctx, rw.inf.escrowID, rw.nonce, "classify_buffer_dropped",
			"host", rw.inf.hostID,
			"reason", reason,
			"buffered", len(rw.inf.classifyPartial),
			"global_bytes", classifyPartialBytes.Load(),
		)
	})
}

// growClassify appends tail to classifyPartial if all caps still fit.
func (rw *raceWriter) growClassify(tail []byte) bool {
	if len(rw.inf.classifyPartial)+len(tail) > maxClassifyPartial {
		rw.logClassifyDrop("per_attempt_cap")
		return false
	}
	if reason := rw.inf.reserveClassifyBytes(int64(len(tail))); reason != "" {
		rw.logClassifyDrop(reason)
		return false
	}
	rw.inf.classifyPartial = append(rw.inf.classifyPartial, tail...)
	return true
}

// replaceClassify swaps classifyPartial for buf, adjusting byte accounting.
func (rw *raceWriter) replaceClassify(buf []byte) bool {
	if len(buf) > maxClassifyPartial {
		rw.logClassifyDrop("per_attempt_cap")
		return false
	}
	delta := int64(len(buf) - len(rw.inf.classifyPartial))
	if delta > 0 {
		if reason := rw.inf.reserveClassifyBytes(delta); reason != "" {
			rw.logClassifyDrop(reason)
			return false
		}
	} else if delta < 0 {
		rw.inf.adjustClassifyBytes(delta)
	}
	rw.inf.classifyPartial = append(rw.inf.classifyPartial[:0], buf...)
	return true
}

// dropClassify releases the per-attempt buffer and its accounted bytes.
func (rw *raceWriter) dropClassify() {
	rw.inf.releaseClassifyPartial()
}

// reserveClassifyBytes charges n bytes against the participant then global cap,
// rolling back on the first cap hit. Returns the drop reason, "" on success.
func (inf *inflight) reserveClassifyBytes(n int64) string {
	participant := inf.participantClassifyBytes
	if participant != nil && participant.Add(n) > maxClassifyPartialParticipant {
		participant.Add(-n)
		return "participant_cap"
	}
	if classifyPartialBytes.Add(n) > maxClassifyPartialGlobal {
		classifyPartialBytes.Add(-n)
		if participant != nil {
			participant.Add(-n)
		}
		return "global_cap"
	}
	return ""
}

// adjustClassifyBytes adds delta to the global and per-participant counters.
func (inf *inflight) adjustClassifyBytes(delta int64) {
	classifyPartialBytes.Add(delta)
	if inf.participantClassifyBytes != nil {
		inf.participantClassifyBytes.Add(delta)
	}
}

// releaseClassifyPartial frees the reassembly buffer's backing array and
// decrements the counters. Nils the slice so cap doesn't outlive the gauge.
func (inf *inflight) releaseClassifyPartial() {
	if n := len(inf.classifyPartial); n > 0 {
		inf.adjustClassifyBytes(-int64(n))
	}
	inf.classifyPartial = nil
}

// raceWriter is an io.Writer that only forwards writes from the winning nonce.
type raceWriter struct {
	group *raceGroup
	nonce uint64
	inf   *inflight
}

func (rw *raceWriter) ctxErr() error {
	if rw.group == nil || rw.group.writeCtx == nil {
		return nil
	}
	return rw.group.writeCtx.Err()
}

func (rw *raceWriter) Write(p []byte) (int, error) {
	now := time.Now()
	rw.inf.finishActiveStall(now)
	firstOutputChunk := false
	rw.inf.tokenOnce.Do(func() {
		firstOutputChunk = true
		rw.inf.setFirstTokenAt(now)
		if rw.inf.firstTokenCh != nil {
			close(rw.inf.firstTokenCh)
		}
	})
	rw.inf.outputChunks.Add(1)
	rw.inf.outputBytes.Add(int64(len(p)))
	rw.inf.lastChunkAt.Store(now.UnixNano())
	rw.inf.captureShortContentResponseChunk(p)

	// Detect whether this Write contains the first content-bearing event for
	// this attempt. Only content events promote a nonce to winner; role-only
	// chunks and [DONE] markers do not. Probes never produce winner content.
	hadContentBefore := rw.inf.contentChunks.Load() > 0
	var chunkHasContent bool
	var chunkHasError bool
	if !rw.inf.probe {
		parseable := rw.takeParseable(p)
		chunkHasContent, chunkHasError = rw.classifyParseable(parseable)
		// Track completion_tokens so isModelBurnEmpty can tell stripped-
		// content empties from no-tokens-generated empties.
		if tokens, ok := sseChunkUsageCompletionTokens(parseable); ok {
			rw.inf.usageComplTokens.Store(tokens)
		} else if tokens, ok := sseChunkUsageCompletionTokens(rw.inf.classifyPartial); ok {
			// Handle a newline-less final event buffered into classifyPartial.
			rw.inf.usageComplTokens.Store(tokens)
		}
	}
	if chunkHasContent || chunkHasError {
		rw.inf.contentChunks.Add(1)
		rw.group.maybeHoldWinnerCandidate(rw.inf)
	}
	if chunkHasContent && !rw.inf.suspicious {
		rw.group.setWinner(rw.nonce)
	} else if chunkHasContent && rw.inf.suspicious {
		rw.inf.suspiciousWinnerDeferredLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "suspicious_winner_deferred", "host", rw.inf.hostID)
		})
	}

	rw.group.mu.Lock()
	isWinner := rw.group.winner == rw.nonce
	winnerNonce := rw.group.winner
	rw.group.mu.Unlock()

	if firstOutputChunk {
		route := "loser"
		if isWinner {
			route = "winner"
		} else if rw.inf.probe {
			route = "probe"
		} else if winnerNonce == 0 {
			route = "pending"
		}
		logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "first_token", "host", rw.inf.hostID, "route", route, "winner_nonce", winnerNonce)
	}

	if rw.inf.probe {
		rw.inf.suppressedLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "poc_probe_stream_suppressed", "host", rw.inf.hostID, "winner_nonce", winnerNonce, "poc_reason", currentPoCPhaseReason())
		})
		return len(p), nil
	}

	switch {
	case isWinner:
		rw.group.clientMu.Lock()
		defer rw.group.clientMu.Unlock()
		if rw.group.isClientDetached() {
			rw.inf.pendingBuf = nil
			return len(p), nil
		}
		if err := rw.ctxErr(); err != nil {
			rw.inf.pendingBuf = nil
			rw.inf.ctxCancelledLog.Do(func() {
				logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "winner_write_ctx_cancelled",
					"host", rw.inf.hostID,
					"output_chunks", rw.inf.outputChunks.Load(),
					"content_chunks", rw.inf.contentChunks.Load(),
					"output_bytes", rw.inf.outputBytes.Load(),
					"where", "write",
					"error", err,
				)
			})
			return 0, err
		}
		rw.inf.forwardedLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "stream_forwarding_started", "host", rw.inf.hostID)
		})
		// On first content for the winner, flush any buffered pre-content
		// bytes (role chunk, etc.) before the current write so SSE event
		// ordering is preserved end-to-end.
		if !hadContentBefore && len(rw.inf.pendingBuf) > 0 && rw.group.w != nil {
			if _, err := rw.group.w.Write(rw.inf.pendingBuf); err != nil {
				rw.inf.pendingBuf = nil
				return 0, err
			}
			rw.inf.recordDeliveredForward(rw.inf.pendingBuf)
		}
		rw.inf.pendingBuf = nil
		if rw.group.w == nil {
			return len(p), nil
		}
		n, err := rw.group.w.Write(p)
		if n > 0 {
			rw.inf.recordDeliveredForward(p[:n])
		}
		return n, err

	case winnerNonce != 0:
		// Another attempt has already won; suppress this attempt's stream
		// entirely (existing behavior). Discard any buffered pre-content
		// bytes — they will never be forwarded.
		rw.inf.pendingBuf = nil
		rw.inf.suppressedLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "stream_suppressed", "host", rw.inf.hostID, "winner_nonce", winnerNonce)
		})
		return len(p), nil

	default:
		// No winner yet and this attempt has not produced content. Buffer
		// the bytes locally; if this attempt eventually produces content it
		// will become the winner and these bytes will be flushed in order.
		// If the attempt completes with no content at all, the buffer is
		// discarded by startInflight's empty-stream handling.
		rw.inf.pendingBuf = append(rw.inf.pendingBuf, p...)
		return len(p), nil
	}
}

func (rw *raceWriter) Flush() {
	if rw.inf.probe {
		return
	}
	rw.group.mu.Lock()
	isWinner := rw.group.winner == rw.nonce
	rw.group.mu.Unlock()
	if !isWinner {
		return
	}
	rw.group.clientMu.Lock()
	defer rw.group.clientMu.Unlock()
	if rw.group.isClientDetached() {
		return
	}
	if err := rw.ctxErr(); err != nil {
		rw.inf.ctxCancelledLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "winner_write_ctx_cancelled",
				"host", rw.inf.hostID,
				"output_chunks", rw.inf.outputChunks.Load(),
				"content_chunks", rw.inf.contentChunks.Load(),
				"output_bytes", rw.inf.outputBytes.Load(),
				"where", "flush",
				"error", err,
			)
		})
		return
	}
	if f, ok := rw.group.w.(http.Flusher); ok {
		f.Flush()
	}
}

// RunInference prepares and sends an inference, optionally racing a secondary.
// It replaces the old retry-based runInference in proxy.go.
func (e *Redundancy) RunInference(ctx context.Context, params user.InferenceParams, w io.Writer, clientFlag *cancelFlag) error {
	ctx, _ = ensureRequestLogContext(ctx)
	settleCtx, _ := ensureRequestLogContext(context.Background())
	settleCtx = logging.PropagateRequestID(settleCtx, ctx)
	logRequestStage(ctx, "runner_started", "escrow", e.devshardID, "input_tokens", params.InputLength, "model", params.Model)
	e.recordAccountingRequestStart(ctx, params)

	// triedParticipants is the per-request memory the picker uses to
	// avoid re-dispatching to a participant this request has already
	// tried. Keyed by participant identity (NOT slot index) so that
	// participants occupying multiple group slots count as one --
	// otherwise a request could be retried against the same physical
	// host through sibling slots, which is exactly what the picker
	// exists to prevent. Populated synchronously after each successful
	// prepareInflight; mutated by startAdditionalInflight (called from
	// awaitRace in the same goroutine), so no synchronisation needed.
	triedParticipants := map[string]bool{}

	primary, err := e.prepareInflight(ctx, params, triedParticipants)
	if err != nil {
		logRequestStage(ctx, "runner_prepare_failed", "escrow", e.devshardID, "error", err)
		if errors.Is(err, types.ErrInsufficientBalance) {
			e.fireBalanceExhausted()
		}
		if capErr := e.knownCapabilityExhaustionError(params, err); capErr != nil {
			return capErr
		}
		return err
	}
	triedParticipants[e.session.HostParticipantKey(primary.hostIdx)] = true
	primary.role = "primary"
	primary.startReason = "primary"

	decision := e.Decide(primary.hostIdx, params.InputLength)
	maxAttempts := e.maxAttempts()
	if primary.suspicious && maxAttempts > 1 && (!decision.RunSecondary || decision.Delay > 0) {
		decision = Decision{RunSecondary: true, Delay: 0, Reason: "primary_suspicious", ImmediateAttempts: 1}
	}
	if e.metrics != nil {
		e.metrics.RecordSpeculativeDecision(decision.Reason)
	}
	decisionFields := []any{
		"host", primary.hostID,
		"decision", decision.Reason,
		"delay_ms", decision.Delay.Milliseconds(),
		"max_attempts", maxAttempts,
		"group_size", e.groupSize,
	}
	if primary.suspicious {
		decisionFields = append(decisionFields,
			"primary_no_winner", true,
			"primary_no_winner_reason", primary.noWinnerReason,
		)
		if primary.noWinnerQuarantineMode != "" {
			decisionFields = append(decisionFields, "primary_quarantine_mode", primary.noWinnerQuarantineMode)
		}
		if primary.noWinnerFailureStrikes > 0 {
			decisionFields = append(decisionFields, "primary_failure_strikes", primary.noWinnerFailureStrikes)
		}
	}
	logInferenceStage(ctx, primary.escrowID, primary.nonce, "decision_made", decisionFields...)
	race := newRaceGroup(settleCtx, ctx, e.devshardID, w)
	attempts := []*inflight{primary}

	// Always start the primary.
	e.startInflight(settleCtx, primary, race, params, clientFlag)

	if decision.RunSecondary && decision.Delay == 0 && len(attempts) < maxAttempts {
		immediateAttempts := decision.ImmediateAttempts
		if immediateAttempts <= 0 {
			immediateAttempts = 1
		}
		for i := 0; i < immediateAttempts && len(attempts) < maxAttempts; i++ {
			logRequestStage(ctx, "secondary_immediate_start",
				"escrow", e.devshardID,
				"decision", decision.Reason,
				"attempt_index", i+1,
				"immediate_attempts", immediateAttempts,
			)
			trigger := attempts[len(attempts)-1]
			trigger.escalated = true
			if secondary := e.startAdditionalInflight(ctx, settleCtx, race, params, "secondary_immediate_start", trigger, decision.Reason, triedParticipants, clientFlag); secondary != nil {
				attempts = append(attempts, secondary)
			} else {
				break
			}
		}
	} else if decision.RunSecondary && decision.Delay == 0 {
		logInferenceStage(ctx, primary.escrowID, primary.nonce, "secondary_immediate_skipped",
			"host", primary.hostID,
			"reason", "attempt_limit",
			"decision", decision.Reason,
			"current_attempts", len(attempts),
			"max_attempts", maxAttempts,
		)
	}

	return e.awaitRace(ctx, settleCtx, attempts, race, params, decision, triedParticipants, clientFlag)
}

// prepareInflight enqueues a request with the session picker and waits
// for a nonce to be assigned. exclude is the set of participant keys
// this request has already tried; the picker matches the request to a
// nonce whose host's participant is NOT in exclude. There are two
// exhaustion paths:
//
//   - ErrAllHostsExcluded (synchronous): the request has already tried
//     every distinct participant in the group. No need to wake the
//     picker. We compare against the unique-participant count rather
//     than groupSize because a single participant can hold multiple
//     slots; using groupSize here would let us submit doomed requests.
//   - ErrNoAvailableHost (from picker): some not-yet-tried participants
//     exist but they are all PoC-required right now. The picker drops
//     the request immediately rather than queueing it indefinitely.
//
// The picker -- not this function -- decides whether the dispatch is a
// real inference or a PoC-style probe-burn. The probe flag flows back
// through pickerResult.isProbe and is recorded on the inflight so the
// rest of the lifecycle (raceWriter, perf tracking, escalation) can
// react accordingly.
func (e *Redundancy) prepareInflight(ctx context.Context, params user.InferenceParams, exclude map[string]bool) (*inflight, error) {
	if len(exclude) >= len(e.session.ParticipantKeys()) {
		return nil, ErrAllHostsExcluded
	}
	req := &pickerRequest{
		params:              params,
		excludeParticipants: exclude,
		ctx:                 ctx,
		submitTime:          time.Now(),
		reply:               make(chan pickerResult, 1),
	}
	e.picker.submit(req)

	select {
	case <-ctx.Done():
		// Abandon the reply channel; the picker will write into its
		// buffered slot and the result will be GC'd.
		return nil, ctx.Err()
	case res := <-req.reply:
		if res.err != nil {
			// Exhaustion sentinels are surfaced unwrapped so callers
			// can errors.Is() against them. Other errors are wrapped
			// for diagnostic context.
			if errors.Is(res.err, ErrNoAvailableHost) || errors.Is(res.err, ErrAllHostsExcluded) {
				return nil, res.err
			}
			return nil, fmt.Errorf("prepare: %w", res.err)
		}
		participantKey := e.session.HostParticipantKey(res.prepared.HostIdx())
		noWinner, noWinnerOK := e.noWinnerStatusForParticipant(participantKey)
		inf := &inflight{
			prepared:                 res.prepared,
			hostIdx:                  res.prepared.HostIdx(),
			hostID:                   e.session.HostLabel(res.prepared.HostIdx()),
			nonce:                    res.prepared.Nonce(),
			escrowID:                 e.devshardID,
			probe:                    res.isProbe,
			suspicious:               noWinnerOK,
			noWinnerReason:           noWinner.reason,
			noWinnerQuarantineMode:   noWinner.quarantineMode,
			noWinnerFailureStrikes:   noWinner.failureStrikes,
			participantClassifyBytes: participantClassify.counterFor(participantKey),
			done:                     make(chan struct{}),
			receiptCh:                make(chan struct{}),
			firstTokenCh:             make(chan struct{}),
		}
		e.recordAccountingAttempt(ctx, inf)
		return inf, nil
	}
}

func (e *Redundancy) startInflight(ctx context.Context, inf *inflight, race *raceGroup, params user.InferenceParams, clientFlag *cancelFlag) {
	// Per-attempt context derived from the settle context so the background
	// finalizer can cut off stragglers after the winner's grace window expires
	// without disturbing the settle context itself (which is shared across all
	// attempts). The cancel is called on the send goroutine's exit path as a
	// no-op after natural completion; explicit invocation from the finalizer
	// is what unwinds SendOnly for speculative losers that outlived the winner.
	attemptCtx, cancel := withMetaDrain(ctx, clientFlag)
	inf.setCancel(cancel)
	rw := &raceWriter{group: race, nonce: inf.nonce, inf: inf}
	receiptHandler := func() {
		inf.receiptOnce.Do(func() {
			now := time.Now()
			inf.setReceiptAt(now)
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "receipt_received", "host", inf.hostID, "elapsed_ms", now.Sub(inf.sendTime).Milliseconds())
			close(inf.receiptCh)
		})
	}
	logInferenceStage(ctx, inf.escrowID, inf.nonce, "prepared", "host", inf.hostID)
	if inf.probe {
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "poc_probe_prepared", "host", inf.hostID, "max_tokens", pocProbeMaxTokens, "poc_reason", currentPoCPhaseReason())
	}
	// Stamp sendTime synchronously, BEFORE spawning the send goroutine, so
	// that awaitRace's first iteration is guaranteed to see a non-zero
	// sendTime and schedule the receipt-timeout / first-token escalation.
	// Previously sendTime was assigned inside the goroutine below, which
	// introduced a scheduler race: if awaitRace iterated before the goroutine
	// ran, nextEscalationTrigger skipped this attempt (sendTime IsZero check)
	// and no escalation timer was ever scheduled. The main loop then only
	// woke on doneCh, so a slow or silent primary never got a secondary —
	// producing both tail-latency regressions (receipts that took seconds to
	// arrive) and full stalls (primary goes silent after receipt, first-token
	// fallback never fires). Setting sendTime here makes the invariant hold
	// before awaitRace can observe the attempt.
	inf.sendTime = time.Now()
	inf.startedBeforePoCGeneration = !currentPoCGenerationActive()
	e.recordGatewayAttemptStarted(inf, params)
	go e.monitorInflight(ctx, inf, race)

	go func() {
		defer close(inf.done)
		defer cancel()
		// Sole owner of classifyPartial: release on every exit path (incl. the early error return); content is classified synchronously via flushClassifyAndCheckEmpty below.
		defer inf.releaseClassifyPartial()
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "started", "host", inf.hostID)
		inf.resp, inf.err = e.session.SendOnly(attemptCtx, inf.prepared, rw, receiptHandler)
		streamBytes := int64(0)
		if inf.resp != nil {
			streamBytes = inf.resp.StreamBytesRead
		}
		if inf.err != nil {
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "send_failed",
				"host", inf.hostID,
				"output_chunks", inf.outputChunks.Load(),
				"content_chunks", inf.contentChunks.Load(),
				"output_bytes", inf.outputBytes.Load(),
				"stream_bytes_read", streamBytes,
				"error", inf.err,
			)
			e.maybeRecordEscrowStateDivergence(ctx, inf, inf.err)
			return
		}
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "send_completed",
			"host", inf.hostID,
			"output_chunks", inf.outputChunks.Load(),
			"content_chunks", inf.contentChunks.Load(),
			"output_bytes", inf.outputBytes.Load(),
			"stream_bytes_read", streamBytes,
		)
		// A receipt-backed transport-level success that produced zero content
		// events and did not produce a normal OpenAI error event is true empty
		// SSE/protocol boilerplate. This includes protocol-only responses where
		// stream_bytes_read > 0 but output_chunks == 0 because only devshard
		// receipt/meta events were parsed and no inference data was forwarded to
		// the race writer.
		if rw.flushClassifyAndCheckEmpty() {
			responseBodySample, responseSampleTruncated := bodySampleForLog(inf.pendingBuf, emptyStreamBodySampleLimit)
			inf.emptyResponseBodySample = responseBodySample
			inf.emptyResponseBodySampleTruncated = responseSampleTruncated
			// Discard any buffered bytes so they are never flushed if this
			// attempt is later promoted incorrectly.
			inf.pendingBuf = nil
			inf.err = errEmptyStream
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "empty_stream",
				"host", inf.hostID,
				"output_chunks", inf.outputChunks.Load(),
				"output_bytes", inf.outputBytes.Load(),
				"content_source", inf.contentSource,
				"request_flags", requestFlagsForLog(params),
			)
		}
		if !inf.probe && inf.errorSource != "" {
			responseBodySample, responseSampleTruncated := bodySampleForLog(inf.errorBodySample, emptyStreamBodySampleLimit)
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "error_stream",
				"host", inf.hostID,
				"output_chunks", inf.outputChunks.Load(),
				"output_bytes", inf.outputBytes.Load(),
				"error_source", inf.errorSource,
				"error_code", inf.errorCode,
				"error_type", inf.errorType,
				"error_message", inf.errorMessage,
				"response_body_sample", responseBodySample,
				"response_body_sample_truncated", responseSampleTruncated,
				"request_flags", requestFlagsForLog(params),
			)
		}
		if e.markPhaseTransitionAbort(inf) {
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "phase_transition_aborted",
				"host", inf.hostID,
				"poc_reason", currentPoCPhaseReason(),
			)
		}
	}()
}

// reconnectInflight re-issues the same PreparedInference with a resume cursor.
// It does not allocate a new nonce, call the picker, mutate triedParticipants,
// or record RealSend (R8). The caller must ensure no prior SendOnly /
// SendOnlyWithCursor is still running on this inflight (the reconnect ladder
// serializes tries; awaitRace marks inf as resuming so the race loop treats the
// resume as in-flight). On success, inf.err is cleared and inf.resp is merged
// so a single later processInflightOnce still runs (R1).
//
// ctx should be the settle context (same parent startInflight uses). The send
// runs under withMetaDrain so client disconnect still allows a bounded
// meta/receipt drain, and setCancel republishes the cancel so winner hard-timeout
// / loser finalizer can unwind the resumed stream.
//
// Step 4 wires this into the reconnect ladder; Step 3 exposes the primitive.
func (e *Redundancy) reconnectInflight(ctx context.Context, inf *inflight, race *raceGroup, params user.InferenceParams, clientFlag *cancelFlag) error {
	if e == nil || inf == nil || inf.prepared == nil {
		return fmt.Errorf("reconnect: missing inflight")
	}
	if race == nil {
		race = newRaceGroup(ctx, ctx, inf.escrowID, io.Discard)
	}

	events := inf.deliveredEvents
	partial := inf.deliveredPartial
	hadPriorContent := inf.contentChunks.Load() > 0 || events > 0
	try := inf.reconnectTries.Add(1)
	e.recordGatewayReconnectTry(inf, try)

	attemptCtx, cancel := withMetaDrain(ctx, clientFlag)
	inf.setCancel(cancel)
	defer cancel()

	rw := &raceWriter{group: race, nonce: inf.nonce, inf: inf}
	// Primary resume is host-side (cursor on HostRequest). prefixSkipWriter is
	// an R2 safety net that activates only when the host appears to re-emit
	// from event 0 despite the cursor (see probe logic in Write).
	stream := &prefixSkipWriter{
		w:            rw,
		skipEvents:   events,
		skipPartial:  partial,
		probeResume:  events > 0 || partial > 0,
		firstEventFP: inf.deliveredFirstEventFP,
	}
	receiptHandler := func() {
		inf.receiptOnce.Do(func() {
			now := time.Now()
			inf.setReceiptAt(now)
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "receipt_received", "host", inf.hostID, "elapsed_ms", now.Sub(inf.sendTime).Milliseconds(), "reconnect_try", try)
			close(inf.receiptCh)
		})
	}

	logInferenceStage(ctx, inf.escrowID, inf.nonce, "reconnect_started",
		"host", inf.hostID,
		"reconnect_try", try,
		"delivered_events", events,
		"delivered_partial", partial,
	)
	resp, err := e.session.SendOnlyWithCursor(attemptCtx, inf.prepared, stream, receiptHandler, events, partial)
	mergeInflightHostResponse(inf, resp, err)

	if err != nil {
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "reconnect_failed",
			"host", inf.hostID,
			"reconnect_try", try,
			"error", err,
		)
		return err
	}
	logInferenceStage(ctx, inf.escrowID, inf.nonce, "reconnect_completed",
		"host", inf.hostID,
		"reconnect_try", try,
		"output_chunks", inf.outputChunks.Load(),
		"content_chunks", inf.contentChunks.Load(),
		"delivered_events", inf.deliveredEvents,
		"delivered_partial", inf.deliveredPartial,
	)
	// Mid-event resume often forwards a JSON fragment that does not re-classify
	// as a content event. If the client already received content before the
	// drop, do not treat that as empty_stream.
	if rw.flushClassifyAndCheckEmpty() && !hadPriorContent && inf.contentChunks.Load() == 0 {
		inf.pendingBuf = nil
		inf.err = errEmptyStream
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "empty_stream",
			"host", inf.hostID,
			"reconnect_try", try,
		)
		return inf.err
	}
	_ = params // reserved for future reconnect-specific empty-stream logging
	return nil
}

// mergeInflightHostResponse folds a reconnect HostResponse into the inflight.
// Prefers the response that carries receipt / mempool txs (R1). Transport
// success (err==nil) clears a prior transport error so settlement can proceed.
func mergeInflightHostResponse(inf *inflight, resp *host.HostResponse, err error) {
	if inf == nil {
		return
	}
	if resp != nil {
		inf.resp = preferRicherHostResponse(inf.resp, resp)
	}
	if err == nil {
		inf.err = nil
		return
	}
	// Keep a prior nil err only if we somehow got err with no resp change;
	// reconnect outcome is authoritative for the latest transport try.
	inf.err = err
}

func preferRicherHostResponse(prev, next *host.HostResponse) *host.HostResponse {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	out := *prev
	if len(next.Receipt) > 0 {
		out.Receipt = next.Receipt
		out.ConfirmedAt = next.ConfirmedAt
	}
	if len(next.Mempool) > 0 {
		out.Mempool = next.Mempool
	}
	if len(next.StateSig) > 0 {
		out.StateSig = next.StateSig
	}
	if len(next.StateHash) > 0 {
		out.StateHash = next.StateHash
	}
	if next.CachedResponseBody != nil {
		out.CachedResponseBody = next.CachedResponseBody
	}
	if next.LiveAttach {
		out.LiveAttach = true
	}
	if next.StreamBytesRead > out.StreamBytesRead {
		out.StreamBytesRead = next.StreamBytesRead
	}
	if next.Nonce != 0 {
		out.Nonce = next.Nonce
	}
	if next.InferenceID != 0 {
		out.InferenceID = next.InferenceID
	}
	out.DeliveredEvents = next.DeliveredEvents
	out.DeliveredPartial = next.DeliveredPartial
	return &out
}

// recordGatewayReconnectTry records a same-nonce transport retry without
// treating it as a new attempt (no RealSend).
func (e *Redundancy) recordGatewayReconnectTry(inf *inflight, try int64) {
	if e == nil || inf == nil {
		return
	}
	logInferenceStage(context.Background(), inf.escrowID, inf.nonce, "reconnect_try",
		"host", inf.hostID,
		"reconnect_try", try,
		"delivered_events", inf.deliveredEvents,
		"delivered_partial", inf.deliveredPartial,
	)
}

// attemptReconnectAllowed is the R0+R7 gate: setting on AND session protocol ≥ v5.
func (e *Redundancy) attemptReconnectAllowed() bool {
	if e == nil || e.session == nil || !AttemptReconnectEnabled {
		return false
	}
	ver, err := types.ParseProtocolVersion(e.session.ProtocolVersion())
	if err != nil {
		return false
	}
	return types.ProtocolVersionAtLeast(ver, types.ProtocolV5)
}

func (e *Redundancy) shouldAttemptWinnerReconnect(inf *inflight) bool {
	if e == nil || inf == nil || inf.probe {
		return false
	}
	if !e.attemptReconnectAllowed() {
		return false
	}
	// Only protect a delivered client prefix (R4). Pre-content failures keep
	// today's attempt_failed escalation.
	if inf.deliveredEvents == 0 && inf.contentChunks.Load() == 0 {
		return false
	}
	return inf.reconnectLadderUsed.CompareAndSwap(false, true)
}

// winnerReconnectHedgeRequest asks awaitRace to start a reconnect-budget hedge
// on the race loop goroutine (attempts / triedParticipants stay single-threaded).
// The ladder waits on done so reservation is still held while the hedge starts.
type winnerReconnectHedgeRequest struct {
	trigger *inflight
	done    chan struct{}
}

// spawnWinnerReconnectLadder re-arms the winner as in-flight and runs the
// reconnect ladder in the background so awaitRace can keep servicing
// winnerHardTimeoutC / stallC / client cancel while the resumed stream runs.
func (e *Redundancy) spawnWinnerReconnectLadder(
	streamCtx, settleCtx context.Context,
	inf *inflight,
	race *raceGroup,
	params user.InferenceParams,
	triedParticipants map[string]bool,
	clientFlag *cancelFlag,
	doneCh chan *inflight,
	hedgeCh chan<- winnerReconnectHedgeRequest,
) {
	if e == nil || inf == nil {
		return
	}
	// Mark the attempt as resuming instead of re-opening inf.done: hard timeout
	// / inter-chunk stall keep arming and allInflightsDone will not short-circuit
	// the race, without mutating a channel that monitorInflight and the
	// background finalizers read concurrently.
	resumeDone := make(chan struct{})
	inf.resumeMu.Lock()
	inf.resumeDone = resumeDone
	inf.resumeMu.Unlock()
	inf.resuming.Store(true)
	go func() {
		// Closed last so waitInflightSettled cannot release a finalizer while
		// this goroutine is still touching inf.
		defer close(resumeDone)
		err := e.runWinnerReconnectLadder(streamCtx, settleCtx, inf, race, params, triedParticipants, clientFlag, hedgeCh)
		if err != nil && inf.err == nil {
			inf.err = err
		}
		inf.resuming.Store(false)
		// Re-signal the race loop; the channel send publishes inf.err/inf.resp.
		// If awaitRace has already given up, its finalizer waits on resumeDone.
		select {
		case doneCh <- inf:
		case <-streamCtx.Done():
		}
	}()
}

// runWinnerReconnectLadder runs same-nonce reconnect tries under a reservation.
//
// R3: reconnect attempts run without blocking the budget timer. When
// AttemptReconnectBudget expires without a successful resume, startHedge fires
// and the ladder keeps trying — hedge and reconnect race under R4 (hedge
// suppressed for client I/O while a delivered prefix exists).
//
// Hedge starts are requested via hedgeCh so awaitRace can append attempts on
// its own goroutine. When hedgeCh is nil (direct/unit-test callers), the hedge
// is started inline and the caller must supply a local attempts sink via the
// optional syncHedge hook — see runWinnerReconnectLadderSync.
//
// Returns nil when a reconnect try succeeds; otherwise a resume-exhausted
// error. Never promotes the hedge onto the client when a prefix was already
// delivered (R4/R5).
func (e *Redundancy) runWinnerReconnectLadder(
	streamCtx, settleCtx context.Context,
	inf *inflight,
	race *raceGroup,
	params user.InferenceParams,
	triedParticipants map[string]bool,
	clientFlag *cancelFlag,
	hedgeCh chan<- winnerReconnectHedgeRequest,
) error {
	return e.runWinnerReconnectLadderWithHedge(streamCtx, settleCtx, inf, race, params, triedParticipants, clientFlag, hedgeCh, nil)
}

// runWinnerReconnectLadderSync is the unit-test entry that starts hedges inline
// against a local attempts slice (same goroutine as the ladder).
func (e *Redundancy) runWinnerReconnectLadderSync(
	streamCtx, settleCtx context.Context,
	inf *inflight,
	race *raceGroup,
	params user.InferenceParams,
	attempts *[]*inflight,
	triedParticipants map[string]bool,
	clientFlag *cancelFlag,
	doneCh chan *inflight,
) error {
	syncHedge := func() *inflight {
		if attempts == nil || streamCtx.Err() != nil {
			return nil
		}
		if len(*attempts) >= e.maxAttempts() {
			return nil
		}
		next := e.startAdditionalInflight(streamCtx, settleCtx, race, params, "attempt_reconnect_budget", inf, "winner_reconnect_hedge", triedParticipants, clientFlag)
		if next != nil {
			*attempts = append(*attempts, next)
			if doneCh != nil {
				e.watchInflightDone(next, doneCh)
			}
		}
		return next
	}
	return e.runWinnerReconnectLadderWithHedge(streamCtx, settleCtx, inf, race, params, triedParticipants, clientFlag, nil, syncHedge)
}

func (e *Redundancy) runWinnerReconnectLadderWithHedge(
	streamCtx, settleCtx context.Context,
	inf *inflight,
	race *raceGroup,
	params user.InferenceParams,
	triedParticipants map[string]bool,
	clientFlag *cancelFlag,
	hedgeCh chan<- winnerReconnectHedgeRequest,
	syncHedge func() *inflight,
) error {
	budget := AttemptReconnectBudget
	if budget <= 0 {
		budget = time.Second
	}
	maxTries := AttemptReconnectMaxTries
	if maxTries <= 0 {
		maxTries = 2
	}
	deadline := time.Now().Add(budget)
	race.reserveWinner(inf.nonce)
	defer race.clearReservation()
	// Blip on every ladder exit (resume or exhausted), not at entry — avoids
	// scoring before outcome and never writes Responsive:false failure samples.
	defer e.recordReconnectBlipOnce(inf)

	logRequestStage(settleCtx, "winner_reconnect_ladder_started",
		"escrow", e.devshardID,
		"winner_nonce", inf.nonce,
		"host", inf.hostID,
		"budget_ms", budget.Milliseconds(),
		"max_tries", maxTries,
		"delivered_events", inf.deliveredEvents,
		"delivered_partial", inf.deliveredPartial,
	)

	hedgeStarted := false
	startHedge := func() {
		if hedgeStarted || streamCtx.Err() != nil {
			return
		}
		hedgeStarted = true
		if hedgeCh != nil {
			req := winnerReconnectHedgeRequest{trigger: inf, done: make(chan struct{})}
			select {
			case hedgeCh <- req:
				select {
				case <-req.done:
				case <-streamCtx.Done():
				}
			case <-streamCtx.Done():
			}
			return
		}
		if syncHedge == nil {
			return
		}
		if next := syncHedge(); next != nil {
			logRequestStage(settleCtx, "winner_reconnect_hedge_started",
				"escrow", e.devshardID,
				"winner_nonce", inf.nonce,
				"hedge_nonce", next.nonce,
				"hedge_host", next.hostID,
			)
		}
	}

	// Budget timer runs independently of reconnectInflight so a slow/hanging
	// same-nonce resume cannot delay the hedge (R3).
	var budgetTimer *time.Timer
	if wait := time.Until(deadline); wait <= 0 {
		startHedge()
	} else {
		budgetTimer = time.NewTimer(wait)
		defer stopTimer(budgetTimer)
	}
	budgetCh := func() <-chan time.Time {
		if budgetTimer == nil {
			return nil
		}
		return budgetTimer.C
	}
	onBudget := func() {
		if budgetTimer == nil {
			return
		}
		budgetTimer = nil
		startHedge()
	}

	type reconnectOutcome struct {
		err error
	}

	var lastErr error
	for try := 0; try < maxTries; try++ {
		if err := streamCtx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			startHedge()
			if budgetTimer != nil {
				stopTimer(budgetTimer)
				budgetTimer = nil
			}
		}

		resultCh := make(chan reconnectOutcome, 1)
		go func() {
			// settleCtx parent matches startInflight: client disconnect is
			// signaled via clientFlag / withMetaDrain, not stream cancel.
			err := e.reconnectInflight(settleCtx, inf, race, params, clientFlag)
			resultCh <- reconnectOutcome{err: err}
		}()

		var outcome reconnectOutcome
		gotResult := false
		for !gotResult {
			select {
			case <-streamCtx.Done():
				inf.cancelAttempt()
				<-resultCh // allow reconnect goroutine to exit
				return streamCtx.Err()
			case <-budgetCh():
				onBudget()
			case outcome = <-resultCh:
				gotResult = true
			}
		}

		lastErr = outcome.err
		if lastErr == nil && inf.err != nil {
			lastErr = inf.err
		}
		if lastErr == nil && inf.err == nil {
			logRequestStage(settleCtx, "winner_reconnect_resumed",
				"escrow", e.devshardID,
				"winner_nonce", inf.nonce,
				"host", inf.hostID,
				"reconnect_try", inf.reconnectTries.Load(),
				"hedge_started", hedgeStarted,
			)
			return nil
		}
		if try+1 >= maxTries {
			break
		}

		backoff := reconnectTryBackoff
		if backoff < 0 {
			backoff = 0
		}
		timer := time.NewTimer(backoff)
		backoffDone := false
		for !backoffDone {
			select {
			case <-streamCtx.Done():
				stopTimer(timer)
				return streamCtx.Err()
			case <-budgetCh():
				onBudget()
			case <-timer.C:
				backoffDone = true
			}
		}
	}

	if !hedgeStarted {
		if ch := budgetCh(); ch != nil {
			select {
			case <-streamCtx.Done():
				return streamCtx.Err()
			case <-ch:
				onBudget()
			}
		} else {
			startHedge()
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("inference: winner resume exhausted")
	}
	logRequestStage(settleCtx, "winner_reconnect_exhausted",
		"escrow", e.devshardID,
		"winner_nonce", inf.nonce,
		"host", inf.hostID,
		"reconnect_tries", inf.reconnectTries.Load(),
		"hedge_started", hedgeStarted,
		"error", lastErr,
	)
	_ = AllowStreamResetOnFailover // R5 opt-in wired in a later change; default remains fail.
	return lastErr
}

// recordReconnectBlipOnce records that this participant needed a reconnect
// ladder (R4/R8). Observability only: it does not write RequestSample failures,
// call ObserveStalledWinner, or influence Decide / the picker.
func (e *Redundancy) recordReconnectBlipOnce(inf *inflight) {
	if e == nil || inf == nil {
		return
	}
	inf.reconnectPenaltyOnce.Do(func() {
		participantKey := e.participantKeyForHost(inf.hostIdx)
		var blipCount int
		if e.perf != nil {
			e.perf.RecordReconnectBlip(participantKey)
			blipCount = e.perf.ReconnectBlipCount(participantKey)
		}
		windowMS := ReconnectBlipWindow.Milliseconds()
		if windowMS <= 0 {
			windowMS = (5 * time.Minute).Milliseconds()
		}
		logInferenceStage(context.Background(), inf.escrowID, inf.nonce, "reconnect_blip",
			"host", inf.hostID,
			"participant_key", participantKey,
			"blip_count", blipCount,
			"window_ms", windowMS,
		)
	})
}

// maxPrefixSkipProbeBytes bounds prefixSkipWriter.forming while the resume
// probe is undecided (blank-line / slow-start hosts must not grow unboundedly).
const maxPrefixSkipProbeBytes = 64 << 10

// prefixSkipWriter drops an already-delivered upstream prefix before forwarding
// to the underlying writer. Safety net if a host re-emits from event 0 on
// reconnect (R2). Blank SSE separator lines are not counted as events.
//
// When probeResume is set (same-nonce reconnect that also passed a cursor to
// the host), the writer first decides whether the stream looks like a full
// restart. A correct host emits only the remainder — which for a mid-event
// break does not start a new "data:" line — and must not be event-skipped.
// Event-boundary resumes compare the first content line to firstEventFP (hash
// of the first delivered event); a match means the host restarted from event 0.
type prefixSkipWriter struct {
	w            io.Writer
	skipEvents   int64
	skipPartial  int64
	eventsSeen   int64
	forming      []byte
	doneSkipping bool
	probeResume  bool
	probed       bool
	emitted      bool   // true after forwarding a non-blank line
	firstEventFP uint64 // fingerprint of first delivered content line; 0 = unset
}

func (p *prefixSkipWriter) Write(b []byte) (int, error) {
	if p == nil || p.w == nil {
		return len(b), nil
	}
	if p.doneSkipping {
		return p.w.Write(b)
	}
	if !p.probeResume && p.skipEvents == 0 && p.skipPartial == 0 && len(p.forming) == 0 {
		p.doneSkipping = true
		return p.w.Write(b)
	}
	p.forming = append(p.forming, b...)
	undecidable := p.boundProbeForming()
	if p.probeResume && !p.probed {
		decided, applySkip := p.decideProbe()
		if !decided && undecidable {
			// One oversized line with no boundary to compare: trust the host
			// rather than truncating bytes the client still needs.
			decided, applySkip = true, false
		}
		if !decided {
			return len(b), nil
		}
		p.probed = true
		if !applySkip {
			// Host honored the cursor — forward the buffered remainder as-is.
			p.doneSkipping = true
			out := p.forming
			p.forming = nil
			if len(out) == 0 {
				return len(b), nil
			}
			if _, err := p.w.Write(out); err != nil {
				return 0, err
			}
			return len(b), nil
		}
		// applySkip: fall through and run event/partial skip on forming.
	}
	var forward []byte
	for {
		nl := bytes.IndexByte(p.forming, '\n')
		if nl == -1 {
			break
		}
		line := p.forming[:nl]
		p.forming = p.forming[nl+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			// Drop separators until we have forwarded real content so a skipped
			// event's trailing blank does not become a leading "\n".
			if p.emitted {
				forward = append(forward, '\n')
			}
			continue
		}
		if p.eventsSeen < p.skipEvents {
			p.eventsSeen++
			continue
		}
		// First event past skipEvents: apply skipPartial into this line.
		if p.skipPartial > 0 {
			if p.skipPartial >= int64(len(line)) {
				p.skipPartial = 0
				p.eventsSeen++
				continue
			}
			line = line[p.skipPartial:]
			p.skipPartial = 0
			p.eventsSeen++
			forward = append(forward, line...)
			forward = append(forward, '\n')
			p.emitted = true
			continue
		}
		p.eventsSeen++
		forward = append(forward, line...)
		forward = append(forward, '\n')
		p.emitted = true
	}
	// Incomplete trailing fragment.
	if len(p.forming) > 0 {
		if p.eventsSeen < p.skipEvents {
			// Still inside skipped prefix; hold in forming.
		} else if p.skipPartial > 0 {
			if p.skipPartial >= int64(len(p.forming)) {
				p.skipPartial -= int64(len(p.forming))
				p.forming = nil
			} else {
				rem := p.forming[p.skipPartial:]
				p.forming = nil
				p.skipPartial = 0
				p.doneSkipping = true
				forward = append(forward, rem...)
				p.emitted = true
			}
		} else {
			p.doneSkipping = true
			forward = append(forward, p.forming...)
			p.forming = nil
			p.emitted = true
		}
	}
	if p.eventsSeen >= p.skipEvents && p.skipPartial == 0 && len(p.forming) == 0 {
		p.doneSkipping = true
	}
	if len(forward) == 0 {
		return len(b), nil
	}
	if _, err := p.w.Write(forward); err != nil {
		return 0, err
	}
	return len(b), nil
}

// decideProbe returns (decided, applySkip). applySkip means the host appears
// to have restarted from event 0 and the event/partial skip should run.
func (p *prefixSkipWriter) decideProbe() (decided, applySkip bool) {
	if p.skipPartial > 0 {
		// Need enough bytes to see whether this is a fresh SSE line or the
		// mid-event remainder the cursor asked for.
		nl := bytes.IndexByte(p.forming, '\n')
		if nl == -1 && int64(len(p.forming)) <= p.skipPartial {
			return false, false
		}
		line := p.forming
		if nl >= 0 {
			line = p.forming[:nl]
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			return false, false
		}
		// Mid-event remainder must not look like a new SSE field line.
		if isSSEFieldLine(line) {
			return true, true
		}
		return true, false
	}
	// Event-boundary resume: wait for the first non-blank line, then compare
	// to the fingerprint of the first event already delivered to the client.
	nl := bytes.IndexByte(p.forming, '\n')
	if nl == -1 {
		return false, false
	}
	line := bytes.TrimSpace(p.forming[:nl])
	if len(line) == 0 {
		return false, false
	}
	if p.firstEventFP != 0 && sseEventFingerprint(line) == p.firstEventFP {
		return true, true
	}
	return true, false
}

// boundProbeForming drops leading blank SSE separators so a host emitting only
// newlines cannot grow the undecided probe buffer without limit. It never
// discards content bytes: those are still owed to the client. When the buffer
// is over the cap and holds one unterminated line, there is nothing left to
// compare, so it reports undecidable and the caller gives up on the probe.
func (p *prefixSkipWriter) boundProbeForming() (undecidable bool) {
	if p == nil || !p.probeResume || p.probed {
		return false
	}
	for len(p.forming) > maxPrefixSkipProbeBytes {
		nl := bytes.IndexByte(p.forming, '\n')
		if nl == -1 {
			return true
		}
		if len(bytes.TrimSpace(p.forming[:nl])) != 0 {
			return false
		}
		p.forming = p.forming[nl+1:]
	}
	return false
}

func isSSEFieldLine(line []byte) bool {
	switch {
	case bytes.HasPrefix(line, []byte("data:")),
		bytes.HasPrefix(line, []byte("event:")),
		bytes.HasPrefix(line, []byte("id:")),
		bytes.HasPrefix(line, []byte("retry:")):
		return true
	default:
		return false
	}
}

func sseEventFingerprint(line []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(line)
	return h.Sum64()
}

// startDelayed waits for receipt or timeout, then starts a secondary if needed.
// Returns nil if receipt arrived before timeout (no secondary needed).
func (e *Redundancy) startAdditionalInflight(streamCtx, settleCtx context.Context, race *raceGroup, params user.InferenceParams, stage string, trigger *inflight, reason string, triedParticipants map[string]bool, clientFlag *cancelFlag) *inflight {
	if streamCtx.Err() != nil {
		return nil
	}
	// After a crown, refuse new attempts unless a reconnect reservation is
	// holding the winner — then the secondary is a suppressed hedge only (R4).
	if race.hasDecided() && !race.hasReservation() {
		return nil
	}
	fields := []any{"host", trigger.hostID}
	if delay := e.escalationDelay(stage, params); delay > 0 {
		fields = append(fields, "delay_ms", delay.Milliseconds())
	}
	logInferenceStage(settleCtx, trigger.escrowID, trigger.nonce, stage, fields...)
	next, err := e.prepareInflight(streamCtx, params, triedParticipants)
	if err != nil {
		// Distinguish exhaustion from generic prepare failures so the
		// next stress test can measure how often the per-request
		// exclude set actually saturates the escrow. When exhausted,
		// existing in-flight attempts will run to completion and the
		// race will resolve naturally; we just stop scheduling more.
		if errors.Is(err, ErrAllHostsExcluded) || errors.Is(err, ErrNoAvailableHost) {
			// Both exhaustion paths land here: either we have tried
			// every slot or the picker says no untried slot is
			// currently usable. In either case stop scheduling more
			// attempts and let in-flight ones finish naturally.
			logRequestStage(settleCtx, "picker_exhausted",
				"escrow", e.devshardID,
				"decision", reason,
				"tried_participants", len(triedParticipants),
				"unique_participants", len(e.session.ParticipantKeys()),
				"group_size", e.groupSize,
				"reason_err", err.Error(),
			)
			return nil
		}
		logRequestStage(settleCtx, "secondary_prepare_failed", "escrow", e.devshardID, "decision", reason, "error", err)
		return nil
	}
	triedParticipants[e.session.HostParticipantKey(next.hostIdx)] = true
	next.role = "secondary"
	next.startReason = reason
	if e.metrics != nil {
		e.metrics.RecordSpeculativeAttemptStart(reason)
	}
	if reason == "pairwise_budgeted_speedup" {
		e.maybeAddPairwiseWinnerHoldCandidate(race, params, trigger, next)
	}
	e.startInflight(settleCtx, next, race, params, clientFlag)
	return next
}

func reducedMaxTokensParams(params user.InferenceParams) (user.InferenceParams, bool) {
	if params.MaxTokens <= 1 {
		return params, false
	}
	reducedMaxTokens := params.MaxTokens / 2
	if reducedMaxTokens == 0 {
		reducedMaxTokens = 1
	}
	prompt, ok := rewritePromptMaxTokens(params.Prompt, reducedMaxTokens)
	if !ok {
		return params, false
	}
	params.Prompt = prompt
	params.MaxTokens = reducedMaxTokens
	return params, true
}

func rewritePromptMaxTokens(prompt []byte, maxTokens uint64) ([]byte, bool) {
	var raw map[string]any
	if err := json.Unmarshal(prompt, &raw); err != nil {
		return nil, false
	}
	_, hasMaxCompletionTokens := raw["max_completion_tokens"]
	_, hasMaxTokens := raw["max_tokens"]
	if hasMaxCompletionTokens {
		raw["max_completion_tokens"] = maxTokens
	}
	if hasMaxTokens || !hasMaxCompletionTokens {
		raw["max_tokens"] = maxTokens
	}
	if minTokens, ok := devshard.JSONNumericUint64(raw["min_tokens"]); ok && minTokens > maxTokens {
		raw["min_tokens"] = maxTokens
	}
	updated, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	return updated, true
}

func (e *Redundancy) maybeAddPairwiseWinnerHoldCandidate(race *raceGroup, params user.InferenceParams, trigger, next *inflight) {
	if e == nil || e.perf == nil || e.perf.pairwise == nil || race == nil || trigger == nil || next == nil {
		return
	}
	triggerParticipant := e.session.HostParticipantKey(trigger.hostIdx)
	nextParticipant := e.session.HostParticipantKey(next.hostIdx)
	speedup, samples, ok := e.perf.pairwise.HoldEligible(params.Model, params.InputLength, triggerParticipant, nextParticipant)
	if !ok {
		return
	}
	race.addWinnerHoldCandidate(next)
	logRequestStage(race.logCtx, "pairwise_winner_hold_candidate",
		"escrow", e.devshardID,
		"from_nonce", trigger.nonce,
		"from_host", trigger.hostID,
		"to_nonce", next.nonce,
		"to_host", next.hostID,
		"speedup", speedup,
		"samples", samples,
		"hold_ms", PairwiseWinnerHold.Milliseconds(),
	)
}

func defaultFirstTokenFallbackDelay(inputTokens uint64) time.Duration {
	tokens := float64(inputTokens)
	seconds := 1.7 + 0.00003*tokens + 0.0000000005*tokens*tokens
	if seconds < 0 {
		seconds = 0
	}
	delay := time.Duration(seconds * float64(time.Second))
	if delay < FirstTokenTimeoutCap {
		delay = FirstTokenTimeoutCap
	}
	return delay
}

func (e *Redundancy) firstTokenFallbackDelay(params user.InferenceParams) time.Duration {
	model := params.Model
	if model == "" && e != nil {
		model = e.model
	}
	if e != nil && e.perf != nil {
		if delay, ok := e.perf.FirstTokenFallbackDelay(model, params.InputLength); ok {
			return delay
		}
	}
	return defaultFirstTokenFallbackDelay(params.InputLength)
}

func receiptTimeoutForInput(inputTokens uint64) time.Duration {
	if inputTokens > 100_000 {
		return ReceiptTimeout * 2
	}
	return ReceiptTimeout
}

func nonStreamingFallbackDelay(inputTokens uint64) time.Duration {
	delay := time.Duration(inputTokens) * PerInputTokenResponseLag
	if delay < NonStreamResponseFloor {
		return NonStreamResponseFloor
	}
	return delay
}

func interChunkStallDeadline(inf *inflight) (time.Time, bool) {
	if inf == nil || inf.probe || inflightDone(inf) || InterChunkStallLogThreshold <= 0 {
		return time.Time{}, false
	}
	if inf.contentChunks.Load() == 0 {
		return time.Time{}, false
	}
	inf.stallMu.Lock()
	active := inf.stallActive
	inf.stallMu.Unlock()
	if active {
		return time.Time{}, false
	}
	lastChunkAt := inf.lastChunkAt.Load()
	if lastChunkAt <= 0 {
		return time.Time{}, false
	}
	return time.Unix(0, lastChunkAt).Add(InterChunkStallLogThreshold), true
}

func nextInterChunkStallTrigger(attempts []*inflight) (*inflight, time.Time, bool) {
	var (
		chosen   *inflight
		deadline time.Time
		ok       bool
	)
	for _, inf := range attempts {
		d, candidate := interChunkStallDeadline(inf)
		if !candidate {
			continue
		}
		if !ok || d.Before(deadline) {
			chosen = inf
			deadline = d
			ok = true
		}
	}
	return chosen, deadline, ok
}

func winnerHardTimeoutDeadline(inf *inflight) (time.Time, bool) {
	if inf == nil || inf.probe || inflightDone(inf) || StreamingAttemptHardTimeout <= 0 || inf.sendTime.IsZero() {
		return time.Time{}, false
	}
	if inf.contentChunks.Load() == 0 {
		return time.Time{}, false
	}
	return inf.sendTime.Add(StreamingAttemptHardTimeout), true
}

func waitForFirstTokenUntil(ctx context.Context, inf *inflight, deadline time.Time) bool {
	if inf.hasFirstToken() {
		return true
	}
	d := time.Until(deadline)
	if d <= 0 {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-inf.firstTokenCh:
		return true
	case <-inf.done:
		return inf.hasFirstToken()
	case <-timer.C:
		return inf.hasFirstToken()
	case <-ctx.Done():
		return false
	}
}

func waitForInflightDoneUntil(ctx context.Context, inf *inflight, deadline time.Time) bool {
	d := time.Until(deadline)
	if d <= 0 {
		select {
		case <-inf.done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-inf.done:
		return true
	case <-timer.C:
		select {
		case <-inf.done:
			return true
		default:
			return false
		}
	case <-ctx.Done():
		return false
	}
}

type escalationTrigger struct {
	inf      *inflight
	deadline time.Time
	stage    string
	reason   string
}

// winningInflightTerminalFailure reports whether the race winner's HTTP
// attempt has finished in a state that must surface as a client error
// (transport error, process failure, or chain protocol incomplete for the
// crowned nonce). Caller must ensure inflightDone(inf) and that inf is the
// current race winner (the race writer crowned this nonce after at least one
// accepted content chunk).
func (e *Redundancy) winningInflightTerminalFailure(inf *inflight) (failed bool, err error) {
	if inf == nil || inf.probe {
		return false, nil
	}
	if inf.err != nil {
		return true, inf.err
	}
	if inf.resp == nil {
		return true, fmt.Errorf("inference: winner host returned no response")
	}
	if err := e.processInflightOnce(inf); err != nil {
		return true, err
	}
	nonceFinished := e.session.IsNonceFinished(inf.nonce)
	ok := nonceFinished && !isEmptyStreamAttempt(inf)
	if ok {
		return false, nil
	}
	if hostErr := hostApplicationErrorFromInflight(inf); hostErr != nil {
		return true, hostErr
	}
	return true, fmt.Errorf("inference: winner inference incomplete (nonce_finished=%v)", nonceFinished)
}

func (e *Redundancy) awaitRace(streamCtx, settleCtx context.Context, attempts []*inflight, race *raceGroup, params user.InferenceParams, decision Decision, triedParticipants map[string]bool, clientFlag *cancelFlag) error {
	doneCh := make(chan *inflight, e.maxAttempts()+2)
	for _, inf := range attempts {
		e.watchInflightDone(inf, doneCh)
	}
	// Reconnect-budget hedges must be started on this goroutine so attempts /
	// triedParticipants stay single-threaded (see RunInference comment).
	reconnectHedgeCh := make(chan winnerReconnectHedgeRequest, 1)
	requestStart := time.Now()
	if len(attempts) > 0 && !attempts[0].sendTime.IsZero() {
		requestStart = attempts[0].sendTime
	}
	reducedMaxTokensFallbackStarted := false

	for {
		winner := race.winnerNonce()
		var winnerC <-chan struct{}
		if winner == 0 {
			winnerC = race.winnerSignal()
		}
		// As soon as the winner has fully delivered its stream and committed
		// the chain protocol, return to the caller so the handler can write
		// `[DONE]` and close the connection. Any still-running speculative
		// losers are handed off to a background finalizer that waits up to
		// SecondaryWaitAfterWinner for them to complete naturally; anything
		// still outstanding at that point is cancelled, which triggers the
		// normal failure path (HandleTimeout vote, perf tracking) through
		// finishRaceOutcome.
		if winner != 0 {
			if winning := inflightByNonce(attempts, winner); winning != nil && inflightDone(winning) && inflightFinished(winning) {
				if pending := pendingInflights(attempts); len(pending) > 0 {
					logRequestStage(settleCtx, "request_returned_while_speculation_pending",
						"escrow", e.devshardID,
						"winner_nonce", winner,
						"pending", len(pending),
						"max_wait_ms", SecondaryWaitAfterWinner.Milliseconds(),
						"decision", decision.Reason,
					)
					e.goTrackedRaceCleanup(func() {
						e.finishRaceWhenPendingDone(settleCtx, attempts, params, decision, winner, raceFinishOptions{recordFailureSamples: true})
					})
					return nil
				}
			}
		}

		trigger, hasTrigger := e.nextEscalationTrigger(attempts, params)
		maxAttempts := e.maxAttempts()
		var escalationTimer *time.Timer
		var escalationC <-chan time.Time
		if hasTrigger && winner == 0 && len(attempts) < maxAttempts {
			wait := time.Until(trigger.deadline)
			if wait < 0 {
				wait = 0
			}
			escalationTimer = time.NewTimer(wait)
			escalationC = escalationTimer.C
		} else if hasTrigger && winner == 0 {
			logInferenceStage(settleCtx, trigger.inf.escrowID, trigger.inf.nonce, "escalation_skipped",
				"host", trigger.inf.hostID,
				"stage", trigger.stage,
				"reason", "attempt_limit",
				"current_attempts", len(attempts),
				"max_attempts", maxAttempts,
			)
		}
		var reducedFallbackTimer *time.Timer
		var reducedFallbackC <-chan time.Time
		if !params.Stream && !reducedMaxTokensFallbackStarted && winner == 0 {
			wait := time.Until(requestStart.Add(nonStreamingReducedMaxTokensFallbackDelay))
			if wait < 0 {
				wait = 0
			}
			reducedFallbackTimer = time.NewTimer(wait)
			reducedFallbackC = reducedFallbackTimer.C
		}
		var nonStreamingTimeoutTimer *time.Timer
		var nonStreamingTimeoutC <-chan time.Time
		if !params.Stream && winner == 0 {
			wait := time.Until(requestStart.Add(nonStreamingNoContentTimeout))
			if wait < 0 {
				wait = 0
			}
			nonStreamingTimeoutTimer = time.NewTimer(wait)
			nonStreamingTimeoutC = nonStreamingTimeoutTimer.C
		}
		var stallInf *inflight
		var stallTimer *time.Timer
		var stallC <-chan time.Time
		if inf, deadline, ok := nextInterChunkStallTrigger(attempts); ok {
			wait := time.Until(deadline)
			if wait < 0 {
				wait = 0
			}
			stallInf = inf
			stallTimer = time.NewTimer(wait)
			stallC = stallTimer.C
		}
		var winnerHardTimeoutTimer *time.Timer
		var winnerHardTimeoutC <-chan time.Time
		if winner != 0 {
			if winning := inflightByNonce(attempts, winner); winning != nil {
				if deadline, ok := winnerHardTimeoutDeadline(winning); ok {
					wait := time.Until(deadline)
					if wait < 0 {
						wait = 0
					}
					winnerHardTimeoutTimer = time.NewTimer(wait)
					winnerHardTimeoutC = winnerHardTimeoutTimer.C
				}
			}
		}
		if allInflightsDone(attempts) && escalationC == nil {
			if !params.Stream && winner == 0 && time.Now().Before(requestStart.Add(nonStreamingNoContentTimeout)) {
				if !reducedMaxTokensFallbackStarted && time.Now().Before(requestStart.Add(nonStreamingReducedMaxTokensFallbackDelay)) && len(attempts) < maxAttempts {
					trigger := attempts[len(attempts)-1]
					trigger.escalated = true
					if next := e.startAdditionalInflight(streamCtx, settleCtx, race, params, "attempt_failed", trigger, "attempt_failed", triedParticipants, clientFlag); next != nil {
						attempts = append(attempts, next)
						e.watchInflightDone(next, doneCh)
					}
				}
				// Wait until the request-level no-content deadline so a reduced
				// max-token fallback can run even if earlier attempts ended empty.
			} else {
				if stallTimer != nil {
					stopTimer(stallTimer)
				}
				if winnerHardTimeoutTimer != nil {
					stopTimer(winnerHardTimeoutTimer)
				}
				if reducedFallbackTimer != nil {
					stopTimer(reducedFallbackTimer)
				}
				if nonStreamingTimeoutTimer != nil {
					stopTimer(nonStreamingTimeoutTimer)
				}
				if winner == 0 {
					if fallback := fallbackSuspiciousWinner(attempts); fallback != nil {
						if err := race.promoteFallbackWinner(fallback); err != nil {
							return err
						}
						winner = fallback.nonce
					}
				}
				return e.finishRaceOutcome(settleCtx, attempts, params, decision, winner, raceFinishOptions{recordFailureSamples: true})
			}
		}

		select {
		case inf := <-doneCh:
			if isErrorStreamAttempt(inf) {
				e.maybeRecordCapabilityError(inf)
				if total := parseContextTotalRequested(inf.errorMessage); total > params.ContextTotalHint {
					params.ContextTotalHint = total
				}
			}
			w := race.winnerNonce()
			if w != 0 && inf != nil && inf.nonce == w {
				if failed, err := e.winningInflightTerminalFailure(inf); failed {
					if e.shouldAttemptWinnerReconnect(inf) {
						// Run the ladder off the select loop so hard-timeout /
						// stall / escalation keep firing during resume.
						e.spawnWinnerReconnectLadder(streamCtx, settleCtx, inf, race, params, triedParticipants, clientFlag, doneCh, reconnectHedgeCh)
						break
					}
					if escalationTimer != nil {
						stopTimer(escalationTimer)
					}
					e.markPhaseTransitionAbort(inf)
					e.recordWinnerTerminalFailureOnce(inf, params, w)
					e.goTrackedRaceCleanup(func() {
						e.finishRaceWhenPendingDone(settleCtx, attempts, params, decision, w, raceFinishOptions{
							forceTreatAsFailure:  true,
							recordFailureSamples: true,
						})
					})
					logRequestStage(settleCtx, "winner_failed_after_content", "escrow", e.devshardID, "winner_nonce", w, "error", err)
					return err
				}
			}
			if w == 0 && e.markPhaseTransitionAbort(inf) && phaseTransitionAbortRetryable(inf) {
				e.reincludePhaseTransitionAbortParticipant(inf, triedParticipants)
				if len(attempts) < maxAttempts {
					if next := e.startAdditionalInflight(streamCtx, settleCtx, race, params, "phase_transition_retry", inf, "phase_transition_aborted", triedParticipants, clientFlag); next != nil {
						attempts = append(attempts, next)
						e.watchInflightDone(next, doneCh)
					}
				}
			}
		case <-escalationC:
			// Re-validate the trigger at fire time. Because the select does
			// not watch receiptCh / firstTokenCh directly, the attempt's
			// state may have advanced between scheduling the timer and it
			// firing (e.g. receipt arrived 400ms in, timer fired at 500ms).
			// In that case the ORIGINAL stage is stale — nextEscalationTrigger
			// would now return a later-stage trigger (or no trigger at all).
			// Escalating on stale info starts unnecessary secondaries: after
			// moving sendTime into the synchronous path this would affect
			// every primary that receipts-in under ReceiptTimeout, i.e. the
			// majority of a healthy run. Skip and let the loop re-schedule
			// the correct next trigger.
			current, stillValid := e.escalationForInflight(trigger.inf, params)
			if !stillValid || current.stage != trigger.stage {
				break
			}
			trigger.inf.escalated = true
			if len(attempts) < maxAttempts {
				if next := e.startAdditionalInflight(streamCtx, settleCtx, race, params, trigger.stage, trigger.inf, trigger.reason, triedParticipants, clientFlag); next != nil {
					attempts = append(attempts, next)
					e.watchInflightDone(next, doneCh)
				}
			}
		case <-reducedFallbackC:
			if reducedMaxTokensFallbackStarted || race.winnerNonce() != 0 {
				break
			}
			reducedMaxTokensFallbackStarted = true
			reducedParams, ok := reducedMaxTokensParams(params)
			if !ok {
				break
			}
			trigger := attempts[len(attempts)-1]
			trigger.escalated = true
			if next := e.startAdditionalInflight(streamCtx, settleCtx, race, reducedParams, "response_timeout_wait_elapsed", trigger, "response_timeout_reduced_max_tokens", triedParticipants, clientFlag); next != nil {
				next.excludePairwise = true
				attempts = append(attempts, next)
				e.watchInflightDone(next, doneCh)
			}
		case <-nonStreamingTimeoutC:
			if race.winnerNonce() != 0 {
				break
			}
			e.cancelPendingInflights(settleCtx, attempts, "non_stream_no_content_timeout")
			e.waitForInflightsDoneUntil(settleCtx, attempts, requestStart.Add(nonStreamingMaxAttemptWait))
			opts := raceFinishOptions{
				recordFailureSamples:            true,
				nonStreamingReducedTokenTimeout: true,
			}
			go func() {
				if err := e.finishRaceOutcome(settleCtx, attempts, params, decision, 0, opts); err != nil {
					var timeoutErr *nonStreamingReducedMaxTokensTimeoutError
					if errors.As(err, &timeoutErr) {
						return
					}
					logRequestStage(settleCtx, "background_finish_failed", "escrow", e.devshardID, "error", err)
				}
			}()
			return &nonStreamingReducedMaxTokensTimeoutError{}
		case <-stallC:
			now := time.Now()
			if stallInf == nil {
				break
			}
			deadline, stalled := interChunkStallDeadline(stallInf)
			if !stalled || now.Before(deadline) {
				break
			}
			rec, ok := stallInf.startInterChunkStall(now)
			if !ok {
				break
			}
			w := race.winnerNonce()
			role := "pending"
			if w == stallInf.nonce {
				role = "winner"
			} else if w != 0 {
				role = "loser"
			}
			stage := "attempt_inter_chunk_stall"
			if role == "winner" {
				stage = "winner_stalled_after_content"
			}
			logInferenceStage(settleCtx, stallInf.escrowID, stallInf.nonce, stage,
				"host", stallInf.hostID,
				"role", role,
				"winner_nonce", w,
				"stall_threshold_ms", InterChunkStallLogThreshold.Milliseconds(),
				"since_last_chunk_ms", now.Sub(rec.StartTime).Milliseconds(),
				"output_chunks_before_stall", rec.OutputChunksBefore,
				"content_chunks_before_stall", rec.ContentChunksBefore,
				"output_bytes_before_stall", rec.OutputBytesBefore,
			)
		case <-winnerHardTimeoutC:
			w := race.winnerNonce()
			winning := inflightByNonce(attempts, w)
			deadline, ok := winnerHardTimeoutDeadline(winning)
			if !ok || time.Now().Before(deadline) {
				break
			}
			winning.hardTimeoutLog.Do(func() {
				logInferenceStage(settleCtx, winning.escrowID, winning.nonce, "winner_stream_hard_timeout",
					"host", winning.hostID,
					"elapsed_ms", time.Since(winning.sendTime).Milliseconds(),
					"timeout_ms", StreamingAttemptHardTimeout.Milliseconds(),
					"output_chunks", winning.outputChunks.Load(),
					"content_chunks", winning.contentChunks.Load(),
					"output_bytes", winning.outputBytes.Load(),
				)
			})
			winning.cancelAttempt()
		case req := <-reconnectHedgeCh:
			if req.trigger != nil && streamCtx.Err() == nil && len(attempts) < maxAttempts {
				if next := e.startAdditionalInflight(streamCtx, settleCtx, race, params, "attempt_reconnect_budget", req.trigger, "winner_reconnect_hedge", triedParticipants, clientFlag); next != nil {
					attempts = append(attempts, next)
					e.watchInflightDone(next, doneCh)
					logRequestStage(settleCtx, "winner_reconnect_hedge_started",
						"escrow", e.devshardID,
						"winner_nonce", req.trigger.nonce,
						"hedge_nonce", next.nonce,
						"hedge_host", next.hostID,
					)
				}
			}
			if req.done != nil {
				close(req.done)
			}
		case <-winnerC:
		case <-streamCtx.Done():
			if escalationTimer != nil {
				stopTimer(escalationTimer)
			}
			if stallTimer != nil {
				stopTimer(stallTimer)
			}
			if winnerHardTimeoutTimer != nil {
				stopTimer(winnerHardTimeoutTimer)
			}
			pending := pendingInflights(attempts)
			logRequestStage(settleCtx, "request_stream_canceled", "escrow", e.devshardID, "winner_nonce", winner, "pending", len(pending), "decision", decision.Reason, "error", streamCtx.Err())
			e.goTrackedRaceCleanup(func() {
				e.finishRaceWhenPendingDone(settleCtx, attempts, params, decision, winner, raceFinishOptions{})
			})
			return streamCtx.Err()
		}

		if escalationTimer != nil {
			stopTimer(escalationTimer)
		}
		if reducedFallbackTimer != nil {
			stopTimer(reducedFallbackTimer)
		}
		if nonStreamingTimeoutTimer != nil {
			stopTimer(nonStreamingTimeoutTimer)
		}
		if stallTimer != nil {
			stopTimer(stallTimer)
		}
		if winnerHardTimeoutTimer != nil {
			stopTimer(winnerHardTimeoutTimer)
		}
	}
}

func (e *Redundancy) watchInflightDone(inf *inflight, doneCh chan<- *inflight) {
	go func() {
		<-inf.done
		doneCh <- inf
	}()
}

func (e *Redundancy) nextEscalationTrigger(attempts []*inflight, params user.InferenceParams) (escalationTrigger, bool) {
	var (
		chosen escalationTrigger
		ok     bool
	)
	for _, inf := range attempts {
		trigger, candidate := e.escalationForInflight(inf, params)
		if !candidate {
			continue
		}
		if !ok || trigger.deadline.Before(chosen.deadline) {
			chosen = trigger
			ok = true
		}
	}
	return chosen, ok
}

func (e *Redundancy) escalationForInflight(inf *inflight, params user.InferenceParams) (escalationTrigger, bool) {
	if inf == nil || inf.escalated {
		return escalationTrigger{}, false
	}
	if inf.suspicious {
		return escalationTrigger{
			inf:      inf,
			deadline: time.Now(),
			stage:    "suspicious_host_immediate_escalation",
			reason:   "suspicious_host",
		}, true
	}
	if inf.probe {
		return escalationTrigger{
			inf:      inf,
			deadline: time.Now(),
			stage:    "poc_probe_immediate_escalation",
			reason:   "poc_probe",
		}, true
	}
	if inflightDone(inf) {
		if inflightFinished(inf) {
			return escalationTrigger{}, false
		}
		if !params.Stream {
			return escalationTrigger{}, false
		}
		return escalationTrigger{
			inf:      inf,
			deadline: time.Now(),
			stage:    "attempt_failed",
			reason:   "attempt_failed",
		}, true
	}
	if inf.sendTime.IsZero() {
		return escalationTrigger{}, false
	}
	if !inf.hasReceipt() {
		return escalationTrigger{
			inf:      inf,
			deadline: inf.sendTime.Add(receiptTimeoutForInput(params.InputLength)),
			stage:    "receipt_timeout_wait_elapsed",
			reason:   "receipt_timeout",
		}, true
	}
	if !params.Stream {
		return escalationTrigger{}, false
	}
	if inf.hasFirstToken() {
		return escalationTrigger{}, false
	}
	return escalationTrigger{
		inf:      inf,
		deadline: inf.sendTime.Add(e.firstTokenFallbackDelay(params)),
		stage:    "first_token_timeout_wait_elapsed",
		reason:   "first_token_timeout",
	}, true
}

func (e *Redundancy) escalationDelay(stage string, params user.InferenceParams) time.Duration {
	switch stage {
	case "receipt_timeout_wait_elapsed":
		return receiptTimeoutForInput(params.InputLength)
	case "first_token_timeout_wait_elapsed":
		return e.firstTokenFallbackDelay(params)
	case "response_timeout_wait_elapsed":
		return nonStreamingFallbackDelay(params.InputLength)
	case "attempt_failed":
		return 0
	default:
		return 0
	}
}

func (e *Redundancy) monitorInflight(ctx context.Context, inf *inflight, race *raceGroup) {
	ticker := time.NewTicker(LogHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-inf.done:
			return
		case <-ticker.C:
			if inf.sendTime.IsZero() {
				continue
			}
			stage := "waiting_for_receipt"
			fields := []any{
				"host", inf.hostID,
				"elapsed_ms", time.Since(inf.sendTime).Milliseconds(),
				"output_chunks", inf.outputChunks.Load(),
			}
			if inf.hasReceipt() {
				stage = "waiting_for_first_token"
				fields = append(fields, "since_receipt_ms", time.Since(inf.receiptAt()).Milliseconds())
			}
			if inf.hasFirstToken() {
				stage = "streaming_inflight"
				fields = append(fields, "since_first_token_ms", time.Since(inf.firstTokenAt()).Milliseconds())
				if lastChunkAt := inf.lastChunkAt.Load(); lastChunkAt > 0 {
					fields = append(fields, "since_last_chunk_ms", time.Since(time.Unix(0, lastChunkAt)).Milliseconds())
				}
				winnerNonce := race.winnerNonce()
				role := "unknown"
				if winnerNonce == inf.nonce {
					role = "winner"
				} else if winnerNonce != 0 {
					role = "loser"
				}
				fields = append(fields, "role", role, "winner_nonce", winnerNonce)
			}
			logInferenceStage(ctx, inf.escrowID, inf.nonce, stage, fields...)
		case <-ctx.Done():
			return
		}
	}
}

type raceFinishOptions struct {
	forceTreatAsFailure             bool
	recordFailureSamples            bool
	nonStreamingReducedTokenTimeout bool
}

// goTrackedRaceCleanup runs a background race cleanup detached while keeping the drain barrier aware of it; onRaceCleanupStart fires synchronously so the winning handler can never see the runtime as quiet mid-cleanup.
func (e *Redundancy) goTrackedRaceCleanup(fn func()) {
	if e.onRaceCleanupStart != nil {
		e.onRaceCleanupStart()
	}
	go func() {
		if e.onRaceCleanupDone != nil {
			defer e.onRaceCleanupDone()
		}
		fn()
	}()
}

func (e *Redundancy) isSuspiciousParticipant(participantKey string) bool {
	return e != nil && e.suspiciousParticipant != nil && e.suspiciousParticipant(strings.TrimSpace(participantKey))
}

func (e *Redundancy) noWinnerStatusForParticipant(participantKey string) (participantNoWinnerStatus, bool) {
	participantKey = strings.TrimSpace(participantKey)
	if participantKey == "" || e == nil {
		return participantNoWinnerStatus{}, false
	}
	if e.isSuspiciousParticipant(participantKey) {
		return participantNoWinnerStatus{reason: "manual_suspicious"}, true
	}
	if e.participantLimiter == nil {
		return participantNoWinnerStatus{}, false
	}
	return e.participantLimiter.NoWinnerStatusForModel(participantKey, e.model)
}

func (e *Redundancy) isNoWinnerParticipant(participantKey string) bool {
	if e == nil {
		return false
	}
	_, ok := e.noWinnerStatusForParticipant(participantKey)
	return ok
}

func (e *Redundancy) quarantineModeForParticipant(participantKey string) string {
	if e == nil || participantKey == "" {
		return "none"
	}
	if status, ok := e.noWinnerStatusForParticipant(participantKey); ok {
		if status.quarantineMode != "" {
			return status.quarantineMode
		}
		return "probation"
	}
	if e.participantLimiter != nil && e.participantLimiter.IsBlockedForModel(participantKey, e.model) {
		return participantQuarantineProbe.String()
	}
	return "none"
}

func gatewayMetricModel(params user.InferenceParams, fallback string) string {
	if params.Model != "" {
		return params.Model
	}
	return fallback
}

func gatewayAttemptRole(inf *inflight) string {
	if inf == nil || strings.TrimSpace(inf.role) == "" {
		return "primary"
	}
	return inf.role
}

func gatewayAttemptStartReason(inf *inflight) string {
	if inf == nil || strings.TrimSpace(inf.startReason) == "" {
		return "primary"
	}
	return inf.startReason
}

func gatewayRequestFailureReason(failed []*inflight) string {
	for _, inf := range failed {
		if inf != nil && !inf.probe {
			return gatewayAttemptFailureReason(inf, nil)
		}
	}
	return "unknown"
}

func timeoutKindForInflight(inf *inflight) string {
	if inf == nil {
		return "unknown"
	}
	if !inf.hasReceipt() {
		return "refused"
	}
	return "execution"
}

func timeoutResultKind(result user.TimeoutResult, inf *inflight) string {
	switch result.Reason {
	case "refused", "execution":
		return result.Reason
	default:
		return timeoutKindForInflight(inf)
	}
}

// gatewayTimeoutFailureAction maps a failed HandleTimeout to a metric/accounting
// action and reason. Applied-but-delivery-failed still counts as completed.
func gatewayTimeoutFailureAction(result user.TimeoutResult) (string, string) {
	if result.Applied {
		return "completed", firstNonEmpty(result.DetailReason, "delivery_failed")
	}
	if result.Outcome == "skipped" {
		return "skipped", firstNonEmpty(result.DetailReason, "unknown")
	}
	return "failed", firstNonEmpty(result.Outcome, "unknown")
}

func (e *Redundancy) recordGatewayRequestOutcome(model, outcome, reason string) {
	if e == nil || e.metrics == nil {
		return
	}
	e.metrics.RecordGatewayRequest(model, outcome, reason)
	if outcome == "failed" {
		e.metrics.RecordCriticalUserFailure(model, reason)
	}
}

func (e *Redundancy) recordGatewayAttemptStarted(inf *inflight, params user.InferenceParams) {
	if e == nil || inf == nil || inf.probe {
		return
	}
	participantKey := e.participantKeyForHost(inf.hostIdx)
	model := gatewayMetricModel(params, e.model)
	role := gatewayAttemptRole(inf)
	reason := gatewayAttemptStartReason(inf)
	quarantineMode := e.quarantineModeForParticipant(participantKey)
	e.accounting.RealSend(inf.escrowID, inf.nonce, inf.sendTime, quarantineMode)
	if e.metrics == nil {
		return
	}
	e.metrics.RecordGatewaySlotDecision(GatewaySlotDecisionMetric{
		ParticipantKey: participantKey,
		Model:          model,
		EscrowID:       inf.escrowID,
		Decision:       "real_send",
		Reason:         reason,
		QuarantineMode: quarantineMode,
	})
	e.metrics.RecordGatewayAttemptStarted(GatewayAttemptStartMetric{
		ParticipantKey: participantKey,
		Model:          model,
		Role:           role,
		Reason:         reason,
		QuarantineMode: quarantineMode,
	})
	if inf.suspicious {
		e.metrics.RecordGatewayNoWinnerAttempt(participantKey, model, inf.noWinnerReason, inf.noWinnerQuarantineMode)
	}
}

func (e *Redundancy) recordGatewayAttemptTerminal(inf *inflight, params user.InferenceParams, winnerNonce uint64, ok bool) {
	if e == nil || inf == nil || inf.probe {
		return
	}
	e.accounting.Usage(inf.escrowID, inf.nonce, winnerNonce)
	if e.metrics == nil {
		return
	}
	outcome := "success"
	if !ok {
		outcome = "failed"
	}
	participantKey := e.participantKeyForHost(inf.hostIdx)
	model := gatewayMetricModel(params, e.model)
	visibility := gatewayAttemptVisibility(inf, winnerNonce, ok)
	role := gatewayAttemptRole(inf)
	e.metrics.RecordGatewayAttemptTerminal(GatewayAttemptTerminalMetric{
		ParticipantKey: participantKey,
		Model:          model,
		Role:           role,
		Outcome:        outcome,
		Visibility:     visibility,
	})
	if !ok {
		e.metrics.RecordGatewayAttemptFailure(GatewayAttemptFailureMetric{
			ParticipantKey: participantKey,
			Model:          model,
			Role:           role,
			Reason:         gatewayAttemptFailureReason(inf, e.session),
			Visibility:     visibility,
		})
	}
}

func (e *Redundancy) recordGatewayUserVisibleWin(attempts []*inflight, params user.InferenceParams, winnerNonce uint64) {
	if e == nil || e.metrics == nil || winnerNonce == 0 {
		return
	}
	if winner := inflightByNonce(attempts, winnerNonce); winner != nil && !winner.probe {
		e.metrics.RecordGatewayUserVisibleWin(e.participantKeyForHost(winner.hostIdx), gatewayMetricModel(params, e.model))
	}
}

func (e *Redundancy) recordGatewayHiddenFailure(model string, failed []*inflight) {
	if e == nil || e.metrics == nil || len(failed) == 0 {
		return
	}
	for _, inf := range failed {
		if inf == nil || inf.probe {
			continue
		}
		e.metrics.RecordGatewayHiddenFailure(model, "protected", gatewayAttemptFailureReason(inf, e.session))
		return
	}
}

func (e *Redundancy) recordGatewayTimeoutAction(inf *inflight, params user.InferenceParams, kind, action, reason string, detailReasons ...string) {
	if e == nil || inf == nil || inf.probe {
		return
	}
	detailReason := gatewayAttemptFailureReason(inf, e.session)
	timeoutReason := reason
	if len(detailReasons) > 0 && detailReasons[0] != "" {
		timeoutReason = detailReasons[0]
	}
	e.accounting.TimeoutResult(inf.escrowID, inf.nonce, kind, action, reason, detailReason, timeoutReason)
	if e.metrics == nil {
		return
	}
	e.metrics.RecordGatewayTimeoutAction(GatewayTimeoutActionMetric{
		ParticipantKey: e.participantKeyForHost(inf.hostIdx),
		Model:          gatewayMetricModel(params, e.model),
		Kind:           kind,
		Action:         action,
		Reason:         reason,
	})
}

func (e *Redundancy) finishRaceWhenPendingDone(ctx context.Context, attempts []*inflight, params user.InferenceParams, decision Decision, winnerNonce uint64, opts raceFinishOptions) {
	bgCtx, _ := ensureRequestLogContext(context.Background())
	bgCtx = logging.PropagateRequestID(bgCtx, ctx)

	e.waitForPendingLosers(bgCtx, winnerNonce, attempts)

	if err := e.finishRaceOutcome(bgCtx, attempts, params, decision, winnerNonce, opts); err != nil {
		logRequestStage(bgCtx, "background_race_finalize_failed", "escrow", e.devshardID, "error", err)
	}
}

func (e *Redundancy) finishStalledWinnerAfterClientTimeout(ctx context.Context, attempts []*inflight, params user.InferenceParams, decision Decision, winnerNonce uint64) {
	bgCtx, _ := ensureRequestLogContext(context.Background())
	bgCtx = logging.PropagateRequestID(bgCtx, ctx)

	winner := inflightByNonce(attempts, winnerNonce)
	abandonedWinner := e.waitForClientTimedOutAttempts(bgCtx, winnerNonce, attempts)
	if abandonedWinner {
		e.recordStalledWinnerFailureOnce(winner, params)
		if err := e.finishRaceOutcome(bgCtx, attempts, params, decision, winnerNonce, raceFinishOptions{
			forceTreatAsFailure: true,
		}); err != nil {
			logRequestStage(bgCtx, "background_stalled_winner_finalize_failed", "escrow", e.devshardID, "error", err)
		}
		return
	}

	if winner != nil && winner.err == nil && inflightFinished(winner) {
		logInferenceStage(bgCtx, winner.escrowID, winner.nonce, "winner_completed_after_client_timeout",
			"host", winner.hostID,
			"output_chunks", winner.outputChunks.Load(),
			"content_chunks", winner.contentChunks.Load(),
			"output_bytes", winner.outputBytes.Load(),
		)
	}
	if err := e.finishRaceOutcome(bgCtx, attempts, params, decision, winnerNonce, raceFinishOptions{
		recordFailureSamples: true,
	}); err != nil {
		logRequestStage(bgCtx, "background_stalled_winner_finalize_failed", "escrow", e.devshardID, "error", err)
	}
}

func (e *Redundancy) waitForClientTimedOutAttempts(ctx context.Context, winnerNonce uint64, attempts []*inflight) bool {
	pending := pendingInflights(attempts)
	if len(pending) == 0 {
		return false
	}

	timer := time.NewTimer(SecondaryWaitAfterWinner)
	defer stopTimer(timer)

	naturalDone := make(chan *inflight, len(pending))
	for _, inf := range pending {
		inf := inf
		go func() {
			waitInflightSettled(inf)
			naturalDone <- inf
		}()
	}

	abandonedWinner := false
	remaining := len(pending)
	for remaining > 0 {
		select {
		case <-naturalDone:
			remaining--
		case <-timer.C:
			still := pendingInflights(attempts)
			logRequestStage(ctx, "client_timeout_wait_abandoned",
				"escrow", e.devshardID,
				"winner_nonce", winnerNonce,
				"pending", len(still),
				"wait_ms", SecondaryWaitAfterWinner.Milliseconds(),
			)
			for _, inf := range still {
				reason := "client_timeout_grace_expired"
				stage := "speculative_attempt_canceled"
				if inf.nonce == winnerNonce {
					stage = "stalled_winner_canceled_after_client_timeout"
					abandonedWinner = true
				}
				logInferenceStage(ctx, inf.escrowID, inf.nonce, stage,
					"host", inf.hostID,
					"reason", reason,
				)
				inf.cancelAttempt()
			}
			for remaining > 0 {
				<-naturalDone
				remaining--
			}
			return abandonedWinner
		}
	}
	return false
}

func (e *Redundancy) cancelPendingInflights(ctx context.Context, attempts []*inflight, reason string) {
	for _, inf := range pendingInflights(attempts) {
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "speculative_attempt_canceled",
			"host", inf.hostID,
			"reason", reason,
		)
		inf.cancelAttempt()
	}
}

func (e *Redundancy) waitForInflightsDoneUntil(ctx context.Context, attempts []*inflight, deadline time.Time) {
	pending := pendingInflights(attempts)
	if len(pending) == 0 {
		return
	}
	done := make(chan struct{}, len(pending))
	for _, inf := range pending {
		inf := inf
		go func() {
			waitInflightSettled(inf)
			done <- struct{}{}
		}()
	}
	remaining := len(pending)
	for remaining > 0 {
		wait := time.Until(deadline)
		if wait <= 0 {
			logRequestStage(ctx, "non_stream_attempt_wait_limit_reached",
				"escrow", e.devshardID,
				"pending", remaining,
				"wait_limit_ms", nonStreamingMaxAttemptWait.Milliseconds(),
			)
			return
		}
		timer := time.NewTimer(wait)
		select {
		case <-done:
			stopTimer(timer)
			remaining--
		case <-timer.C:
			logRequestStage(ctx, "non_stream_attempt_wait_limit_reached",
				"escrow", e.devshardID,
				"pending", remaining,
				"wait_limit_ms", nonStreamingMaxAttemptWait.Milliseconds(),
			)
			return
		}
	}
}

// waitForPendingLosers waits for all not-yet-done attempts to close their done
// channel, giving them at most SecondaryWaitAfterWinner to finish naturally.
// Anything still running at the deadline has its per-attempt context cancelled
// so SendOnly unwinds, and we drain the resulting done signals before
// returning. Callers rely on this drain so finishRaceOutcome sees stable
// inf.resp/inf.err state before invoking ProcessResponse / HandleTimeout.
func (e *Redundancy) waitForPendingLosers(ctx context.Context, winnerNonce uint64, attempts []*inflight) {
	pending := pendingInflights(attempts)
	if len(pending) == 0 {
		return
	}

	timer := time.NewTimer(SecondaryWaitAfterWinner)
	defer stopTimer(timer)

	naturalDone := make(chan *inflight, len(pending))
	for _, inf := range pending {
		inf := inf
		go func() {
			waitInflightSettled(inf)
			naturalDone <- inf
		}()
	}

	remaining := len(pending)
	for remaining > 0 {
		select {
		case <-naturalDone:
			remaining--
		case <-timer.C:
			still := pendingInflights(attempts)
			logRequestStage(ctx, "speculative_wait_abandoned",
				"escrow", e.devshardID,
				"winner_nonce", winnerNonce,
				"pending", len(still),
				"wait_ms", SecondaryWaitAfterWinner.Milliseconds(),
			)
			for _, inf := range still {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "speculative_attempt_canceled",
					"host", inf.hostID,
					"reason", "winner_grace_expired",
				)
				inf.cancelAttempt()
			}
			// Drain the remaining signals. SendOnly honors ctx cancellation,
			// so these should arrive promptly; the wait is unbounded so a
			// hung transport leaks its own goroutine rather than corrupting
			// finalization with a concurrent write to inf.resp/inf.err.
			for remaining > 0 {
				<-naturalDone
				remaining--
			}
			return
		}
	}
}

func pendingInflights(attempts []*inflight) []*inflight {
	var pending []*inflight
	for _, inf := range attempts {
		if !inflightDone(inf) {
			pending = append(pending, inf)
		}
	}
	return pending
}

func allInflightsDone(attempts []*inflight) bool {
	for _, inf := range attempts {
		if !inflightDone(inf) {
			return false
		}
	}
	return true
}

func inflightDone(inf *inflight) bool {
	// A resuming attempt has a closed done channel but is still streaming
	// through the same-nonce reconnect ladder.
	if inf.resuming.Load() {
		return false
	}
	select {
	case <-inf.done:
		return true
	default:
		return false
	}
}

// waitInflightSettled blocks until the attempt's transport goroutine and any
// same-nonce reconnect ladder have exited, so inf.resp / inf.err are stable
// for finalization.
func waitInflightSettled(inf *inflight) {
	<-inf.done
	inf.resumeMu.Lock()
	resumeDone := inf.resumeDone
	inf.resumeMu.Unlock()
	if resumeDone != nil {
		<-resumeDone
	}
}

// shouldRunHandleTimeout reports whether HandleTimeout should be invoked for a
// failed attempt. Some empty-stream attempts still post MsgFinishInference, but
// others can fail before that finish marker exists, so the protocol outcome is
// the only safe gate for timeout voting.
func shouldRunHandleTimeout(inf *inflight, session *user.Session) bool {
	if inf == nil || session == nil {
		return false
	}
	if inf.probe {
		return false
	}
	return !session.IsNonceFinished(inf.nonce)
}

func emptyStreamWithoutWinnerTimeoutSkipReason(inf *inflight, session *user.Session) (string, bool) {
	if session != nil && isEmptyStreamAttempt(inf) && session.IsNonceFinished(inf.nonce) {
		return "empty_stream_without_non_empty_winner", true
	}
	return "", false
}

func longResponseFailureExempt(inf *inflight, session *user.Session) bool {
	if inf == nil || session == nil || inf.probe || inf.sendTime.IsZero() {
		return false
	}
	if session.IsNonceFinished(inf.nonce) {
		return false
	}
	if inf.contentChunks.Load() == 0 {
		return false
	}
	return time.Since(inf.sendTime) >= longResponseFailureExemption
}

func (e *Redundancy) longResponseFailureExempt(inf *inflight) bool {
	if e == nil {
		return false
	}
	return longResponseFailureExempt(inf, e.session)
}

func longNonStreamEmptyFailureExempt(inf *inflight, params user.InferenceParams) bool {
	if inf == nil || inf.probe || params.Stream || inf.sendTime.IsZero() {
		return false
	}
	if !isEmptyStreamAttempt(inf) {
		return false
	}
	return time.Since(inf.sendTime) >= longResponseFailureExemption
}

func attemptCountsAsSuccessfulForPerf(inf *inflight, params user.InferenceParams, session *user.Session) bool {
	if inf == nil {
		return false
	}
	if longNonStreamEmptyFailureExempt(inf, params) {
		return true
	}
	return inf.resp != nil && inf.resp.ConfirmedAt > 0 && !isEmptyStreamAttempt(inf) && session != nil && session.IsNonceFinished(inf.nonce)
}

func isFailedStreamAttempt(inf *inflight) bool {
	return isEmptyStreamAttempt(inf) || isErrorStreamAttempt(inf)
}

func (e *Redundancy) markPhaseTransitionAbort(inf *inflight) bool {
	if inf == nil || inf.probe || inf.phaseTransitionAborted {
		return inf != nil && inf.phaseTransitionAborted
	}
	if !inf.startedBeforePoCGeneration || !currentPoCGenerationActive() {
		return false
	}
	if isErrorStreamAttempt(inf) || e.attemptHasMsgFinish(inf) {
		return false
	}
	if !isEmptyStreamAttempt(inf) && inf.err == nil && inf.contentChunks.Load() == 0 {
		return false
	}
	inf.phaseTransitionAborted = true
	inf.excludePairwise = true
	return true
}

func (e *Redundancy) attemptHasMsgFinish(inf *inflight) bool {
	if inf == nil {
		return false
	}
	if inf.resp != nil && user.HasMsgFinish(inf.resp.Mempool, inf.nonce) {
		return true
	}
	return e != nil && e.session != nil && e.session.IsNonceFinished(inf.nonce)
}

func phaseTransitionAbortRetryable(inf *inflight) bool {
	return inf != nil && inf.phaseTransitionAborted && inf.contentChunks.Load() == 0
}

func (e *Redundancy) reincludePhaseTransitionAbortParticipant(inf *inflight, triedParticipants map[string]bool) {
	if e == nil || e.session == nil || inf == nil || !inf.phaseTransitionAborted || triedParticipants == nil {
		return
	}
	delete(triedParticipants, e.session.HostParticipantKey(inf.hostIdx))
}

func isErrorStreamAttempt(inf *inflight) bool {
	return inf != nil && inf.errorSource != ""
}

func hostApplicationErrorFromInflight(inf *inflight) *hostApplicationError {
	if !isErrorStreamAttempt(inf) {
		return nil
	}
	details, payload, ok := sseChunkErrorPayload(inf.errorBodySample)
	if !ok {
		details = sseErrorDetails{
			Code:    inf.errorCode,
			Type:    inf.errorType,
			Message: inf.errorMessage,
		}
	}
	return &hostApplicationError{details: details, payload: payload}
}

func hostApplicationErrorFromAttempts(attempts []*inflight, winnerNonce uint64) *hostApplicationError {
	if winner := inflightByNonce(attempts, winnerNonce); winner != nil {
		if err := hostApplicationErrorFromInflight(winner); err != nil {
			return err
		}
	}
	for _, inf := range attempts {
		if err := hostApplicationErrorFromInflight(inf); err != nil {
			return err
		}
	}
	return nil
}

// inflightFinished checks the raw response for MsgFinishInference.
// Used during the race loop before ProcessResponse has been called.
// Non-probe attempts that completed the chain protocol but produced no
// content (empty SSE, or stalled with no first-token) are treated as
// failed so redundancy can retry on a different host.
func inflightFinished(inf *inflight) bool {
	if inf.err != nil || inf.resp == nil {
		return false
	}
	if isFailedStreamAttempt(inf) {
		return false
	}
	return user.HasMsgFinish(inf.resp.Mempool, inf.nonce)
}

// isEmptyStreamAttempt reports whether a non-probe attempt that confirmed
// receipt failed to deliver any content. This covers two patterns:
//
//   - Empty SSE: bytes were streamed but no content events parsed (role
//     marker + [DONE] only). Caught by contentChunks == 0.
//   - Stall: receipt came back fast, then the host went silent for the
//     full deadline before completing the chain protocol with zero output.
//     Same condition: contentChunks == 0.
//
// We gate on receiptTime being set so attempts that never even confirmed
// receipt fall through to the upstream error/timeout path instead.
func isEmptyStreamAttempt(inf *inflight) bool {
	if inf == nil || inf.probe {
		return false
	}
	if !inf.hasReceipt() {
		return false
	}
	if isErrorStreamAttempt(inf) {
		return false
	}
	return inf.contentChunks.Load() == 0
}

// isModelBurnEmpty: empty stream where the model generated tokens that vLLM
// stripped (e.g. </think> at small max_tokens). Documented reasoning outcome,
// not a host fault — must not penalize. Scoped to the reasoning route: the
// completion_tokens signal is host-reported, so honoring it on non-reasoning
// models would let any host dodge empty-stream quarantine by faking usage.
func isModelBurnEmpty(inf *inflight, model string) bool {
	if model != kimiK26ModelID || !isEmptyStreamAttempt(inf) {
		return false
	}
	return inf.usageComplTokens.Load() > 0
}

func fallbackSuspiciousWinner(attempts []*inflight) *inflight {
	for _, inf := range attempts {
		if inf == nil || inf.probe || !inf.suspicious {
			continue
		}
		if inf.contentChunks.Load() > 0 && !isFailedStreamAttempt(inf) {
			return inf
		}
	}
	return nil
}

func emptyStreamAccountingSuppressedByPoC() bool {
	return relaxedPoCBypassActive()
}

func inflightByNonce(attempts []*inflight, nonce uint64) *inflight {
	for _, inf := range attempts {
		if inf.nonce == nonce {
			return inf
		}
	}
	return nil
}

func (e *Redundancy) recordSampleOnce(inf *inflight, params user.InferenceParams, requestSucceeded bool) {
	if isErrorStreamAttempt(inf) {
		e.maybeRecordCapabilityError(inf)
		return
	}
	if inf != nil && errors.Is(inf.processErr, types.ErrStateHashMismatch) {
		return
	}
	if e.longResponseFailureExempt(inf) {
		return
	}
	inf.sampleOnce.Do(func() {
		e.recordSample(inf, params, requestSucceeded)
	})
}

func (e *Redundancy) maybeRecordCapabilityError(inf *inflight) {
	if inf == nil || inf.probe || inf.errorMessage == "" || e.perf == nil {
		return
	}
	participantKey := e.participantKeyForHost(inf.hostIdx)
	if isToolChoiceCapabilityError(inf.errorMessage) {
		e.perf.RecordToolUnsupported(participantKey)
		return
	}
	maxTokens := parseContextLengthLimit(inf.errorMessage)
	if maxTokens == 0 {
		return
	}
	e.perf.RecordContextLimit(participantKey, maxTokens)
}

func (e *Redundancy) capabilityBlocked(participantKey string, params user.InferenceParams) (string, bool) {
	if reason, blocked := e.escrowStateBlockReason(participantKey); blocked {
		return reason, true
	}
	if e == nil || e.perf == nil {
		return "", false
	}
	return e.perf.HostCannotServeRequest(participantKey, params)
}

func (e *Redundancy) maybeRecordEscrowStateDivergence(ctx context.Context, inf *inflight, err error) {
	if e == nil || inf == nil || inf.probe || !isStateRootDivergenceError(err) {
		return
	}
	participantKey := e.participantKeyForHost(inf.hostIdx)
	if participantKey == "" {
		return
	}
	const reason = "escrow_state_root_diverged"
	e.stateBlockMu.Lock()
	if e.stateBlockedHosts == nil {
		e.stateBlockedHosts = make(map[string]string)
	}
	_, existed := e.stateBlockedHosts[participantKey]
	e.stateBlockedHosts[participantKey] = reason
	e.stateBlockMu.Unlock()
	if !existed {
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "escrow_participant_state_blocked",
			"host", inf.hostID,
			"participant_key", participantKey,
			"reason", reason,
			"error", err,
		)
	}
}

func (e *Redundancy) escrowStateBlockReason(participantKey string) (string, bool) {
	if e == nil || participantKey == "" {
		return "", false
	}
	e.stateBlockMu.RLock()
	defer e.stateBlockMu.RUnlock()
	reason, blocked := e.stateBlockedHosts[participantKey]
	return reason, blocked
}

func isStateRootDivergenceError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "apply diff nonce") &&
		strings.Contains(msg, "post_state_root does not match computed state root")
}

func isRetriableCapabilityErrorMessage(msg string) bool {
	return isToolChoiceCapabilityError(msg) || parseContextLengthLimit(msg) > 0
}

func isToolChoiceCapabilityError(msg string) bool {
	return strings.Contains(msg, toolChoiceUnsupportedMessage)
}

func (e *Redundancy) knownCapabilityExhaustionError(params user.InferenceParams, err error) *hostApplicationError {
	if e == nil || e.perf == nil || !errors.Is(err, ErrNoAvailableHost) || !requestRequiresTools(params) {
		return nil
	}
	if !e.perf.AllKnownToolUnsupported(e.session.ParticipantKeys()) {
		return nil
	}
	return &hostApplicationError{
		details: sseErrorDetails{
			Code:    strconv.Itoa(http.StatusBadRequest),
			Type:    "BadRequestError",
			Message: toolChoiceUnsupportedMessage,
		},
	}
}

func requestRequiresTools(params user.InferenceParams) bool {
	var raw map[string]any
	if err := json.Unmarshal(params.Prompt, &raw); err != nil {
		return false
	}
	if tools, ok := raw["tools"].([]any); ok && len(tools) > 0 {
		return true
	}
	if choice, ok := raw["tool_choice"]; ok && choice != nil {
		if s, ok := choice.(string); ok && strings.EqualFold(s, "none") {
			return false
		}
		return true
	}
	return false
}

// parseContextLengthLimit extracts the maximum context length from an error
// message like "maximum context length is 131072 tokens" or
// "This model's maximum context length is 120000 tokens".
func parseContextLengthLimit(msg string) uint64 {
	return parseUintAfterMarker(msg, "maximum context length is ")
}

func parseContextTotalRequested(msg string) uint64 {
	return parseUintAfterMarker(msg, "for a total of at least ")
}

func parseUintAfterMarker(msg, marker string) uint64 {
	lower := strings.ToLower(msg)
	idx := strings.Index(lower, marker)
	if idx < 0 {
		return 0
	}
	rest := msg[idx+len(marker):]
	end := strings.IndexFunc(rest, func(r rune) bool {
		return r < '0' || r > '9'
	})
	if end <= 0 {
		return 0
	}
	n, err := strconv.ParseUint(rest[:end], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (e *Redundancy) recordStartedAttemptSamples(attempts []*inflight, params user.InferenceParams, requestSucceeded bool) {
	for _, inf := range attempts {
		if inf == nil || inf.probe || inf.sendTime.IsZero() {
			continue
		}
		if inf.phaseTransitionAborted {
			continue
		}
		e.recordSampleOnce(inf, params, requestSucceeded)
	}
}

func (e *Redundancy) recordStalledWinnerFailureOnce(inf *inflight, params user.InferenceParams) {
	if inf == nil {
		return
	}
	if !inf.hasRecordedStall() {
		return
	}
	e.recordPostContentWinnerFailureOnce(inf, params)
}

func (e *Redundancy) recordPostContentWinnerFailureOnce(inf *inflight, params user.InferenceParams) {
	if inf == nil {
		return
	}
	if inf.phaseTransitionAborted {
		return
	}
	if isErrorStreamAttempt(inf) {
		// TODO: Hosts should submit MsgFinishInference for model/client error
		// responses too. Until that is fixed across hosts, do not punish a
		// participant that returned a valid OpenAI-style error just because the
		// nonce did not finish. Restore normal stalled-winner accounting here
		// once error responses reliably finish on-chain.
		return
	}
	if e.longResponseFailureExempt(inf) {
		return
	}
	if longNonStreamEmptyFailureExempt(inf, params) {
		return
	}
	inf.sampleOnce.Do(func() {
		participantKey := e.participantKeyForHost(inf.hostIdx)
		sample := RequestSample{
			HostIdx:        inf.hostIdx,
			ParticipantKey: participantKey,
			Model:          normalizeModelID(params.Model),
			Responsive:     false,
			SendTime:       inf.sendTime,
			ReceiptTime:    inf.receiptAt(),
			FirstToken:     inf.firstTokenAt(),
			InputTokens:    params.InputLength,
		}
		if !inf.sendTime.IsZero() {
			sample.TotalTime = time.Since(inf.sendTime)
		}
		e.perf.Record(sample)
		if e.participantLimiter != nil && e.perf.ParticipantFailureThresholdExceeded(participantKey) {
			e.participantLimiter.ObserveStalledWinner(participantKey)
		}
		if e.metrics != nil {
			e.metrics.ObserveRequestSample(e.devshardID, sample)
		}
	})
}

func (e *Redundancy) recordWinnerTerminalFailureOnce(inf *inflight, params user.InferenceParams, winnerNonce uint64) {
	if inf == nil || inf.probe || inf.nonce != winnerNonce {
		return
	}
	if inf.contentChunks.Load() == 0 {
		return
	}
	if e.longResponseFailureExempt(inf) {
		return
	}
	if !inf.hasRecordedStall() && (inf.err != nil || inf.processErr != nil) {
		return
	}
	e.recordPostContentWinnerFailureOnce(inf, params)
}

func (e *Redundancy) processInflightOnce(inf *inflight) error {
	if inf == nil {
		return nil
	}
	// Do not consume processOnce on a nil resp — a later same-nonce reconnect
	// may merge a receipt/mempool that still needs a single ProcessResponse.
	if inf.resp == nil {
		return inf.processErr
	}
	inf.processOnce.Do(func() {
		inf.processErr = e.session.ProcessResponse(inf.hostIdx, inf.resp, inf.nonce)
	})
	return inf.processErr
}

// finishRaceOutcome aggregates attempt outcomes and returns a user-visible
// error when no non-probe attempt fully succeeded. When forceTreatAsFailure
// is true (winner failed after content while other inflights were still
// running), the request is always settled as a failure even if another
// attempt later completes successfully on the protocol layer.
func (e *Redundancy) finishRaceOutcome(ctx context.Context, attempts []*inflight, params user.InferenceParams, decision Decision, winnerNonce uint64, opts raceFinishOptions) error {
	// Process all responses first so Session has complete protocol state.
	for _, inf := range attempts {
		if err := e.processInflightOnce(inf); err != nil {
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "process_response_failed", "host", inf.hostID, "error", err)
			if errors.Is(err, types.ErrStateHashMismatch) {
				e.maybeRecordEscrowStateDivergence(ctx, inf, fmt.Errorf("apply diff nonce %d: post_state_root does not match computed state root: %w", inf.nonce, err))
			}
		}
	}

	winnerNonce = e.resolvedWinnerNonce(attempts, winnerNonce)
	var winnerIdx int
	if len(attempts) > 0 {
		winnerIdx = attempts[0].hostIdx
	}
	if winner := inflightByNonce(attempts, winnerNonce); winner != nil {
		winnerIdx = winner.hostIdx
	}
	if opts.nonStreamingReducedTokenTimeout {
		for _, inf := range attempts {
			if inf.excludePairwise {
				inf.escalated = true
			}
		}
	}

	var (
		anySucceeded bool
		failed       []*inflight
	)
	for _, inf := range attempts {
		finishedAt := time.Now()
		inf.finishActiveStall(finishedAt)
		nonceFinished := e.session.IsNonceFinished(inf.nonce)
		// A successful attempt must finalise the protocol nonce AND must
		// not be an empty stream (streamed bytes with no content). Attempts
		// that never streamed at all (e.g. in-process clients) still count
		// as successful purely on the protocol-level finish.
		ok := nonceFinished && !isFailedStreamAttempt(inf)
		if !inf.probe {
			anySucceeded = anySucceeded || ok
		}
		streamBytes := int64(0)
		if inf.resp != nil {
			streamBytes = inf.resp.StreamBytesRead
		}
		var confirmedAt int64
		var hasReceipt bool
		if inf.resp != nil {
			confirmedAt = inf.resp.ConfirmedAt
			hasReceipt = len(inf.resp.Receipt) > 0
		}
		fields := []any{
			"host", inf.hostID,
			"winner", inf.nonce == winnerNonce,
			"finished", ok,
			"responsive", confirmedAt > 0,
			"has_receipt", hasReceipt,
			"confirmed_at", confirmedAt,
			"output_chunks", inf.outputChunks.Load(),
			"content_chunks", inf.contentChunks.Load(),
			"output_bytes", inf.outputBytes.Load(),
			"stream_bytes_read", streamBytes,
			"content_source", inf.contentSource,
			"error_source", inf.errorSource,
			"probe", inf.probe,
			"suspicious", inf.suspicious,
		}
		fields = append(fields, inf.stallLogFields(finishedAt)...)
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "race_completed", fields...)
		e.recordGatewayAttemptTerminal(inf, params, winnerNonce, ok)
		if !ok {
			e.recordWinnerTerminalFailureOnce(inf, params, winnerNonce)
			failed = append(failed, inf)
		}
	}
	captureEmptyStreamAttemptRequest(ctx, e.devshardID, params, attempts, winnerNonce)
	captureShortContentAttemptRequest(ctx, e.devshardID, params, attempts, winnerNonce)
	effectiveSuccess := anySucceeded && !opts.forceTreatAsFailure
	if !effectiveSuccess {
		if opts.recordFailureSamples {
			e.recordStartedAttemptSamples(attempts, params, false)
		}
		for _, inf := range failed {
			if inf.probe {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "poc_probe_failed_no_timeout", "host", inf.hostID, "poc_reason", currentPoCPhaseReason())
				continue
			}
			if inf.phaseTransitionAborted {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "timeout_skipped",
					"host", inf.hostID, "reason", "phase_transition_aborted")
				e.recordGatewayTimeoutAction(inf, params, timeoutKindForInflight(inf), "skipped", "phase_transition_aborted")
				continue
			}
			if reason, skip := emptyStreamWithoutWinnerTimeoutSkipReason(inf, e.session); skip {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "timeout_skipped",
					"host", inf.hostID, "reason", reason)
				e.recordGatewayTimeoutAction(inf, params, timeoutKindForInflight(inf), "skipped", reason)
				continue
			}
			if !shouldRunHandleTimeout(inf, e.session) {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "timeout_skipped",
					"host", inf.hostID, "reason", "nonce_already_finished")
				e.recordGatewayTimeoutAction(inf, params, timeoutKindForInflight(inf), "skipped", "nonce_already_finished")
				continue
			}
			if e.longResponseFailureExempt(inf) {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "timeout_skipped",
					"host", inf.hostID,
					"reason", "long_response_after_content",
					"elapsed_ms", time.Since(inf.sendTime).Milliseconds(),
					"content_chunks", inf.contentChunks.Load(),
					"output_bytes", inf.outputBytes.Load(),
				)
				e.recordGatewayTimeoutAction(inf, params, timeoutKindForInflight(inf), "skipped", "long_response_after_content")
				continue
			}
			payload := &host.InferencePayload{
				Prompt:      params.Prompt,
				Model:       params.Model,
				InputLength: params.InputLength,
				MaxTokens:   params.MaxTokens,
				StartedAt:   params.StartedAt,
			}
			e.recordGatewayTimeoutAction(inf, params, timeoutKindForInflight(inf), "started", "none")
			result, err := e.session.HandleTimeout(ctx, inf.nonce, inf.sendTime, payload)
			if result.Reason != "" && e.metrics != nil {
				e.metrics.RecordInferenceTimeout(result.Reason)
			}
			if err != nil {
				action, reason := gatewayTimeoutFailureAction(result)
				e.recordGatewayTimeoutAction(inf, params, timeoutResultKind(result, inf), action, reason, result.DetailReason)
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "timeout_failed", "host", inf.hostID, "error", err)
			} else {
				e.recordGatewayTimeoutAction(inf, params, timeoutResultKind(result, inf), "completed", "none")
			}
		}
		if hostErr := hostApplicationErrorFromAttempts(attempts, winnerNonce); hostErr != nil {
			captureAllAttemptsFailedRequest(ctx, e.devshardID, params, hostErr)
			logRequestStage(ctx, "request_failed", "escrow", e.devshardID, "error", hostErr)
			e.recordGatewayRequestOutcome(params.Model, "failed", gatewayRequestFailureReason(failed))
			e.completeAccountingRequest(ctx, 0, decision, "failed")
			e.logRequestSettled(ctx, 0, decision, "failed")
			e.checkEscrowMissing(ctx, attempts)
			return hostErr
		}
		errMsg := "inference: no non-probe attempt finished"
		if opts.forceTreatAsFailure && anySucceeded {
			errMsg = "inference: winner failed after streaming started (alternate completion ignored)"
		}
		if opts.nonStreamingReducedTokenTimeout {
			errMsg = (&nonStreamingReducedMaxTokensTimeoutError{}).Error()
		}
		captureAllAttemptsFailedRequest(ctx, e.devshardID, params, fmt.Errorf("%s", errMsg))
		logRequestStage(ctx, "request_failed", "escrow", e.devshardID, "error", errMsg)
		e.recordGatewayRequestOutcome(params.Model, "failed", gatewayRequestFailureReason(failed))
		e.completeAccountingRequest(ctx, 0, decision, "failed")
		e.logRequestSettled(ctx, 0, decision, "failed")
		e.checkEscrowMissing(ctx, attempts)
		if opts.nonStreamingReducedTokenTimeout {
			return &nonStreamingReducedMaxTokensTimeoutError{}
		}
		return fmt.Errorf("%s", errMsg)
	}

	var involvement []HostInvolvement
	for _, inf := range attempts {
		if inf.probe {
			continue
		}
		if inf.phaseTransitionAborted {
			continue
		}
		e.recordSampleOnce(inf, params, true)
		involvement = append(involvement, e.buildInvolvement(inf, winnerNonce, params))
	}
	e.perf.RecordRequest(RequestRecord{
		Timestamp:     time.Now(),
		Model:         params.Model,
		InputTokens:   params.InputLength,
		WinnerHostIdx: winnerIdx,
		WinnerNonce:   winnerNonce,
		Decision:      decision.Reason,
		Hosts:         involvement,
	})
	if len(failed) > 0 {
		payload := &host.InferencePayload{
			Prompt:      params.Prompt,
			Model:       params.Model,
			InputLength: params.InputLength,
			MaxTokens:   params.MaxTokens,
			StartedAt:   params.StartedAt,
		}
		if anySucceeded {
			e.goTrackedRaceCleanup(func() {
				bgCtx, _ := ensureRequestLogContext(context.Background())
				bgCtx = logging.PropagateRequestID(bgCtx, ctx)
				for _, inf := range failed {
					if inf.probe {
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "poc_probe_failed_no_timeout", "host", inf.hostID, "poc_reason", currentPoCPhaseReason())
						continue
					}
					if inf.phaseTransitionAborted {
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "timeout_skipped",
							"host", inf.hostID, "reason", "phase_transition_aborted")
						e.recordGatewayTimeoutAction(inf, params, timeoutKindForInflight(inf), "skipped", "phase_transition_aborted")
						continue
					}
					if reason, blocked := e.escrowStateBlockReason(e.participantKeyForHost(inf.hostIdx)); blocked {
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "timeout_skipped",
							"host", inf.hostID, "reason", reason)
						e.recordGatewayTimeoutAction(inf, params, timeoutKindForInflight(inf), "skipped", reason)
						continue
					}
					if !shouldRunHandleTimeout(inf, e.session) {
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "timeout_skipped",
							"host", inf.hostID, "reason", "nonce_already_finished")
						e.recordGatewayTimeoutAction(inf, params, timeoutKindForInflight(inf), "skipped", "nonce_already_finished")
						continue
					}
					if e.longResponseFailureExempt(inf) {
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "timeout_skipped",
							"host", inf.hostID,
							"reason", "long_response_after_content",
							"elapsed_ms", time.Since(inf.sendTime).Milliseconds(),
							"content_chunks", inf.contentChunks.Load(),
							"output_bytes", inf.outputBytes.Load(),
						)
						e.recordGatewayTimeoutAction(inf, params, timeoutKindForInflight(inf), "skipped", "long_response_after_content")
						continue
					}
					e.recordGatewayTimeoutAction(inf, params, timeoutKindForInflight(inf), "started", "none")
					result, err := e.session.HandleTimeout(bgCtx, inf.nonce, inf.sendTime, payload)
					if result.Reason != "" && e.metrics != nil {
						e.metrics.RecordInferenceTimeout(result.Reason)
					}
					if err != nil {
						action, reason := gatewayTimeoutFailureAction(result)
						e.recordGatewayTimeoutAction(inf, params, timeoutResultKind(result, inf), action, reason, result.DetailReason)
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "background_timeout_failed", "host", inf.hostID, "error", err)
					} else {
						e.recordGatewayTimeoutAction(inf, params, timeoutResultKind(result, inf), "completed", "none")
					}
				}
				e.logRequestSettled(bgCtx, winnerNonce, decision, "success")
			})
		}
	}

	e.completeAccountingRequest(ctx, winnerNonce, decision, "success")
	e.recordGatewayRequestOutcome(params.Model, "success", "none")
	e.recordGatewayUserVisibleWin(attempts, params, winnerNonce)
	e.recordGatewayHiddenFailure(params.Model, failed)
	logRequestStage(ctx, "request_succeeded", "escrow", e.devshardID, "winner_nonce", winnerNonce, "decision", decision.Reason)
	if len(failed) == 0 {
		e.logRequestSettled(ctx, winnerNonce, decision, "success")
	}

	e.checkEscrowMissing(ctx, attempts)

	return nil
}

func (e *Redundancy) maxAttempts() int {
	if e.groupSize <= 0 {
		return 1
	}
	maxSpeculativeAttempts := CurrentMaxSpeculativeAttempts()
	if maxSpeculativeAttempts <= 0 || maxSpeculativeAttempts > e.groupSize {
		return e.groupSize
	}
	return maxSpeculativeAttempts
}

func (e *Redundancy) resolvedWinnerNonce(attempts []*inflight, winnerNonce uint64) uint64 {
	if winnerNonce != 0 {
		return winnerNonce
	}
	for _, inf := range attempts {
		if inf.probe {
			continue
		}
		if e.session.IsNonceFinished(inf.nonce) && !isFailedStreamAttempt(inf) {
			return inf.nonce
		}
	}
	return 0
}

func stopTimer(t *time.Timer) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

func (e *Redundancy) logRequestSettled(ctx context.Context, winnerNonce uint64, decision Decision, outcome string) {
	logRequestStage(ctx, "request_fully_settled",
		"escrow", e.devshardID,
		"winner_nonce", winnerNonce,
		"decision", decision.Reason,
		"outcome", outcome,
	)
}

func (e *Redundancy) recordAccountingRequestStart(ctx context.Context, params user.InferenceParams) {
	requestID, ok := requestLogFromContext(ctx)
	if !ok || requestID == "" || e.perf == nil {
		return
	}
	e.perf.RecordAccountingRequestStart(requestID, e.devshardID, params.Model, time.Now())
}

func (e *Redundancy) recordAccountingAttempt(ctx context.Context, inf *inflight) {
	requestID, ok := requestLogFromContext(ctx)
	if !ok || requestID == "" || e.perf == nil || inf == nil {
		return
	}
	e.perf.RecordAccountingAttempt(RequestAccountingAttempt{
		RequestID:      requestID,
		EscrowID:       inf.escrowID,
		Nonce:          inf.nonce,
		HostIdx:        inf.hostIdx,
		ParticipantKey: e.participantKeyForHost(inf.hostIdx),
		Probe:          inf.probe,
		CreatedAt:      time.Now(),
	})
}

func (e *Redundancy) completeAccountingRequest(ctx context.Context, winnerNonce uint64, decision Decision, outcome string) {
	requestID, ok := requestLogFromContext(ctx)
	if !ok || requestID == "" || e.perf == nil {
		return
	}
	e.perf.CompleteAccountingRequest(requestID, e.devshardID, winnerNonce, decision.Reason, outcome, time.Now())
}

func (e *Redundancy) buildInvolvement(inf *inflight, winnerNonce uint64, params user.InferenceParams) HostInvolvement {
	successfulForPerf := attemptCountsAsSuccessfulForPerf(inf, params, e.session)
	hi := HostInvolvement{
		HostIdx:         inf.hostIdx,
		ParticipantKey:  e.participantKeyForHost(inf.hostIdx),
		Nonce:           inf.nonce,
		OutputChunks:    inf.outputChunks.Load(),
		Responsive:      successfulForPerf,
		Finished:        successfulForPerf,
		Winner:          inf.nonce == winnerNonce,
		ExcludePairwise: inf.excludePairwise,
	}
	if !inf.sendTime.IsZero() {
		if inf.hasReceipt() {
			hi.ReceiptTimeMs = float64(inf.receiptAt().Sub(inf.sendTime).Milliseconds())
		}
		if inf.hasFirstToken() {
			hi.FirstTokenMs = float64(inf.firstTokenAt().Sub(inf.sendTime).Milliseconds())
		}
		hi.TotalTimeMs = float64(time.Since(inf.sendTime).Milliseconds())
	}
	return hi
}

func (e *Redundancy) recordSample(inf *inflight, params user.InferenceParams, requestSucceeded bool) {
	if inf.probe {
		return
	}
	if inf.phaseTransitionAborted {
		return
	}
	// Long non-stream responses that end empty around the client timeout are
	// still useful timing samples, but should not be treated like fast empty
	// stream faults for participant quarantine.
	longNonStreamEmptyExempt := longNonStreamEmptyFailureExempt(inf, params)
	emptyStream := isEmptyStreamAttempt(inf)
	if emptyStream && emptyStreamAccountingSuppressedByPoC() {
		return
	}
	participantKey := e.participantKeyForHost(inf.hostIdx)
	if emptyStream && !longNonStreamEmptyExempt && e.participantLimiter != nil {
		if isModelBurnEmpty(inf, e.model) {
			// Reasoning-burn outcome: the model emitted completion tokens but
			// no content. Telemetry-only — not a host fault, no quarantine.
			e.participantLimiter.ObserveModelBurnEmpty(participantKey, e.model)
		} else {
			e.participantLimiter.ObserveEmptyStreamForModel(participantKey, e.model)
		}
	}
	if !requestSucceeded && emptyStream {
		return
	}
	responsive := attemptCountsAsSuccessfulForPerf(inf, params, e.session)
	sample := RequestSample{
		HostIdx:        inf.hostIdx,
		ParticipantKey: participantKey,
		Model:          normalizeModelID(params.Model),
		Responsive:     responsive,
		SendTime:       inf.sendTime,
		ReceiptTime:    inf.receiptAt(),
		FirstToken:     inf.firstTokenAt(),
		InputTokens:    params.InputLength,
	}
	if !inf.sendTime.IsZero() {
		sample.TotalTime = time.Since(inf.sendTime)
	}
	e.perf.Record(sample)
	if e.participantLimiter != nil {
		switch {
		case responsive && !longNonStreamEmptyExempt:
			e.participantLimiter.ObserveSuccessfulInferenceForModel(participantKey, e.model)
		}
	}
	if e.metrics != nil {
		e.metrics.ObserveRequestSample(e.devshardID, sample)
	}
}

func probeParams(params user.InferenceParams) user.InferenceParams {
	params.Prompt = pocProbePromptBody
	params.InputLength = uint64(len(pocProbePromptBody))
	params.MaxTokens = pocProbeMaxTokens
	return params
}

// ghostProbeParams returns the params for a synthetic probe that is not
// tied to any user request. The model is taken from the escrow
// registration (passed into NewRedundancy) so the host receives a
// well-formed inference for the configured model.
func ghostProbeParams(model string) user.InferenceParams {
	return probeParams(user.InferenceParams{
		Model:     model,
		StartedAt: time.Now().UnixMilli(),
	})
}

// runGhostProbe records a synthetic probe inference WITHOUT contacting
// the host. The picker invokes this when it must consume a nonce but
// no real request should land on the host (PoC-required, queue
// excluded all available hosts past pickerStaleThreshold, or host is
// reactively throttled). Every kind behaves identically: log + return.
//
// Why silent for every kind:
//
//   - PoC: the host cannot serve user traffic during PoC. We previously
//     sent a tiny inference so the host produced MsgFinishInference
//     for the nonce; that produces the same chain settlement an idle
//     host's own probe would, but at the cost of an HTTP round-trip
//     per burned nonce. Skipping the round-trip removes the per-nonce
//     load on a host that is already busy with PoC stitching.
//
//   - Exclude: the queue had no compatible request for this host
//     after the stale-hold window. Sending a tiny inference settled
//     the chain protocol, but again at HTTP cost. Skipping it leaves
//     the nonce as an orphan MsgStart -- chain-side, other validators
//     may post a timeout vote; we don't.
//
//   - Throttled: the host just 503'd / 429'd and is over capacity.
//     Sending anything would only deepen the overload. This was the
//     original silent path; PoC and Exclude now match it.
//
// Side effects accepted across all kinds:
//
//   - The MsgStart for the burned nonce is composed inside
//     PrepareInferenceFn and lives in s.diffs. It will replay to the
//     host as catch-up on the host's next real dispatch (so the chain
//     view eventually converges). For PoC-required hosts that means a
//     backlog of orphan MsgStarts arriving once PoC ends.
//
//   - We do not post a timeout vote from this node: there is no
//     inflight, so HandleTimeout never runs. Other validators may.
//
//   - PerfTracker is not updated (no attempt happened from our POV).
//
// Liveness: every nonce the session advances through is accounted for
// exactly once -- by a real request via the picker, or by this
// log-only no-op. Without this method the picker would have to dequeue
// a real request and turn IT into a probe, costing that request a turn.
//
// kind is retained on the signature for log-label differentiation only;
// the dispatch path is identical for every kind.
func (e *Redundancy) runGhostProbe(prepared *user.PreparedInference, kind ghostKind, reason string) {
	if prepared == nil || e.session == nil {
		return
	}
	participantKey := e.participantKeyForHost(prepared.HostIdx())
	quarantineMode := e.quarantineModeForParticipant(participantKey)
	e.accounting.Ghost(e.devshardID, prepared.Nonce(), reason, quarantineMode)
	if e.metrics != nil {
		e.metrics.RecordGatewaySlotDecision(GatewaySlotDecisionMetric{
			ParticipantKey: participantKey,
			Model:          e.model,
			EscrowID:       e.devshardID,
			Decision:       "ghost_no_send",
			Reason:         reason,
			QuarantineMode: quarantineMode,
		})
	}
	ctx, _ := ensureRequestLogContext(context.Background())
	logInferenceStage(ctx, e.devshardID, prepared.Nonce(), "ghost_probe_skipped",
		"host", e.session.HostLabel(prepared.HostIdx()),
		"kind", int(kind),
		"reason", reason,
		"poc_reason", currentPoCPhaseReason(),
	)
}

// fireBalanceExhausted fires onBalanceExhausted at most once per Redundancy
// lifetime. The callback deactivates the runtime at the gateway level so no
// more requests are routed to this escrow.
func (e *Redundancy) fireBalanceExhausted() {
	if e.onBalanceExhausted == nil {
		return
	}
	e.balanceExhaustedOnce.Do(func() {
		log.Printf("escrow_balance_exhausted escrow=%s", e.devshardID)
		e.onBalanceExhausted()
	})
}

// checkEscrowMissing fires onEscrowMissing if any attempt got "escrow not found"
// from its host. The callback is expected to trigger a verified chain check.
func (e *Redundancy) checkEscrowMissing(ctx context.Context, attempts []*inflight) {
	if e.onEscrowMissing == nil {
		return
	}
	for _, inf := range attempts {
		if inf.err != nil && transport.IsUpstreamEscrowNotFound(inf.err) {
			logRequestStage(ctx, "escrow_not_found_reported_by_host",
				"escrow", e.devshardID, "host", inf.hostID, "nonce", inf.nonce)
			e.onEscrowMissing()
			return
		}
	}
}
