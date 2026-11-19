package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

const (
	cachedTokensHostPromptTokens = 9000
	cachedTokensBeyondAnyCount   = 1 << 20
	cachedTokensWithinCount      = 3
)

// Test flow:
//  1. For a streaming and a non-streaming chat completion, make the fake host race answer with a content chunk and a usage chunk whose prompt_tokens_details.cached_tokens is either within or beyond the gateway's prompt count.
//  2. POST the request and read the usage object the client received.
//  3. Assert cached_tokens is the host's value when within the gateway's count and the gateway's count when beyond it.
//  4. Assert the host's prompt_tokens reaches the client unchanged.
func TestTheClientSeesTheHostsCachedPromptTokensBoundedByTheGatewaysCount(t *testing.T) {
	requests := []struct {
		name string
		body string
	}{
		{name: "streaming", body: `{"model":"qwen","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`},
		{name: "non-streaming", body: chatBody},
	}
	reports := []struct {
		name           string
		hostCached     uint64
		boundedByCount bool
	}{
		{name: "within the count", hostCached: cachedTokensWithinCount},
		{name: "beyond the count", hostCached: cachedTokensBeyondAnyCount, boundedByCount: true},
	}
	for _, request := range requests {
		for _, report := range reports {
			t.Run(request.name+", "+report.name, func(t *testing.T) {
				live := newHarness(t)
				live.inference.reply = "data: {\"id\":\"resp\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
					fmt.Sprintf("data: {\"id\":\"resp\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":1,\"prompt_tokens_details\":{\"cached_tokens\":%d}}}\n\n", cachedTokensHostPromptTokens, report.hostCached) +
					"data: [DONE]\n\n"

				recorder := live.request(t, http.MethodPost, "/v1/chat/completions", request.body, nil)

				if recorder.Code != http.StatusOK {
					t.Fatalf("status: got %d (%s)", recorder.Code, recorder.Body.String())
				}
				raced := live.inference.raced.Load()
				if raced == nil {
					t.Fatal("the request never reached the race")
				}
				want := report.hostCached
				if report.boundedByCount {
					want = raced.InputTokens
				}
				if want >= cachedTokensBeyondAnyCount {
					t.Fatalf("the gateway counted %d prompt tokens, which leaves nothing beyond the count to bound", raced.InputTokens)
				}
				usage := clientUsage(t, recorder.Body.Bytes())
				if usage.PromptTokens != cachedTokensHostPromptTokens {
					t.Fatalf("prompt_tokens: got %d, want the host's %d (%s)", usage.PromptTokens, cachedTokensHostPromptTokens, recorder.Body.String())
				}
				if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens == nil {
					t.Fatalf("cached_tokens did not reach the client: %s", recorder.Body.String())
				}
				if got := *usage.PromptTokensDetails.CachedTokens; got != want {
					t.Fatalf("cached_tokens: got %d, want %d (gateway count %d, host reported %d)", got, want, raced.InputTokens, report.hostCached)
				}
			})
		}
	}
}

type clientUsageView struct {
	PromptTokens        uint64 `json:"prompt_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *uint64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func clientUsage(t *testing.T, body []byte) clientUsageView {
	t.Helper()
	documents := [][]byte{body}
	if bytes.HasPrefix(body, []byte("data: ")) {
		documents = nil
		for event := range bytes.SplitSeq(body, []byte("\n\n")) {
			payload := bytes.TrimPrefix(event, []byte("data: "))
			if bytes.Contains(payload, []byte(`"usage"`)) {
				documents = append(documents, payload)
			}
		}
	}
	if len(documents) != 1 {
		t.Fatalf("want exactly one document carrying usage, got %d: %s", len(documents), body)
	}
	var reply struct {
		Usage *clientUsageView `json:"usage"`
	}
	if err := json.Unmarshal(documents[0], &reply); err != nil || reply.Usage == nil {
		t.Fatalf("no usage object in %s (%v)", documents[0], err)
	}
	return *reply.Usage
}
