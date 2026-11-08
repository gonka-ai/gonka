package mockopenai

// Chaos mode: every execution gets one answer shape out of a catalog, picked from a seed so a run
// can be repeated, and the request log names the shape so a check that fires can see why. Only
// executions are touched; a validator's replay is always answered correctly, so an execution
// answered wrongly is one the validators should catch.

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
)

// Chaos actions. An action that needs a stream is not picked for a request that is not streamed.
const (
	ChaosOK          = "ok"
	ChaosDelay       = "delay"        // hold the answer DelayMs, then answer normally
	ChaosClientError = "status_400"   // vLLM-shaped 400: the engine blames the request
	ChaosServerError = "status_500"   // vLLM-shaped 500
	ChaosAbort       = "abort"        // drop the connection without a response
	ChaosErrorString = "error_string" // 500 whose "error" is a string, not the vLLM object
	ChaosPrelude     = "prelude"      // role-only chunks and a hold before the content (stream)
	ChaosPartial     = "partial"      // stream cut halfway, no finish and no [DONE] (stream)
	ChaosSSEError    = "sse_error"    // a few tokens, then a vLLM error event and [DONE] (stream)
	ChaosEmpty       = "empty"        // a finished answer with no content
	ChaosOddUsage    = "odd_usage"    // usage.completion_tokens far from the tokens sent
	ChaosWrongAnswer = "wrong_answer" // the tokens are sent with logprobs the replay will not confirm
)

var streamOnlyChaos = map[string]bool{ChaosPrelude: true, ChaosPartial: true, ChaosSSEError: true}

// DefaultChaosWeights answer most executions normally and spread the rest over every shape.
func DefaultChaosWeights() map[string]int {
	return map[string]int{
		ChaosOK: 12, ChaosDelay: 1, ChaosClientError: 1, ChaosServerError: 1, ChaosAbort: 1, ChaosErrorString: 1,
		ChaosPrelude: 1, ChaosPartial: 1, ChaosSSEError: 1, ChaosEmpty: 1, ChaosOddUsage: 1, ChaosWrongAnswer: 1,
	}
}

// ChaosConfig turns chaos mode on. Weights empty means DefaultChaosWeights; DelayMs 0 means 3000.
type ChaosConfig struct {
	Enabled bool           `json:"enabled"`
	Seed    int64          `json:"seed"`
	Weights map[string]int `json:"weights,omitempty"`
	DelayMs int            `json:"delay_ms,omitempty"`
}

// Validate refuses an unknown action, a negative weight or a negative delay.
func (c ChaosConfig) Validate() error {
	for name, w := range c.Weights {
		if _, ok := DefaultChaosWeights()[name]; !ok {
			return fmt.Errorf("chaos: unknown action %q", name)
		}
		if w < 0 {
			return fmt.Errorf("chaos: negative weight for %q", name)
		}
	}
	if c.DelayMs < 0 {
		return fmt.Errorf("chaos: negative delay_ms")
	}
	return nil
}

func (c ChaosConfig) delayMs() int {
	if c.DelayMs == 0 {
		return 3000
	}
	return c.DelayMs
}

// Pick is the action for one execution: the same seed, prompt and request seed always give the
// same action, whichever mock of a pool receives the request.
func (c ChaosConfig) Pick(promptKey string, requestSeed *int, stream bool) string {
	weights := c.Weights
	if len(weights) == 0 {
		weights = DefaultChaosWeights()
	}
	names := make([]string, 0, len(weights))
	total := 0
	for name, w := range weights {
		if w <= 0 || (!stream && streamOnlyChaos[name]) {
			continue
		}
		names = append(names, name)
		total += w
	}
	if total == 0 {
		return ChaosOK
	}
	sort.Strings(names)
	seed := "none"
	if requestSeed != nil {
		seed = fmt.Sprint(*requestSeed)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s", c.Seed, promptKey, seed)))
	n := int(binary.BigEndian.Uint64(sum[:8]) % uint64(total))
	for _, name := range names {
		n -= weights[name]
		if n < 0 {
			return name
		}
	}
	return names[len(names)-1]
}

// forgeLogprobs keeps the tokens and replaces every logprob with values the replay will not give.
func forgeLogprobs(content []map[string]any) {
	for _, entry := range content {
		entry["logprob"] = -20.0
		if tops, ok := entry["top_logprobs"].([]map[string]any); ok {
			for i, t := range tops {
				t["logprob"] = -20.0 - float64(i)
			}
		}
	}
}

// oddUsage is the completion count an odd_usage answer reports for n tokens sent.
func oddUsage(n int) int { return 3*n + 7 }
