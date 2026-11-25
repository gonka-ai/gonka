package filters

import (
	"bytes"
	stdjson "encoding/json"
	"testing"
)

const cachedTokensGatewayCount = 100

var cachedTokensCases = []struct {
	name      string
	usage     string
	wantUsage string
}{
	{
		name:      "within the gateway's count",
		usage:     `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":64}}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120,"prompt_tokens_details":{"cached_tokens":64}}`,
	},
	{
		name:      "details absent",
		usage:     `{"prompt_tokens":120,"completion_tokens":1}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120}`,
	},
	{
		name:      "details null",
		usage:     `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":null}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120,"prompt_tokens_details":null}`,
	},
	{
		name:      "cached_tokens null",
		usage:     `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":null}}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120,"prompt_tokens_details":{"cached_tokens":null}}`,
	},
	{
		name:      "cached_tokens absent beside another detail",
		usage:     `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":{"audio_tokens":0}}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120,"prompt_tokens_details":{"audio_tokens":0}}`,
	},
	{
		name:      "larger than the gateway's count",
		usage:     `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":112}}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120,"prompt_tokens_details":{"cached_tokens":100}}`,
	},
	{
		name:      "beyond the integer range",
		usage:     `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":99999999999999999999}}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120,"prompt_tokens_details":{"cached_tokens":100}}`,
	},
	{
		name:      "negative",
		usage:     `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":-3}}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120,"prompt_tokens_details":{"cached_tokens":0}}`,
	},
	{
		name:      "a fraction",
		usage:     `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":2.5}}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120,"prompt_tokens_details":{}}`,
	},
	{
		name:      "a string",
		usage:     `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":"64"}}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120,"prompt_tokens_details":{}}`,
	},
	{
		name:      "beyond any integer",
		usage:     `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":1e400}}`,
		wantUsage: `{"completion_tokens":1,"prompt_tokens":120,"prompt_tokens_details":{}}`,
	},
}

// Test flow:
//  1. For each usage a host may report, stream a content chunk and a usage chunk through a rewriter that keeps usage, built with the gateway's prompt count.
//  2. Decode the usage of the event the client receives.
//  3. Assert it equals the case's expected usage: cached_tokens within 0..count kept, clamped beyond it, deleted when not an integer, and nothing synthesised where the host sent nothing.
func TestTheStreamRewriterBoundsCachedTokensToTheGatewaysCount(t *testing.T) {
	for _, testCase := range cachedTokensCases {
		t.Run(testCase.name, func(t *testing.T) {
			rewriter := NewStreamRewriter(LogprobIntent{}, true, cachedTokensGatewayCount)
			stream := []byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
				"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":" + testCase.usage + "}\n\n")
			written, err := rewriter.Write(stream)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			var usageEvent []byte
			for event := range bytes.SplitSeq(written, []byte("\n\n")) {
				if bytes.Contains(event, usageKey) {
					usageEvent = bytes.TrimPrefix(event, sseDataPrefix)
				}
			}
			if got := usageOf(t, usageEvent); got != testCase.wantUsage {
				t.Fatalf("usage: got %s, want %s", got, testCase.wantUsage)
			}
		})
	}
}

// Test flow:
//  1. For each usage a host may report, fold it into one body three ways: a chunk stream, a whole chat.completion event, and an unframed JSON body, each through a folder built with the gateway's prompt count.
//  2. Decode the usage of the body the client receives.
//  3. Assert it equals the case's expected usage on every path.
func TestTheBodyFolderBoundsCachedTokensToTheGatewaysCount(t *testing.T) {
	for _, testCase := range cachedTokensCases {
		deliveries := map[string]string{
			"chunk stream": "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
				"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":" + testCase.usage + "}\n\ndata: [DONE]\n\n",
			"whole completion event": "data: {\"id\":\"x\",\"object\":\"chat.completion\",\"choices\":[{\"index\":0,\"message\":{\"content\":\"hi\"}}],\"usage\":" + testCase.usage + "}\n\ndata: [DONE]\n\n",
			"unframed body":          "{\"id\":\"x\",\"object\":\"chat.completion\",\"choices\":[{\"index\":0,\"message\":{\"content\":\"hi\"}}],\"usage\":" + testCase.usage + "}",
		}
		for delivery, body := range deliveries {
			t.Run(testCase.name+", "+delivery, func(t *testing.T) {
				folder := NewBodyFolder(LogprobIntent{}, cachedTokensGatewayCount)
				if _, err := folder.Write([]byte(body)); err != nil {
					t.Fatalf("Write: %v", err)
				}
				if got := usageOf(t, folder.Body()); got != testCase.wantUsage {
					t.Fatalf("usage: got %s, want %s", got, testCase.wantUsage)
				}
			})
		}
	}
}

// Test flow:
//  1. Build a usage whose prompt_tokens_details repeats cached_tokens, an out-of-range value first and an in-range one last.
//  2. Send it through a rewriter and a folder built with the gateway's prompt count.
//  3. Assert each client body carries cached_tokens exactly once, with the in-range value.
func TestARepeatedCachedTokensKeyReachesTheClientOnce(t *testing.T) {
	usage := `{"prompt_tokens":120,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":999999,"cached_tokens":64}}`
	event := "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":" + usage + "}\n\n"
	streamed, err := NewStreamRewriter(LogprobIntent{}, true, cachedTokensGatewayCount).Write([]byte(event))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	folder := NewBodyFolder(LogprobIntent{}, cachedTokensGatewayCount)
	if _, err := folder.Write([]byte(event + "data: [DONE]\n\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for name, body := range map[string][]byte{"streamed": streamed, "folded": folder.Body()} {
		if count := bytes.Count(body, cachedTokensKey); count != 1 {
			t.Fatalf("%s: cached_tokens appears %d times, want once: %s", name, count, body)
		}
		if !bytes.Contains(body, []byte(`"cached_tokens":64`)) {
			t.Fatalf("%s: want the value a decoder reads, 64: %s", name, body)
		}
	}
}

func usageOf(t *testing.T, payload []byte) string {
	t.Helper()
	decoder := stdjson.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var document struct {
		Usage map[string]any `json:"usage"`
	}
	if err := decoder.Decode(&document); err != nil || document.Usage == nil {
		t.Fatalf("no usage in %s (%v)", payload, err)
	}
	encoded, err := stdjson.Marshal(document.Usage)
	if err != nil {
		t.Fatalf("encode usage: %v", err)
	}
	return string(encoded)
}
