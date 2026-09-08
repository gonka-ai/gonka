package mockopenai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"strconv"
	"time"
)

// Config wires the mock OpenAI HTTP server.
type Config struct {
	Addr   string
	Faults FaultConfig
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
type FaultConfig struct {
	Latency          time.Duration
	HTTPStatus       int // 0 = OK
	DropFirstChunk   bool
	PartialStream    bool // omit final chunk + [DONE]
	StreamChunkDelay time.Duration
	PauseStream      bool // pause after first content chunk until testenv release
	// StreamErrorEnvelope returns HTTP 200 text/event-stream with an OpenAI
	// error envelope and [DONE], matching vLLM EngineCore failures. Distinct
	// from HTTPStatus >= 400, which is a JSON 5xx with no Finish.
	StreamErrorEnvelope bool
}

// FaultPatch is the JSON body for POST /testenv/fault.
type FaultPatch struct {
	LatencyMs           *int  `json:"latency_ms,omitempty"`
	HTTPStatus          *int  `json:"http_status,omitempty"`
	DropFirstChunk      *bool `json:"drop_first_chunk,omitempty"`
	PartialStream       *bool `json:"partial_stream,omitempty"`
	StreamChunkDelay    *int  `json:"stream_chunk_delay_ms,omitempty"`
	PauseStream         *bool `json:"pause_stream,omitempty"`
	StreamErrorEnvelope *bool `json:"stream_error_envelope,omitempty"`
}

func (p FaultPatch) apply(dst *FaultConfig) {
	if p.LatencyMs != nil {
		dst.Latency = time.Duration(*p.LatencyMs) * time.Millisecond
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
	if p.StreamChunkDelay != nil {
		dst.StreamChunkDelay = time.Duration(*p.StreamChunkDelay) * time.Millisecond
	}
	if p.PauseStream != nil {
		dst.PauseStream = *p.PauseStream
	}
	if p.StreamErrorEnvelope != nil {
		dst.StreamErrorEnvelope = *p.StreamErrorEnvelope
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
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// completionText derives deterministic assistant text from model + messages.
// stream / stream_options are excluded so a forced-upstream streaming request
// yields the same seed as the equivalent non-streaming request.
//
// When max_tokens is larger than the base echo, the text is padded so citest
// can force aggregate spill / oversize without a real long generation.
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
	if req.MaxTokens <= len([]rune(base)) {
		return base
	}
	runes := []rune(base)
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
// topN mirrors the request's top_logprobs (the host pins 5 upstream).
//
// "token" is a numeric token ID (decimal string), matching vLLM after
// gm/enforced-str. The decoded text lives in "bytes" (UTF-8 code units as
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
			tops = append(tops, map[string]any{
				"token":   mockTokenID(alt),
				"logprob": -0.1 - float64(i),
				"bytes":   utf8CodeUnits(alt),
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
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	n := h.Sum32()
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
