package filters

import (
	"strings"
	"testing"
)

func normalizeForTest(t *testing.T, body string) Result {
	t.Helper()
	result, err := NormalizeRequest([]byte(body), Options{
		DefaultMaxTokens: 3072,
		MaxTokensCap:     4096,
		RoutedModel:      "model-a",
	})
	if err != nil {
		t.Fatalf("NormalizeRequest(%s) = %v, want acceptance", body, err)
	}
	return result
}

// Test flow:
//  1. Normalize request bodies that vary the client's stream and stream_options fields (absent, false, true, true+usage, false+usage).
//  2. Assert the body sent upstream always asks for stream:true and stream_options include_usage:true.
//  3. Assert the reported ClientStream and ClientUsage match what each case's client actually asked for.
func TestForcesStreamingUpstream(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name             string
		body             string
		wantClientStream bool
		wantClientUsage  bool
	}{
		{
			name: "a client that said nothing about streaming",
			body: `{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "a client that asked for a buffered reply",
			body: `{"model":"model-a","messages":[{"role":"user","content":"hi"}],"stream":false}`,
		},
		{
			name:             "a streaming client that did not ask for usage",
			body:             `{"model":"model-a","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantClientStream: true,
		},
		{
			name:             "a streaming client that asked for usage",
			body:             `{"model":"model-a","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`,
			wantClientStream: true,
			wantClientUsage:  true,
		},
		{
			name: "a buffered client whose stream_options the validation stage drops",
			body: `{"model":"model-a","messages":[{"role":"user","content":"hi"}],"stream":false,"stream_options":{"include_usage":true}}`,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := normalizeForTest(t, testCase.body)

			if !strings.Contains(string(result.Body), `"stream":true`) {
				t.Errorf("body = %s, want stream:true asked of the host", result.Body)
			}
			if !strings.Contains(string(result.Body), `"stream_options":{"include_usage":true}`) {
				t.Errorf("body = %s, want include_usage asked of the host", result.Body)
			}
			if result.ClientStream != testCase.wantClientStream {
				t.Errorf("ClientStream = %v, want %v", result.ClientStream, testCase.wantClientStream)
			}
			if result.ClientUsage != testCase.wantClientUsage {
				t.Errorf("ClientUsage = %v, want %v", result.ClientUsage, testCase.wantClientUsage)
			}
		})
	}
}

// Test flow:
//  1. Normalize a request whose client set stream_options to include_usage:false plus an unlisted sub-field.
//  2. Assert the unlisted sub-field is gone from the body sent upstream.
//  3. Assert the body still forces include_usage:true upstream.
//  4. Assert ClientUsage reports false, matching the client's own include_usage:false.
func TestForcedStreamOptionsReplaceWhatTheClientSent(t *testing.T) {
	t.Parallel()

	result := normalizeForTest(t, `{"model":"model-a","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":false,"continuous_usage_stats":true}}`)

	if strings.Contains(string(result.Body), "continuous_usage_stats") {
		t.Errorf("body = %s, want the unlisted sub-field gone", result.Body)
	}
	if !strings.Contains(string(result.Body), `"stream_options":{"include_usage":true}`) {
		t.Errorf("body = %s, want include_usage asked of the host", result.Body)
	}
	if result.ClientUsage {
		t.Error("ClientUsage = true, want false: the client asked for include_usage:false")
	}
}

// Test flow:
//  1. Feed a stream rewriter SSE chunks that vary content-only, usage-only, content-with-usage events and the [DONE] terminator, with keepUsage toggled per case.
//  2. Assert the rewritten output matches each case's expected chunk, dropping forced usage only when the client did not ask to keep it.
func TestStreamRewriterDropsForcedUsage(t *testing.T) {
	t.Parallel()
	const contentEvent = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	const usageOnlyEvent = "data: {\"choices\":[],\"usage\":{\"completion_tokens\":2}}\n\n"
	const contentWithUsageEvent = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}],\"usage\":{\"completion_tokens\":2}}\n\n"
	testCases := []struct {
		name      string
		keepUsage bool
		stream    string
		want      string
	}{
		{
			name:   "the usage-only event disappears for a client that did not ask",
			stream: contentEvent + usageOnlyEvent,
			want:   contentEvent,
		},
		{
			name:      "the usage-only event survives for a client that asked",
			keepUsage: true,
			stream:    contentEvent + usageOnlyEvent,
			want:      contentEvent + usageOnlyEvent,
		},
		{
			name:   "usage riding along with content is removed, the content is not",
			stream: contentWithUsageEvent,
			want:   "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0}]}\n\n",
		},
		{
			name:   "the terminator is not usage",
			stream: string(SSEDoneEvent),
			want:   string(SSEDoneEvent),
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := NewStreamRewriter(LogprobIntent{}, testCase.keepUsage).Write([]byte(testCase.stream))
			if err != nil {
				t.Fatalf("Write() = %v, want no error", err)
			}
			if string(got) != testCase.want {
				t.Errorf("rewritten\n got: %q\nwant: %q", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Write an SSE error event that carries no choices but includes a usage field.
//  2. Assert the rewritten output still contains the error message.
//  3. Assert the usage field is stripped from the output.
func TestStreamRewriterKeepsAnErrorEventThatCarriesUsage(t *testing.T) {
	t.Parallel()
	event := []byte("data: {\"error\":{\"message\":\"upstream exploded\"},\"usage\":{\"completion_tokens\":1}}\n\n")

	got, err := NewStreamRewriter(LogprobIntent{}, false).Write(event)
	if err != nil {
		t.Fatalf("Write() = %v, want no error", err)
	}
	if !strings.Contains(string(got), "upstream exploded") {
		t.Fatalf("rewritten = %q, want the host's error preserved", got)
	}
	if strings.Contains(string(got), "usage") {
		t.Errorf("rewritten = %q, want the usage gone", got)
	}
}

// Test flow:
//  1. Write an SSE content event whose usage field is null.
//  2. Assert the rewritten output no longer mentions usage.
func TestStreamRewriterRemovesANullUsage(t *testing.T) {
	t.Parallel()
	event := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}],\"usage\":null}\n\n")

	got, err := NewStreamRewriter(LogprobIntent{}, false).Write(event)
	if err != nil {
		t.Fatalf("Write() = %v, want no error", err)
	}
	if strings.Contains(string(got), "usage") {
		t.Errorf("rewritten = %q, want the null usage gone", got)
	}
}

// Test flow:
//  1. Write a usage-only SSE chunk without its trailing blank-line terminator.
//  2. Close the rewriter.
//  3. Assert Close returns no error for the dropped usage tail.
//  4. Assert Close emits nothing.
func TestStreamRewriterCloseSeparatesADropFromATruncation(t *testing.T) {
	t.Parallel()
	rewriter := NewStreamRewriter(LogprobIntent{}, false)
	if _, err := rewriter.Write([]byte("data: {\"choices\":[],\"usage\":{\"completion_tokens\":2}}")); err != nil {
		t.Fatalf("Write() = %v, want no error", err)
	}

	tail, err := rewriter.Close()
	if err != nil {
		t.Fatalf("Close() = %v, want no error for a dropped usage tail", err)
	}
	if len(tail) != 0 {
		t.Errorf("Close() = %q, want nothing emitted", tail)
	}
}

// Test flow:
//  1. Normalize requests with KeepClientStream set, varying the client's stream field between false and true.
//  2. Assert the body sent upstream preserves the client's own stream value.
//  3. Assert no stream_options field is forced into the body.
func TestKeepingTheClientStreamSendsWhatTheClientAsked(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		body       string
		wantStream string
	}{
		{
			name:       "a client that asked for a buffered reply",
			body:       `{"model":"model-a","messages":[{"role":"user","content":"hi"}],"stream":false}`,
			wantStream: `"stream":false`,
		},
		{
			name:       "a streaming client",
			body:       `{"model":"model-a","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantStream: `"stream":true`,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, err := NormalizeRequest([]byte(testCase.body), Options{
				KeepClientStream: true,
				DefaultMaxTokens: 3072,
				MaxTokensCap:     4096,
				RoutedModel:      "model-a",
			})

			if err != nil {
				t.Fatalf("NormalizeRequest(%s) = %v, want acceptance", testCase.body, err)
			}
			if !strings.Contains(string(result.Body), testCase.wantStream) {
				t.Errorf("body = %s, want %s", result.Body, testCase.wantStream)
			}
			if strings.Contains(string(result.Body), `"stream_options"`) {
				t.Errorf("body = %s, want no forced stream_options", result.Body)
			}
		})
	}
}

// Test flow:
//  1. Normalize a request with KeepClientStream set whose client body never mentions "stream".
//  2. Assert the body sent upstream still has no "stream" field.
func TestKeepingTheClientStreamLeavesAnUnaskedRequestAlone(t *testing.T) {
	t.Parallel()

	result, err := NormalizeRequest([]byte(`{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`), Options{
		KeepClientStream: true,
		DefaultMaxTokens: 3072,
		MaxTokensCap:     4096,
		RoutedModel:      "model-a",
	})

	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
	}
	if strings.Contains(string(result.Body), `"stream"`) {
		t.Errorf("body = %s, want the absent field left absent", result.Body)
	}
}
