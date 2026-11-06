package filters

import (
	"bytes"
	"testing"
)

const plainChunkPayload = `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,` +
	`"model":"model-a","choices":[{"index":0,"delta":{"content":" the \"quoted\" \u00e9"},"finish_reason":null}]}`

// Test flow:
//  1. Take payloads that need the full rewrite: a stripped key plain, nested, escaped or in another case, a usage the client did not ask for, a complete message, a non-finite number, a second object, and an unclosed one.
//  2. Ask the fast path about each, and about a plain content chunk.
//  3. Assert it declines every one that needs the rewrite and passes only the plain chunk: a host cannot hide a field from it by spelling or nesting.
func TestCarriesNothingToRewriteDeclinesWhatTheRewriteChanges(t *testing.T) {
	testCases := []struct {
		name      string
		payload   string
		keepUsage bool
		want      bool
	}{
		{name: "a plain content chunk", payload: plainChunkPayload, want: true},
		{name: "a usage the client asked for", payload: `{"choices":[],"usage":{"total_tokens":3}}`, keepUsage: true, want: true},
		{name: "logprobs on a choice", payload: `{"choices":[{"delta":{},"logprobs":{"content":[]}}]}`, want: false},
		{name: "token ids deep inside", payload: `{"a":{"b":[{"c":{"token_ids":[1]}}]}}`, want: false},
		{name: "a stripped key", payload: `{"choices":[{"logprobs":{}}]}`, want: false},
		{name: "a key spelled with escapes", payload: `{"choices":[{"\u006cogprobs":{}}]}`, want: false},
		{name: "a usage key spelled with escapes", payload: `{"choices":[],"\u0075sage":{}}`, want: false},
		{name: "a vertical tab after the object", payload: "{}\v", want: false},
		{name: "an unterminated escaped key", payload: "{\"\\0\x05\\", want: false},
		{name: "a lone opening quote", payload: "{\"", want: false},
		{name: "a key in another case", payload: `{"choices":[{"LogProbs":{}}]}`, want: false},
		{name: "a usage the client did not ask for", payload: `{"choices":[],"usage":{"total_tokens":3}}`, want: false},
		{name: "a complete message", payload: `{"choices":[{"message":{"content":"hi"}}]}`, want: false},
		{name: "a non-finite number", payload: `{"choices":[],"value":NaN}`, want: false},
		{name: "a second object", payload: `{"a":1}{"logprobs":2}`, want: false},
		{name: "an unclosed object", payload: `{"choices":[`, want: false},
		{name: "not an object", payload: `[{"logprobs":1}]`, want: false},
		{name: "a stripped name as a value, not a key", payload: `{"choices":[{"delta":{"content":"logprobs"}}]}`, want: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := carriesNothingToRewrite([]byte(testCase.payload), testCase.keepUsage); got != testCase.want {
				t.Fatalf("carriesNothingToRewrite(%s) = %v, want %v", testCase.payload, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Generate payloads and ask the fast path about each.
//  2. Run every payload it lets through the full rewrite it skips.
//  3. Assert the full rewrite forwards that event byte for byte: the fast path only ever skips work that would have changed nothing.
func FuzzFastPathAgreesWithTheFullRewrite(f *testing.F) {
	f.Add([]byte(plainChunkPayload), false)
	f.Add([]byte(`{"choices":[{"\u006cogprobs":{}}]}`), false)
	f.Add([]byte(`{"choices":[],"usage":{"total_tokens":3}}`), true)
	f.Add([]byte(`{"choices":[{"message":{"content":"hi"}}]}`), false)
	f.Add([]byte(`{"a":{"Token_IDS":[1]},"b":"\\"}`), false)
	f.Add([]byte("{}\v"), true)
	f.Fuzz(func(t *testing.T, payload []byte, keepUsage bool) {
		if bytes.ContainsAny(payload, "\r\n") || !carriesNothingToRewrite(payload, keepUsage) {
			return
		}
		event := append([]byte("data: "), payload...)
		dataLines, decodedPayload, held := eventPayload(event)
		if !held || dataLines != 1 {
			return
		}
		rewritten, malformed := rewriteDecodedEvent(event, dataLines, decodedPayload, LogprobIntent{}, keepUsage)
		if malformed || !bytes.Equal(rewritten, event) {
			t.Fatalf("the fast path passed %q, which the full rewrite turns into %q (malformed %v)", payload, rewritten, malformed)
		}
	})
}

func BenchmarkRewriteEventPlainChunk(b *testing.B) {
	event := []byte("data: " + plainChunkPayload)
	b.ReportAllocs()
	for range b.N {
		_, _ = rewriteEvent(event, LogprobIntent{}, false)
	}
}

// Test flow:
//  1. Take chunks with no error and payloads that carry one: a named error, an error object, and both spelled with escapes or in another case.
//  2. Ask the error pre-check about each.
//  3. Assert it skips the decode only for the chunks with no error: a host cannot hide an error from the classifier by spelling it.
func TestMayCarryUpstreamErrorDeclinesOnlyPlainChunks(t *testing.T) {
	testCases := []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "a plain content chunk", payload: plainChunkPayload, want: false},
		{name: "a named error", payload: `{"error":{"message":"boom"}}`, want: true},
		{name: "an error object", payload: `{"object":"error","message":"boom"}`, want: true},
		{name: "an error key in another case", payload: `{"Error":"boom"}`, want: true},
		{name: "an error key spelled with escapes", payload: `{"\u0065rror":"boom"}`, want: true},
		{name: "an object value spelled with escapes", payload: `{"object":"\u0065rror"}`, want: true},
		{name: "an object value that is not a string", payload: `{"object":1}`, want: true},
		{name: "an object key with a value cut off", payload: `0"oBjeCt":"`, want: true},
		{name: "an unterminated escaped key", payload: "{\"\\0\x05\\", want: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := mayCarryUpstreamError([]byte(testCase.payload)); got != testCase.want {
				t.Fatalf("mayCarryUpstreamError(%s) = %v, want %v", testCase.payload, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Generate payloads and ask the error pre-check about each.
//  2. Decode every payload it skips with the full decode.
//  3. Assert that decode finds no error either: the pre-check only ever skips a decode that would have answered no error.
func FuzzErrorPreCheckAgreesWithTheDecode(f *testing.F) {
	f.Add([]byte(plainChunkPayload))
	f.Add([]byte(`{"error":{"message":"boom"}}`))
	f.Add([]byte(`{"object":"\u0065rror","message":"x"}`))
	f.Add([]byte(`{"object":"error","object":"chat.completion.chunk"}`))
	f.Add([]byte("{\"\\0\x05\\"))
	f.Add([]byte("{\"\\00\\"))
	f.Add([]byte(`0"oBjeCt":"`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if mayCarryUpstreamError(payload) {
			return
		}
		event, ok := decodeScannedEvent(payload, false)
		if !ok {
			return
		}
		if failure, held := event.upstreamError(); held {
			t.Fatalf("the pre-check skipped %q, which decodes to the error %+v", payload, failure)
		}
	})
}

func BenchmarkDecodeUpstreamErrorPlainChunk(b *testing.B) {
	payload := []byte(plainChunkPayload)
	b.ReportAllocs()
	for range b.N {
		_, _ = DecodeUpstreamError(payload)
	}
}
