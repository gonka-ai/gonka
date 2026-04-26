package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

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
// path it returns bytes unchanged. Only if a host sent SSE-wrapped
// chat.completion JSON do we synthesize chat.completion.chunk events for the
// client. The synthetic role chunk exists only in that streaming rewrite.
func rewriteStreamingPayload(p []byte) []byte {
	if !bytes.Contains(p, []byte(`data: {`)) || !bytes.Contains(p, []byte(`"message"`)) {
		return p
	}

	var out bytes.Buffer
	rewroteAny := false
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
		rewritten, ok := rewriteStreamingDataEvent(payload)
		if !ok {
			out.Write(eventChunk)
			continue
		}
		rewroteAny = true
		out.Write(rewritten)
	}
	if !rewroteAny {
		return p
	}
	return out.Bytes()
}

func rewriteStreamingDataEvent(payload []byte) ([]byte, bool) {
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
				writeStreamingChunkEvent(&out, resp, choice.Index, delta, []rewriteLogprob{token}, finish, choice.StopReason)
			}
			continue
		}

		if choice.Message.Content != "" {
			writeStreamingChunkEvent(&out, resp, choice.Index, map[string]any{"content": choice.Message.Content}, nil, nil, nil)
		}
		if choice.FinishReason != nil || len(bytes.TrimSpace(choice.StopReason)) > 0 {
			writeStreamingChunkEvent(&out, resp, choice.Index, map[string]any{}, nil, choice.FinishReason, choice.StopReason)
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

func writeStreamingChunkEvent(out *bytes.Buffer, resp streamingRewritePayload, index int, delta map[string]any, logprobs []rewriteLogprob, finishReason *string, stopReason json.RawMessage) {
	choice := map[string]any{
		"index":         index,
		"delta":         delta,
		"finish_reason": finishReason,
	}
	if len(logprobs) == 0 {
		choice["logprobs"] = nil
	} else {
		choice["logprobs"] = map[string]any{"content": logprobs}
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
