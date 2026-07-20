package mockopenai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	HTTPStatus       int  // 0 = OK
	DropFirstChunk   bool
	PartialStream    bool // omit final chunk + [DONE]
	StreamChunkDelay time.Duration
}

// FaultPatch is the JSON body for POST /testenv/fault.
type FaultPatch struct {
	LatencyMs        *int  `json:"latency_ms,omitempty"`
	HTTPStatus       *int  `json:"http_status,omitempty"`
	DropFirstChunk   *bool `json:"drop_first_chunk,omitempty"`
	PartialStream    *bool `json:"partial_stream,omitempty"`
	StreamChunkDelay *int  `json:"stream_chunk_delay_ms,omitempty"`
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
}

// ChatRequest is the subset of OpenAI chat completion we care about.
type ChatRequest struct {
	Model    string          `json:"model"`
	Messages []ChatMessage   `json:"messages"`
	Stream   bool            `json:"stream"`
	Seed     *int            `json:"seed,omitempty"`
	Logprobs bool            `json:"logprobs,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// completionText derives deterministic assistant text from the request body.
func completionText(body []byte) string {
	sum := sha256.Sum256(body)
	return "mock-openai:" + hex.EncodeToString(sum[:8])
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
