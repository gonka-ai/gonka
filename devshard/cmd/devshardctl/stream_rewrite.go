package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// clientResponseIntent is what the client ORIGINALLY asked for in fields the gateway force-enables
// upstream for its own purposes, so the response strip can hand back only those. Zero keeps nothing.
type clientResponseIntent struct {
	keepLogprobs    bool
	keepTopLogprobs bool
	keepUsage       bool
}

func clientResponseIntentFromRequest(req chatRequest) clientResponseIntent {
	return clientResponseIntent{
		keepLogprobs:    req.Logprobs,
		keepTopLogprobs: req.Logprobs && req.TopLogprobs > 0,
		keepUsage:       req.IncludeUsage,
	}
}

type clientResponseIntentContextKey struct{}

func withClientResponseIntent(ctx context.Context, intent clientResponseIntent) context.Context {
	return context.WithValue(ctx, clientResponseIntentContextKey{}, intent)
}

func clientResponseIntentFromContext(ctx context.Context) (clientResponseIntent, bool) {
	intent, recorded := ctx.Value(clientResponseIntentContextKey{}).(clientResponseIntent)
	return intent, recorded
}

// A forwarded request carries a normalized body, whose forced logprobs say nothing about the client.
func resolveClientResponseIntent(ctx context.Context, req chatRequest) clientResponseIntent {
	if intent, recorded := clientResponseIntentFromContext(ctx); recorded {
		return intent
	}
	return clientResponseIntentFromRequest(req)
}

type streamingRewritePayload struct {
	ID                string          `json:"id"`
	Object            string          `json:"object"`
	Created           int64           `json:"created"`
	Model             string          `json:"model"`
	SystemFingerprint string          `json:"system_fingerprint,omitempty"`
	Choices           []rewriteChoice `json:"choices"`
	Usage             json.RawMessage `json:"usage"`
}

type rewriteChoice struct {
	Index        int              `json:"index"`
	Message      *rewriteMessage  `json:"message"`
	Logprobs     *rewriteLogprobs `json:"logprobs"`
	FinishReason *string          `json:"finish_reason"`
	StopReason   json.RawMessage  `json:"stop_reason"`
}

type rewriteMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type rewriteLogprobs struct {
	Content []rewriteLogprob `json:"content"`
}

type rewriteLogprob struct {
	Token       string           `json:"token"`
	Logprob     float64          `json:"logprob"`
	Bytes       []int            `json:"bytes"`
	TopLogprobs []map[string]any `json:"top_logprobs"`
}

// rewriteStreamingPayload is only for the proxy's streaming path. In the hot
// path it returns bytes unchanged. It also strips logprob payloads from
// client-facing SSE events; devshard internals can still use the original host
// response before this proxy boundary.
//
// Only if a host sent SSE-wrapped chat.completion JSON do we synthesize
// chat.completion.chunk events for the client. The synthetic role chunk exists
// only in that streaming rewrite.
func rewriteStreamingPayload(p []byte, intent clientResponseIntent) []byte {
	needsCompletionRewrite := bytes.Contains(p, []byte(`data: {`)) && bytes.Contains(p, []byte(`"message"`))
	needsInternalFieldsFilter := containsAnyInternalField(p)
	needsUsageStrip := !intent.keepUsage && bytes.Contains(p, []byte(`"usage"`))
	if !needsCompletionRewrite && !needsInternalFieldsFilter && !needsUsageStrip {
		return p
	}

	var out bytes.Buffer
	changed := false
	for _, eventChunk := range bytes.SplitAfter(p, []byte("\n\n")) {
		if len(eventChunk) == 0 {
			continue
		}
		event := bytes.TrimRight(eventChunk, "\r\n")
		if len(event) == 0 {
			out.Write(eventChunk)
			continue
		}
		if bytes.Equal(event, []byte("data: [DONE]")) || !bytes.HasPrefix(event, []byte("data: {")) {
			out.Write(eventChunk)
			continue
		}
		payload := bytes.TrimSpace(event[len("data: "):])
		usageStripped := false
		if !intent.keepUsage {
			stripped, ok := stripUsageChunk(payload)
			if ok {
				changed, usageStripped = true, true
				if stripped == nil {
					continue
				}
				payload = stripped
			}
		}
		rewritten, ok := rewriteStreamingDataEvent(payload, intent)
		if !ok {
			filtered := filterClientInternalFields(payload, intent)
			if usageStripped || !bytes.Equal(filtered, payload) {
				fmt.Fprintf(&out, "data: %s\n\n", filtered)
				changed = true
				continue
			}
			out.Write(eventChunk)
			continue
		}
		changed = true
		out.Write(rewritten)
	}
	if !changed {
		return p
	}
	return out.Bytes()
}

func rewriteStreamingDataEvent(payload []byte, intent clientResponseIntent) ([]byte, bool) {
	var resp streamingRewritePayload
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, false
	}
	if len(resp.Choices) == 0 {
		return nil, false
	}
	convertible := false
	for _, choice := range resp.Choices {
		if choice.Message != nil && choice.Message.Content != "" {
			convertible = true
			break
		}
	}
	if !convertible {
		return nil, false
	}

	var out bytes.Buffer
	for _, choice := range resp.Choices {
		if choice.Message == nil {
			continue
		}
		if role := choice.Message.Role; role != "" {
			writeStreamingChunkEvent(&out, resp, choice.Index, map[string]any{"role": role}, nil, nil, nil)
		}

		tokens := []rewriteLogprob(nil)
		if choice.Logprobs != nil {
			tokens = choice.Logprobs.Content
		}
		if len(tokens) > 0 {
			for i, token := range tokens {
				delta := map[string]any{"content": token.Token}
				var finish *string
				if i == len(tokens)-1 {
					finish = choice.FinishReason
				}
				writeStreamingChunkEvent(&out, resp, choice.Index, delta, finish, choice.StopReason, reconstructChunkLogprobs(token, intent))
			}
			continue
		}

		if choice.Message.Content != "" {
			writeStreamingChunkEvent(&out, resp, choice.Index, map[string]any{"content": choice.Message.Content}, nil, nil, nil)
		}
		if choice.FinishReason != nil || len(bytes.TrimSpace(choice.StopReason)) > 0 {
			writeStreamingChunkEvent(&out, resp, choice.Index, map[string]any{}, choice.FinishReason, choice.StopReason, nil)
		}
	}

	trimmedUsage := bytes.TrimSpace(resp.Usage)
	if len(trimmedUsage) > 0 && !bytes.Equal(trimmedUsage, []byte("null")) {
		evt := map[string]any{
			"id":      resp.ID,
			"object":  "chat.completion.chunk",
			"created": resp.Created,
			"model":   resp.Model,
			"choices": []any{},
		}
		if resp.SystemFingerprint != "" {
			evt["system_fingerprint"] = resp.SystemFingerprint
		}
		evt["usage"] = json.RawMessage(trimmedUsage)
		b, err := json.Marshal(evt)
		if err == nil {
			fmt.Fprintf(&out, "data: %s\n\n", b)
		}
	}
	return out.Bytes(), true
}

func writeStreamingChunkEvent(out *bytes.Buffer, resp streamingRewritePayload, index int, delta map[string]any, finishReason *string, stopReason json.RawMessage, logprobs any) {
	choice := map[string]any{
		"index":         index,
		"delta":         delta,
		"finish_reason": finishReason,
	}
	if logprobs != nil {
		choice["logprobs"] = logprobs
	}
	if len(bytes.TrimSpace(stopReason)) > 0 && !bytes.Equal(bytes.TrimSpace(stopReason), []byte("null")) {
		choice["stop_reason"] = json.RawMessage(bytes.TrimSpace(stopReason))
	}
	evt := map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion.chunk",
		"created": resp.Created,
		"model":   resp.Model,
		"choices": []any{choice},
	}
	if resp.SystemFingerprint != "" {
		evt["system_fingerprint"] = resp.SystemFingerprint
	}
	b, err := json.Marshal(evt)
	if err != nil {
		return
	}
	fmt.Fprintf(out, "data: %s\n\n", b)
}

// reconstructChunkLogprobs rebuilds the OpenAI streaming logprobs object for a
// single synthesized content chunk, but only when the client asked for logprobs.
// When the client wanted logprobs but not top_logprobs, the top alternatives are
// emitted as an empty array (OpenAI's shape for logprobs-without-top_logprobs).
// Returns nil when the client did not request logprobs, so the chunk omits the
// field entirely.
func reconstructChunkLogprobs(token rewriteLogprob, intent clientResponseIntent) any {
	if !intent.keepLogprobs {
		return nil
	}
	entry := map[string]any{
		"token":   token.Token,
		"logprob": token.Logprob,
		"bytes":   token.Bytes,
	}
	if intent.keepTopLogprobs {
		entry["top_logprobs"] = token.TopLogprobs
	} else {
		entry["top_logprobs"] = []any{}
	}
	return map[string]any{"content": []any{entry}}
}

// internalStrippedFields are always removed from client-facing responses: they
// are devshard/vLLM internals the client never asked for and must never see.
var internalStrippedFields = []string{
	"token_ids",
	"prompt_token_ids",
	"prompt_logprobs",
}

// strippedFields returns the response fields to remove for this client. The
// internal fields above are always stripped; the logprob/logprobs payloads are
// stripped only when the client did not request logprobs -- the gateway force-
// enables logprobs upstream for validation regardless of what the client asked.
// top_logprobs is handled separately (emptied, not removed) by emptyTopLogprobs
// so the response keeps OpenAI's logprobs-without-top_logprobs shape.
func (intent clientResponseIntent) strippedFields() []string {
	fields := append([]string(nil), internalStrippedFields...)
	if !intent.keepLogprobs {
		fields = append(fields, "logprob", "logprobs")
	}
	return fields
}

// emptyTopLogprobs replaces every top_logprobs array in the response with an
// empty array. The gateway forces top_logprobs upstream for validation, so a
// client who asked for logprobs but not top_logprobs would otherwise receive the
// forced alternatives. OpenAI returns top_logprobs as a present-but-empty array
// in this case, which this mirrors (matching the streaming-rewrite path's
// reconstructChunkLogprobs). top_logprobs only appears under logprobs.content[]
// in a chat completion, so emptying every occurrence is safe.
func emptyTopLogprobs(v any) bool {
	switch typed := v.(type) {
	case map[string]any:
		changed := false
		if existing, ok := typed["top_logprobs"]; ok {
			if arr, isArr := existing.([]any); !isArr || len(arr) > 0 {
				typed["top_logprobs"] = []any{}
				changed = true
			}
		}
		for _, child := range typed {
			if emptyTopLogprobs(child) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if emptyTopLogprobs(child) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

// stripUsageChunk returns nil when the event goes entirely: a chunk left with no choices is one a
// client that never asked for usage cannot read.
func stripUsageChunk(payload []byte) ([]byte, bool) {
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return payload, false
	}
	if _, carries := decoded["usage"]; !carries {
		return payload, false
	}
	delete(decoded, "usage")
	if choices, _ := decoded["choices"].([]any); len(choices) == 0 {
		return nil, true
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		return payload, false
	}
	return out, true
}

func containsAnyInternalField(p []byte) bool {
	return bytes.Contains(p, []byte(`"logprob`)) ||
		bytes.Contains(p, []byte(`"token_ids"`)) ||
		bytes.Contains(p, []byte(`"prompt_logprobs"`)) ||
		bytes.Contains(p, []byte(`"prompt_token_ids"`))
}

func filterClientInternalFields(payload []byte, intent clientResponseIntent) []byte {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return payload
	}
	changed := stripClientInternalFields(v, intent.strippedFields())
	if intent.keepLogprobs && !intent.keepTopLogprobs {
		if emptyTopLogprobs(v) {
			changed = true
		}
	}
	if !changed {
		return payload
	}
	out, err := json.Marshal(v)
	if err != nil {
		return payload
	}
	return out
}

func stripClientInternalFields(v any, fields []string) bool {
	switch typed := v.(type) {
	case map[string]any:
		changed := false
		for _, key := range fields {
			if _, ok := typed[key]; ok {
				delete(typed, key)
				changed = true
			}
		}
		for _, child := range typed {
			if stripClientInternalFields(child, fields) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if stripClientInternalFields(child, fields) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}
