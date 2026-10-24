package mockopenai

// Replay oracle support: fault rules bound to request identity and the replay answer built from
// enforced_tokens. None of it is active unless a knob in FaultConfig or a rule asks for it.

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// FaultRule injects a fault into the requests it matches and into no others. A rule must name at
// least one of Seeds and PromptContains, so it cannot degrade into a global knob.
type FaultRule struct {
	Name string `json:"name" yaml:"name"`
	// Phase limits the rule to executions or replays; empty matches both.
	Phase string `json:"phase,omitempty" yaml:"phase,omitempty"`
	// Seeds matches the request "seed" field. Unless the client sets one, the host sends the inference
	// id as the seed of an execution and the validator replays with the same number, so a seed names
	// an inference in both phases (Phase tells them apart), and on every escrow that reached that id.
	Seeds []int `json:"seeds,omitempty" yaml:"seeds,omitempty"`
	// PromptContains matches a substring of the concatenated message contents, which is where a
	// per-request synthetic nonce lives.
	PromptContains string `json:"prompt_contains,omitempty" yaml:"prompt_contains,omitempty"`
	// Times caps how often the rule fires; 0 means every match.
	Times int `json:"times,omitempty" yaml:"times,omitempty"`

	// Actions. DelayMs runs first; then Abort or HTTPStatus.
	DelayMs    int  `json:"delay_ms,omitempty" yaml:"delay_ms,omitempty"`
	HTTPStatus int  `json:"http_status,omitempty" yaml:"http_status,omitempty"`
	Abort      bool `json:"abort,omitempty" yaml:"abort,omitempty"` // drop the connection without a response

	// PreludeChunks streams that many role-only chunks (no content) before the content, then holds the
	// stream PreludeHoldMs. Streaming requests only; the content that follows is the normal answer.
	PreludeChunks int `json:"prelude_chunks,omitempty" yaml:"prelude_chunks,omitempty"`
	PreludeHoldMs int `json:"prelude_hold_ms,omitempty" yaml:"prelude_hold_ms,omitempty"`
}

// ValidateRules checks rules the way POST /testenv/rules does, so a caller can refuse them before
// a node starts with them.
func ValidateRules(rules []FaultRule) error {
	names := make(map[string]bool, len(rules))
	for _, r := range rules {
		if err := r.validate(); err != nil {
			return err
		}
		// The stats and the request log name the rule that fired; two rules of one name would merge.
		if names[r.Name] {
			return fmt.Errorf("fault rule %q is named twice", r.Name)
		}
		names[r.Name] = true
	}
	return nil
}

func (r FaultRule) validate() error {
	if r.Name == "" {
		return errors.New("fault rule needs a name")
	}
	if len(r.Seeds) == 0 && r.PromptContains == "" {
		return fmt.Errorf("fault rule %q names neither seeds nor prompt_contains; a rule must select requests", r.Name)
	}
	switch r.Phase {
	case "", PhaseExecute, PhaseReplay:
	default:
		return fmt.Errorf("fault rule %q: unknown phase %q", r.Name, r.Phase)
	}
	if r.DelayMs <= 0 && r.HTTPStatus < 400 && !r.Abort && r.PreludeChunks <= 0 {
		return fmt.Errorf("fault rule %q has no action (delay_ms, http_status >= 400, abort or prelude_chunks)", r.Name)
	}
	if r.HTTPStatus != 0 && (r.HTTPStatus < 400 || r.HTTPStatus > 599) {
		return fmt.Errorf("fault rule %q: http_status must be between 400 and 599", r.Name)
	}
	if r.Times < 0 || r.DelayMs < 0 || r.PreludeChunks < 0 || r.PreludeHoldMs < 0 {
		return fmt.Errorf("fault rule %q: negative times, delay_ms, prelude_chunks or prelude_hold_ms", r.Name)
	}
	return nil
}

func (r FaultRule) matches(phase string, seed *int, prompt string) bool {
	if r.Phase != "" && r.Phase != phase {
		return false
	}
	if len(r.Seeds) > 0 {
		if seed == nil {
			return false
		}
		found := false
		for _, s := range r.Seeds {
			if s == *seed {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if r.PromptContains != "" && !strings.Contains(prompt, r.PromptContains) {
		return false
	}
	return true
}

type ruleEntry struct {
	rule    FaultRule
	applied int
}

func (j *journalState) setRules(rules []FaultRule) error {
	if err := ValidateRules(rules); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.rules = j.rules[:0]
	for _, r := range rules {
		j.rules = append(j.rules, &ruleEntry{rule: r})
	}
	return nil
}

// preludeOnly says whether the rule's one action is a prelude, which only a streamed answer can carry.
func (r FaultRule) preludeOnly() bool {
	return r.PreludeChunks > 0 && r.DelayMs <= 0 && r.HTTPStatus == 0 && !r.Abort
}

// matchRule returns the first rule that selects the request and still has budget, and counts the hit.
// A prelude-only rule passes over a request whose answer will not stream, so it keeps its budget.
func (j *journalState) matchRule(phase string, seed *int, prompt string, streams bool) *FaultRule {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.rules {
		if e.rule.Times > 0 && e.applied >= e.rule.Times {
			continue
		}
		if !streams && e.rule.preludeOnly() {
			continue
		}
		if !e.rule.matches(phase, seed, prompt) {
			continue
		}
		e.applied++
		r := e.rule
		return &r
	}
	return nil
}

// replayLogprobContent answers a replay from the enforced tokens exactly as they arrived: one entry
// per enforced token, the token id and its alternatives echoed, logprobs a pure function of the rank.
// topN, when positive, limits the alternatives per token the way the request's top_logprobs does.
// The same rank rule builds the original executor response, so a replay of a full sequence matches it
// and a replay of a cut sequence is shorter, which is the whole point of replaying what was received.
func replayLogprobContent(et *EnforcedTokens, topN int) []map[string]any {
	out := make([]map[string]any, 0, len(et.Tokens))
	for _, t := range et.Tokens {
		alts := t.TopTokens
		if topN > 0 && len(alts) > topN {
			alts = alts[:topN]
		}
		tops := make([]map[string]any, 0, len(alts))
		for i, alt := range alts {
			tops = append(tops, map[string]any{
				"token":   alt,
				"logprob": rankLogprob(i),
				"bytes":   utf8CodeUnits(alt),
			})
		}
		out = append(out, map[string]any{
			"token":        t.Token,
			"logprob":      rankLogprob(0),
			"bytes":        utf8CodeUnits(t.Token),
			"top_logprobs": tops,
		})
	}
	return out
}

func rankLogprob(rank int) float64 { return -0.1 - float64(rank) }

// chunkSizes splits n tokens into streamed chunks of size per (the last one may be smaller).
func chunkSizes(n, per int) []int {
	if per < 1 {
		per = 1
	}
	var out []int
	for n > 0 {
		c := per
		if n < c {
			c = n
		}
		out = append(out, c)
		n -= c
	}
	return out
}

// emitOffset is when chunk k of total goes out, measured from the request start: the first content
// at first, the last one at exec, the rest evenly between. exec <= first collapses to first.
func emitOffset(k, total int, first, exec time.Duration) time.Duration {
	if total <= 1 || exec <= first {
		return first
	}
	return first + time.Duration(int64(exec-first)*int64(k)/int64(total-1))
}
