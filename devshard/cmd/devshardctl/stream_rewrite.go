package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"common/completionapi"
	"devshard/internal/jsonshape"
)

var streamingDroppedShapeTotal atomic.Uint64

var streamingEventShapeLimits = foldEventShapeLimits

// clientResponseIntent is what the client asked for in fields a host answers with regardless; zero keeps nothing.
type clientResponseIntent struct {
	keepLogprobs    bool
	keepTopLogprobs bool
	keepUsage       bool
}

func clientResponseIntentFromRequest(req chatRequest) clientResponseIntent {
	// The host forwards by this same predicate, so neither side can start showing what the other hides.
	askedForLogprobs := completionapi.LogprobsAsked(req.Logprobs, float64(req.TopLogprobs))
	return clientResponseIntent{
		keepLogprobs:    askedForLogprobs,
		keepTopLogprobs: askedForLogprobs,
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

// A forwarded request carries a normalized body, whose capped width says less than the client wrote.
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
	Content json.RawMessage `json:"content"`
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
	out, _ := rewriteStreamingPayloadChecked(p, intent)
	return out
}

func rewriteStreamingPayloadChecked(p []byte, intent clientResponseIntent) ([]byte, error) {
	var shapeErr error
	needsCompletionRewrite := bytes.Contains(p, []byte(`"message"`))
	needsInternalFieldsFilter := containsAnyInternalField(p) || bytes.Contains(p, []byte(`\u`))
	needsUsageStrip := !intent.keepUsage && bytes.Contains(p, []byte(`"usage"`))

	if bytes.Count(p, []byte("\n\n")) <= 1 && !needsCompletionRewrite && !needsInternalFieldsFilter && !needsUsageStrip {
		payload, ok := sseEventData(bytes.TrimRight(p, "\r\n"))
		if !ok || payload[0] != '{' {
			return p, nil
		}
		if _, err := jsonshape.Check(payload, streamingEventShapeLimits); err == nil {
			return p, nil
		}
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
		payload, ok := sseEventData(event)
		if !ok || payload[0] != '{' {
			out.Write(eventChunk)
			continue
		}
		if _, err := jsonshape.Check(payload, streamingEventShapeLimits); err != nil {
			streamingDroppedShapeTotal.Add(1)
			shapeErr = fmt.Errorf("%w: %w", ErrAggregateFoldTooLarge, err)
			changed = true
			break
		}
		if needsCompletionRewrite && bytes.Contains(payload, []byte(`"message"`)) {
			rewritten, err := rewriteStreamingDataEvent(payload, intent)
			if err != nil {
				streamingDroppedShapeTotal.Add(1)
				shapeErr = err
				changed = true
				break
			}
			if rewritten != nil {
				changed = true
				out.Write(rewritten)
				continue
			}
		}
		if !needsInternalFieldsFilter && !needsUsageStrip {
			out.Write(eventChunk)
			continue
		}
		filtered, err := filterClientEvent(payload, intent)
		if err != nil {
			streamingDroppedShapeTotal.Add(1)
			shapeErr = err
			changed = true
			break
		}
		if filtered == nil {
			changed = true
			continue
		}
		if !bytes.Equal(filtered, payload) {
			fmt.Fprintf(&out, "data: %s\n\n", filtered)
			changed = true
			continue
		}
		out.Write(eventChunk)
	}
	if !changed {
		return p, nil
	}
	return out.Bytes(), shapeErr
}

func rewriteStreamingDataEvent(payload []byte, intent clientResponseIntent) ([]byte, error) {
	if _, err := jsonshape.Check(payload, foldEventShapeLimits); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAggregateFoldTooLarge, err)
	}
	if !json.Valid(payload) {
		return nil, nil
	}
	choicesWithinLimit := true
	jsonshape.Object(payload, func(key, value []byte) bool {
		if jsonshape.KeyEqualFold(key, "choices") {
			_, err := jsonshape.Check(value, jsonshape.Limits{RootMembers: aggregateMaxChoicesPerEvent})
			choicesWithinLimit = err == nil
		}
		return choicesWithinLimit
	})
	if !choicesWithinLimit {
		return nil, ErrAggregateFoldTooLarge
	}
	var resp streamingRewritePayload
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, nil
	}
	if len(resp.Choices) == 0 {
		return nil, nil
	}
	convertible := false
	for _, choice := range resp.Choices {
		if choice.Message != nil && choice.Message.Content != "" {
			convertible = true
			break
		}
	}
	if !convertible {
		return nil, nil
	}

	maxOutput := 4*len(payload) + (8 << 20)
	var out bytes.Buffer
	for _, choice := range resp.Choices {
		if choice.Message == nil {
			continue
		}
		if role := choice.Message.Role; role != "" {
			writeStreamingChunkEvent(&out, resp, choice.Index, map[string]any{"role": role}, nil, nil, nil)
		}

		if choice.Logprobs != nil {
			tokens := choice.Logprobs.Content
			repeatedBytes := len(resp.ID) + len(resp.Model) + len(resp.SystemFingerprint)
			stop := bytes.TrimSpace(choice.StopReason)
			if !bytes.Equal(stop, []byte("null")) {
				repeatedBytes += len(stop)
			}
			if repeatedBytes > 0 {
				limit := (maxOutput - out.Len()) / repeatedBytes
				if limit <= 0 {
					return nil, ErrAggregateFoldTooLarge
				}
				if _, err := jsonshape.Check(tokens, jsonshape.Limits{RootMembers: limit}); err != nil {
					return nil, fmt.Errorf("%w: %w", ErrAggregateFoldTooLarge, err)
				}
			}
			var previous rewriteLogprob
			hasToken := false
			var tokenErr error
			jsonshape.Array(tokens, func(raw []byte) bool {
				if hasToken {
					writeStreamingChunkEvent(&out, resp, choice.Index, map[string]any{"content": previous.Token}, nil, choice.StopReason, reconstructChunkLogprobs(previous, intent))
				}
				previous = rewriteLogprob{}
				if intent.keepLogprobs {
					_, err := jsonshape.Check(raw, jsonshape.Limits{Nodes: aggregateMaxValueNodes})
					if err != nil {
						tokenErr = fmt.Errorf("%w: %w", ErrAggregateFoldTooLarge, err)
						return false
					}
					if err := json.Unmarshal(raw, &previous); err != nil {
						tokenErr = err
						return false
					}
					for _, top := range previous.TopLogprobs {
						stripClientInternalFields(top, internalStrippedFields)
					}
				} else if rawToken := jsonshape.FieldFold(raw, "token"); len(rawToken) > 0 {
					if err := json.Unmarshal(rawToken, &previous.Token); err != nil {
						tokenErr = err
						return false
					}
				}
				hasToken = true
				if out.Len() > maxOutput {
					tokenErr = ErrAggregateFoldTooLarge
				}
				return tokenErr == nil
			})
			if tokenErr != nil {
				if errors.Is(tokenErr, ErrAggregateFoldTooLarge) {
					return nil, tokenErr
				}
				return nil, nil
			}
			if hasToken {
				writeStreamingChunkEvent(&out, resp, choice.Index, map[string]any{"content": previous.Token}, choice.FinishReason, choice.StopReason, reconstructChunkLogprobs(previous, intent))
				if out.Len() > maxOutput {
					return nil, ErrAggregateFoldTooLarge
				}
				continue
			}
		}

		if choice.Message.Content != "" {
			writeStreamingChunkEvent(&out, resp, choice.Index, map[string]any{"content": choice.Message.Content}, nil, nil, nil)
		}
		if choice.FinishReason != nil || len(bytes.TrimSpace(choice.StopReason)) > 0 {
			writeStreamingChunkEvent(&out, resp, choice.Index, map[string]any{}, choice.FinishReason, choice.StopReason, nil)
		}
	}

	trimmedUsage := bytes.TrimSpace(resp.Usage)
	if intent.keepUsage && len(trimmedUsage) > 0 && !bytes.Equal(trimmedUsage, []byte("null")) {
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
	if out.Len() > maxOutput {
		return nil, ErrAggregateFoldTooLarge
	}
	return out.Bytes(), nil
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

// strippedFields returns what to remove for this client: the internal fields always, the logprobs unless it asked.
func (intent clientResponseIntent) strippedFields() []string {
	fields := append([]string(nil), internalStrippedFields...)
	if !intent.keepLogprobs {
		fields = append(fields, "logprob", "logprobs")
	}
	return fields
}

func containsAnyInternalField(p []byte) bool {
	return bytes.Contains(p, []byte(`"logprob`)) ||
		bytes.Contains(p, []byte(`"token_ids"`)) ||
		bytes.Contains(p, []byte(`"prompt_logprobs"`)) ||
		bytes.Contains(p, []byte(`"prompt_token_ids"`))
}

func filterClientInternalFields(payload []byte, intent clientResponseIntent) ([]byte, error) {
	out, _, err := rewriteClientFields(payload, intent, false)
	return out, err
}

func filterClientEvent(payload []byte, intent clientResponseIntent) ([]byte, error) {
	out, hadUsage, err := rewriteClientFields(payload, intent, !intent.keepUsage)
	if err != nil {
		return nil, err
	}
	if hadUsage {
		choices := bytes.TrimSpace(jsonshape.Field(out, "choices"))
		if len(choices) < 2 || choices[0] != '[' || len(bytes.TrimSpace(choices[1:len(choices)-1])) == 0 {
			return nil, nil
		}
	}
	return out, nil
}

func rewriteClientFields(payload []byte, intent clientResponseIntent, stripUsage bool) ([]byte, bool, error) {
	fields := intent.strippedFields()
	hadUsage := false
	out, _, err := jsonshape.Rewrite(payload, func(key []byte, depth int) (string, bool) {
		if stripUsage && depth == 1 && jsonshape.KeyEqual(key, "usage") {
			hadUsage = true
			return "", true
		}
		for _, name := range fields {
			if jsonshape.KeyEqual(key, name) {
				return "", true
			}
		}
		if intent.keepLogprobs && !intent.keepTopLogprobs && jsonshape.KeyEqual(key, "top_logprobs") {
			return "[]", true
		}
		return "", false
	})
	switch {
	case errors.Is(err, jsonshape.ErrInvalid):
		return payload, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("%w: %w", ErrAggregateFoldTooLarge, err)
	}
	return out, hadUsage, nil
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
