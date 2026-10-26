package mockopenai

// Request journal: every chat request the node received, what it was asked and what it answered,
// kept in memory up to a bound and exported by GET /testenv/log. A load run reads it to compare
// what the ML node really produced with what the hosts were paid for.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
)

const (
	PhaseExecute = "execute" // a request without enforced_tokens
	PhaseReplay  = "replay"  // a request that carries enforced_tokens

	// OutcomeClientGone marks an answer the caller hung up on before it was written in full.
	OutcomeClientGone = "client_gone"

	// maxLogRecords bounds the request log. Records beyond it are counted, not kept, and the
	// export says so; a truncated export must never read as a complete one.
	maxLogRecords = 200000
)

// RequestRecord is one chat request as the mock saw it and answered it.
type RequestRecord struct {
	Seq         int    `json:"seq"`
	StartUnixMs int64  `json:"start_unix_ms"`
	DurationMs  int64  `json:"duration_ms"`
	Phase       string `json:"phase"`
	Seed        *int   `json:"seed,omitempty"`
	PromptKey   string `json:"prompt_key"`
	Model       string `json:"model,omitempty"`
	// Node is the pool member that answered; the mock leaves it empty, an export of several
	// nodes fills it in.
	Node           string `json:"node,omitempty"`
	Stream         bool   `json:"stream"`
	EnforcedTokens int    `json:"enforced_tokens"`
	EnforcedTops   int    `json:"enforced_top_tokens"` // alternatives across all enforced tokens
	TokensOut      int    `json:"tokens_out"`          // tokens emitted or replayed
	// UsageCompletion is the completion_tokens the answer reported in usage. By default the mock
	// reports len(text)/4 while it emits one token per rune, so it differs from TokensOut.
	UsageCompletion int       `json:"usage_completion_tokens,omitempty"`
	ChunkTokens     int       `json:"chunk_tokens"`
	ActiveAtStart   int       `json:"active_at_start"`
	Rule            string    `json:"rule,omitempty"`
	Chaos           string    `json:"chaos,omitempty"` // the chaos action the execution was answered with
	Status          int       `json:"status"`          // 0: connection dropped or stream cut before a status was settled
	Outcome         string    `json:"outcome"`
	Wire            *WireView `json:"wire,omitempty"`
}

// WireView is the request as it reached the ML endpoint, reduced to the fields the production
// normalizer is supposed to settle. A pointer is nil when the field was absent, so "absent" and
// "false" stay different.
type WireView struct {
	Stream            *bool `json:"stream,omitempty"`
	Logprobs          *bool `json:"logprobs,omitempty"`
	TopLogprobs       *int  `json:"top_logprobs,omitempty"`
	ReturnTokenIDs    *bool `json:"return_token_ids,omitempty"`
	SkipSpecialTokens *bool `json:"skip_special_tokens,omitempty"`
	N                 *int  `json:"n,omitempty"`
	MinTokens         *int  `json:"min_tokens,omitempty"`
	MaxTokens         *int  `json:"max_tokens,omitempty"`
	HasStreamOptions  bool  `json:"has_stream_options"`
	IncludeUsage      *bool `json:"include_usage,omitempty"`
	HasEnforced       bool  `json:"has_enforced_tokens"`
}

func wireViewOf(body []byte) *WireView {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil
	}
	getBool := func(raw json.RawMessage) *bool {
		var v bool
		if json.Unmarshal(raw, &v) != nil {
			return nil
		}
		return &v
	}
	getInt := func(raw json.RawMessage) *int {
		var v float64
		if json.Unmarshal(raw, &v) != nil {
			return nil
		}
		i := int(v)
		return &i
	}
	w := &WireView{
		Stream: getBool(m["stream"]), Logprobs: getBool(m["logprobs"]), TopLogprobs: getInt(m["top_logprobs"]),
		ReturnTokenIDs: getBool(m["return_token_ids"]), SkipSpecialTokens: getBool(m["skip_special_tokens"]),
		N: getInt(m["n"]), MinTokens: getInt(m["min_tokens"]), MaxTokens: getInt(m["max_tokens"]),
	}
	if raw, ok := m["stream_options"]; ok {
		w.HasStreamOptions = true
		var opts map[string]json.RawMessage
		if json.Unmarshal(raw, &opts) == nil {
			w.IncludeUsage = getBool(opts["include_usage"])
		}
	}
	_, w.HasEnforced = m["enforced_tokens"]
	return w
}

// Stats is the mock's own account of the load it carried.
type Stats struct {
	Active     int            `json:"active"`
	PeakActive int            `json:"peak_active"`
	Requests   map[string]int `json:"requests"`
	LogRecords int            `json:"log_records"`
	LogDropped int            `json:"log_dropped"`

	RequestsReceived    uint64 `json:"requests_received"`
	SuccessfulResponses uint64 `json:"successful_responses"`
	FailedResponses     uint64 `json:"failed_responses"`
	Timeouts            uint64 `json:"timeouts"`
	ReplayHits          uint64 `json:"replay_hits"`
	ReplayMisses        uint64 `json:"replay_misses"`
}

type journalState struct {
	mu      sync.Mutex
	active  int
	peak    int
	reqs    map[string]int
	seq     int
	log     []RequestRecord
	dropped int
}

// enter counts a request in flight and returns how many are, itself included.
func (j *journalState) enter() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.active++
	if j.active > j.peak {
		j.peak = j.active
	}
	return j.active
}

func (j *journalState) leave() {
	j.mu.Lock()
	j.active--
	j.mu.Unlock()
}

func (j *journalState) record(rec RequestRecord) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.reqs == nil {
		j.reqs = map[string]int{}
	}
	j.reqs[rec.Phase]++
	j.seq++
	rec.Seq = j.seq
	if len(j.log) >= maxLogRecords {
		j.dropped++
		return
	}
	j.log = append(j.log, rec)
}

func (j *journalState) snapshotStats() Stats {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := Stats{
		Active: j.active, PeakActive: j.peak, Requests: map[string]int{},
		LogRecords: len(j.log), LogDropped: j.dropped,
	}
	for k, v := range j.reqs {
		st.Requests[k] = v
	}
	return st
}

func (j *journalState) snapshotLog() ([]RequestRecord, int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]RequestRecord(nil), j.log...), j.dropped
}

// PromptKey identifies a request by its model and messages, the same bytes completionText hashes.
// A harness that knows the prompt it sent can compute the key and find the request in the log.
func PromptKey(body []byte) string {
	var req struct {
		Model    string        `json:"model"`
		Messages []ChatMessage `json:"messages"`
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
	return hex.EncodeToString(sum[:8])
}

func promptText(msgs []ChatMessage) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, m.Content)
	}
	return strings.Join(parts, "\n")
}

func countTops(et *EnforcedTokens) int {
	if et == nil {
		return 0
	}
	n := 0
	for _, t := range et.Tokens {
		n += len(t.TopTokens)
	}
	return n
}

// recordedAnswer reads the generated positions and the claimed completion count of one encoded
// chunk or response of a recording: the positions are the logprobs entries, or the token ids when
// the answer carries no logprobs.
func recordedAnswer(raw []byte) (positions int, completion *int) {
	var head struct {
		Choices []struct {
			TokenIDs []int64 `json:"token_ids"`
			Logprobs *struct {
				Content []json.RawMessage `json:"content"`
			} `json:"logprobs"`
		} `json:"choices"`
		Usage *struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return 0, nil
	}
	for _, choice := range head.Choices {
		n := len(choice.TokenIDs)
		if choice.Logprobs != nil && len(choice.Logprobs.Content) > n {
			n = len(choice.Logprobs.Content)
		}
		positions += n
	}
	if head.Usage != nil {
		return positions, &head.Usage.CompletionTokens
	}
	return positions, nil
}
