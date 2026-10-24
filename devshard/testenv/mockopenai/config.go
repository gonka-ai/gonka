package mockopenai

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"strconv"
	"time"

	"devshard/testenv/replay"
)

// Config wires the mock OpenAI HTTP server.
type Config struct {
	Addr       string
	Faults     FaultConfig
	Workers    int
	Queue      int
	ReplayFile string
	// Chaos picks an answer shape for every execution (chaos.go). It lives here rather than in
	// FaultConfig, which is compared with == and cannot hold the weights map.
	Chaos ChaosConfig
}

// DefaultConfig returns local dev defaults.
func DefaultConfig() Config {
	return Config{
		Addr: ":8088",
		Faults: FaultConfig{
			StreamChunkDelay: 5 * time.Millisecond,
		},
	}
}

// FaultConfig holds runtime fault-injection knobs (env or POST /testenv/fault).
//
// Latency delays Validate only (non-stream JSON or an enforced-token replay). StreamFirstTokenDelay delays
// the first SSE chunk (Execute TTFT). StreamChunkDelay sleeps after each
// flushed chunk, so the first token stays immediate when StreamFirstTokenDelay
// is zero. Do not put Validate latency in front of the first stream chunk.
type FaultConfig struct {
	Latency               time.Duration
	StreamFirstTokenDelay time.Duration
	HTTPStatus            int // 0 = OK
	FailureRate           float64
	Hang                  bool // accept the request and wait for its context to be cancelled
	DropFirstChunk        bool
	PartialStream         bool   // omit final chunk + [DONE]
	SSEErrorMessage       string // if set: HTTP 200 stream with OpenAI/vLLM error event + [DONE]
	StreamChunkDelay      time.Duration
	PauseStream           bool // pause after first content chunk until testenv release
	// StreamErrorEnvelope returns HTTP 200 text/event-stream with an OpenAI
	// error envelope and [DONE], matching vLLM EngineCore failures. Distinct
	// from HTTPStatus >= 400, which is a JSON 5xx with no Finish.
	StreamErrorEnvelope bool
	// UsageCompletionTokens and UsagePromptTokens, when positive, replace the
	// counts in the usage of a recorded stream (total_tokens follows), as an
	// engine that overstates its usage would. PadEmptyChunks adds that many
	// chunks with an empty content delta and no logprobs ahead of the chunk
	// that finishes the choice. They act on executions only: a validation
	// replay stands for another host's ML node and gets the recording as is.
	// DropTokens strips every sampled token (content, logprobs, token_ids)
	// from the stream and keeps its usage and finish_reason: an answer with
	// a claim and no generated tokens.
	UsageCompletionTokens int
	UsagePromptTokens     int
	PadEmptyChunks        int
	DropTokens            bool

	// Oracle makes the mock a replay oracle: a request that carries enforced_tokens is answered
	// from those tokens exactly as received (their ids, their top alternatives, their count), as SSE
	// in chunks of ChunkTokens when the replay asks to stream, and usage.completion_tokens is the
	// number of tokens actually emitted or replayed. The mock never
	// decides a verdict; whatever the validator receives from it follows from what the caller sent.
	Oracle bool
	// HonestUsage reports usage.completion_tokens as the number of tokens the answer actually carried,
	// as vLLM does, without the rest of the oracle mode. Off, the mock reports len(text)/4 while it
	// emits one token per rune, and a check that compares a host's Finish against the ML cannot tell
	// an overclaim from the mock's own estimate.
	HonestUsage bool
	// ChunkTokens is how many tokens one streamed chunk carries. 0 and 1 keep one token per chunk.
	ChunkTokens int
	// FirstContentDelay and ExecDuration shape a streamed execution: the first content chunk goes out
	// FirstContentDelay after the request is read, the last one ExecDuration after it. 0 keeps
	// StreamChunkDelay pacing.
	FirstContentDelay time.Duration
	ExecDuration      time.Duration
	// ReplayDelay is the service time of a request that carries enforced_tokens. 0 falls back to Latency.
	ReplayDelay time.Duration

	// Set per request by a chaos action, never by a patch.
	emptyAnswer bool
	oddUsage    bool
	wrongAnswer bool
}

// ShouldFail deterministically selects requests for a configured failure
// rate. The request body includes the load-generator request ID, so a fixed
// scenario seed produces the same failure pattern on every run.
func (f FaultConfig) ShouldFail(body []byte) bool {
	if f.FailureRate <= 0 {
		return false
	}
	if f.FailureRate >= 1 {
		return true
	}
	sum := sha256.Sum256(body)
	sample := binary.BigEndian.Uint64(sum[:8])
	return float64(sample)/float64(^uint64(0)) < f.FailureRate
}

// FaultPatch is the JSON body for POST /testenv/fault.
type FaultPatch struct {
	LatencyMs           *int    `json:"latency_ms,omitempty"`
	StreamTTFTMs        *int    `json:"stream_ttft_ms,omitempty"`
	HTTPStatus          *int    `json:"http_status,omitempty"`
	DropFirstChunk      *bool   `json:"drop_first_chunk,omitempty"`
	PartialStream       *bool   `json:"partial_stream,omitempty"`
	SSEErrorMessage     *string `json:"sse_error_message,omitempty"`
	StreamChunkDelay    *int    `json:"stream_chunk_delay_ms,omitempty"`
	PauseStream         *bool   `json:"pause_stream,omitempty"`
	StreamErrorEnvelope *bool   `json:"stream_error_envelope,omitempty"`
	Hang                *bool   `json:"hang,omitempty"`
	// Zero turns a usage or padding knob off.
	UsageCompletionTokens *int `json:"usage_completion_tokens,omitempty"`
	UsagePromptTokens     *int `json:"usage_prompt_tokens,omitempty"`
	PadEmptyChunks        *int `json:"pad_empty_chunks,omitempty"`

	DropTokens *bool `json:"drop_tokens,omitempty"`

	Oracle            *bool `json:"oracle,omitempty"`
	ChunkTokens       *int  `json:"chunk_tokens,omitempty"`
	FirstContentDelay *int  `json:"first_content_delay_ms,omitempty"`
	ExecDuration      *int  `json:"exec_duration_ms,omitempty"`
	ReplayDelay       *int  `json:"replay_delay_ms,omitempty"`

	// Seed, when set, scopes the patch's usage, padding and drop_tokens
	// knobs to the requests that carry this seed, so concurrent requests
	// can each get their own; the other knobs of such a patch are ignored.
	// A seed patch with all of them zero removes the seed's knobs.
	Seed *int `json:"seed,omitempty"`

	// Chaos replaces the chaos config; a seed patch does not scope it.
	Chaos *ChaosConfig `json:"chaos,omitempty"`
}

func (p FaultPatch) apply(dst *FaultConfig) {
	if p.LatencyMs != nil {
		dst.Latency = time.Duration(*p.LatencyMs) * time.Millisecond
	}
	if p.StreamTTFTMs != nil {
		dst.StreamFirstTokenDelay = time.Duration(*p.StreamTTFTMs) * time.Millisecond
	}
	if p.HTTPStatus != nil {
		dst.HTTPStatus = *p.HTTPStatus
	}
	if p.DropFirstChunk != nil {
		dst.DropFirstChunk = *p.DropFirstChunk
	}
	if p.PartialStream != nil {
		dst.PartialStream = *p.PartialStream
	}
	if p.SSEErrorMessage != nil {
		dst.SSEErrorMessage = *p.SSEErrorMessage
	}
	if p.StreamChunkDelay != nil {
		dst.StreamChunkDelay = time.Duration(*p.StreamChunkDelay) * time.Millisecond
	}
	if p.PauseStream != nil {
		dst.PauseStream = *p.PauseStream
	}
	if p.StreamErrorEnvelope != nil {
		dst.StreamErrorEnvelope = *p.StreamErrorEnvelope
	}
	if p.Hang != nil {
		dst.Hang = *p.Hang
	}
	if p.UsageCompletionTokens != nil {
		dst.UsageCompletionTokens = *p.UsageCompletionTokens
	}
	if p.UsagePromptTokens != nil {
		dst.UsagePromptTokens = *p.UsagePromptTokens
	}
	if p.PadEmptyChunks != nil {
		dst.PadEmptyChunks = *p.PadEmptyChunks
	}
	if p.DropTokens != nil {
		dst.DropTokens = *p.DropTokens
	}
	if p.Oracle != nil {
		dst.Oracle = *p.Oracle
	}
	if p.ChunkTokens != nil {
		dst.ChunkTokens = *p.ChunkTokens
	}
	if p.FirstContentDelay != nil {
		dst.FirstContentDelay = time.Duration(*p.FirstContentDelay) * time.Millisecond
	}
	if p.ExecDuration != nil {
		dst.ExecDuration = time.Duration(*p.ExecDuration) * time.Millisecond
	}
	if p.ReplayDelay != nil {
		dst.ReplayDelay = time.Duration(*p.ReplayDelay) * time.Millisecond
	}
}

// OpenAIErrorBody is the nested OpenAI/vLLM error JSON shape.
func OpenAIErrorBody(status int, message string) map[string]any {
	if message == "" {
		message = "mock-openai fault injection"
	}
	typ := "InternalServerError"
	if status >= 400 && status < 500 {
		typ = "BadRequestError"
	}
	return map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    typ,
			"param":   nil,
			"code":    status,
		},
	}
}

// ChatRequest is the subset of OpenAI chat completion we care about.
type ChatRequest struct {
	Model       string          `json:"model"`
	Messages    []ChatMessage   `json:"messages"`
	Stream      bool            `json:"stream"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Seed        *int            `json:"seed,omitempty"`
	Logprobs    bool            `json:"logprobs,omitempty"`
	TopLogprobs int             `json:"top_logprobs,omitempty"`
	Raw         json.RawMessage `json:"-"`

	// EnforcedTokens is set on a validator replay; the token ids are the ones the executor produced.
	EnforcedTokens *EnforcedTokens `json:"enforced_tokens,omitempty"`
}

// EnforcedTokens mirrors the wire shape of completionapi.EnforcedTokens. It is declared here on
// purpose: the mock must not share the production parser it is used to check.
type EnforcedTokens struct {
	Tokens []EnforcedToken `json:"tokens"`
}

type EnforcedToken struct {
	Token     string   `json:"token"`
	TopTokens []string `json:"top_tokens"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func replayMessages(messages []ChatMessage) []replay.Message {
	out := make([]replay.Message, len(messages))
	for i, message := range messages {
		out[i] = replay.Message{Role: message.Role, Content: message.Content}
	}
	return out
}

// completionText derives deterministic assistant text from model + messages.
// stream / stream_options are excluded so a forced-upstream streaming request
// yields the same seed as the equivalent non-streaming request.
//
// The text is cut or padded to max_tokens, one token per rune, so an honest replay never has more
// positions than max_tokens and citest can force aggregate spill / oversize without a real long generation.
func completionText(body []byte) string {
	var req struct {
		Model     string        `json:"model"`
		Messages  []ChatMessage `json:"messages"`
		MaxTokens int           `json:"max_tokens"`
	}
	seed := body
	if err := json.Unmarshal(body, &req); err == nil {
		if canonical, err := json.Marshal(struct {
			Model    string        `json:"model"`
			Messages []ChatMessage `json:"messages"`
		}{Model: req.Model, Messages: req.Messages}); err == nil {
			seed = canonical
		}
	}
	sum := sha256.Sum256(seed)
	base := "mock-openai:" + hex.EncodeToString(sum[:8])
	runes := []rune(base)
	if req.MaxTokens > 0 && req.MaxTokens < len(runes) {
		return string(runes[:req.MaxTokens])
	}
	for len(runes) < req.MaxTokens {
		runes = append(runes, 'x')
	}
	return string(runes)
}

func promptTokenEstimate(body []byte) int {
	if len(body) == 0 {
		return 1
	}
	n := len(body) / 4
	if n < 1 {
		return 1
	}
	return n
}

// buildLogprobContent returns OpenAI-shaped logprobs.content entries for text.
// topN mirrors the request's top_logprobs (gateway forces 5 upstream).
//
// "token" is a numeric token ID (decimal string), matching vLLM after
// gm/enforced-str. The position's decoded text lives in "bytes" (UTF-8 code units as
// []int — []byte would JSON-encode as base64 and break completionapi.Response).
// Validators reject decoded-text tokens via HasNonNumericTokens before the
// ML replay; citest SlowMockOpenAI needs that replay so leases stay pending.
func buildLogprobContent(text string, topN int) []map[string]any {
	if topN < 0 {
		topN = 0
	}
	if topN > 20 {
		topN = 20
	}
	var out []map[string]any
	for _, r := range []rune(text) {
		tok := string(r)
		id := mockTokenID(tok)
		entry := map[string]any{
			"token":   id,
			"logprob": -0.1,
			"bytes":   utf8CodeUnits(tok),
		}
		tops := make([]map[string]any, 0, topN)
		for i := 0; i < topN; i++ {
			alt := tok
			if i > 0 {
				alt = tok + string(rune('a'+i-1))
			}
			altID := mockTokenID(alt)
			// vLLM spells an alternative's bytes from its id string, which the compressor checks.
			tops = append(tops, map[string]any{
				"token":   altID,
				"logprob": -0.1 - float64(i),
				"bytes":   utf8CodeUnits(altID),
			})
		}
		entry["top_logprobs"] = tops
		out = append(out, entry)
	}
	return out
}

// mockTokenID maps token text to a stable non-negative decimal id so executor
// and validator logprobs compare equal, and HasNonNumericTokens stays false.
func mockTokenID(s string) string {
	const replayTokenIDLimit = 9_000_000

	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	n := h.Sum32() % replayTokenIDLimit
	if n == 0 {
		n = 1
	}
	return strconv.FormatUint(uint64(n), 10)
}

func utf8CodeUnits(s string) []int {
	b := []byte(s)
	out := make([]int, len(b))
	for i, c := range b {
		out[i] = int(c)
	}
	return out
}
