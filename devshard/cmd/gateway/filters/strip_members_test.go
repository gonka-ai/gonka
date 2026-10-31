package filters

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func decodedAfterStripping(t testing.TB, payload []byte, intent LogprobIntent) (stripped any, changed bool) {
	t.Helper()
	decoded, ok := decodeJSONValue(payload)
	if !ok {
		t.Fatalf("decodeJSONValue(%s) failed", payload)
	}
	changed = stripDecodedFields(decoded, intent)
	return decoded, changed
}

func requireStripMatchesDecoding(t testing.TB, payload []byte, intent LogprobIntent) {
	t.Helper()
	stripped, reportedChange, handled := stripMembersWithoutDecoding(payload, intent)
	if !handled {
		return
	}
	got, ok := decodeJSONValue(stripped)
	if !ok {
		t.Fatalf("stripMembersWithoutDecoding(%s, %+v) = %s, which does not decode", payload, intent, stripped)
	}
	want, changed := decodedAfterStripping(t, payload, intent)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stripMembersWithoutDecoding(%s, %+v) decodes to %v, want %v", payload, intent, got, want)
	}
	if reportedChange != changed {
		t.Fatalf("stripMembersWithoutDecoding(%s, %+v) changed = %v, want %v", payload, intent, reportedChange, changed)
	}
}

func streamPayloads(t testing.TB) [][]byte {
	t.Helper()
	payloads := [][]byte{}
	entries, err := os.ReadDir(filepath.Join("testdata", "sse"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		stream, err := os.ReadFile(filepath.Join("testdata", "sse", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for event := range bytes.SplitSeq(stream, sseEventSeparator) {
			if _, payload, held := eventPayload(event); held && stdjson.Valid(payload) {
				payloads = append(payloads, payload)
			}
		}
	}
	for _, event := range standStream(3) {
		if _, payload, held := eventPayload(bytes.TrimRight(event, "\n")); held && stdjson.Valid(payload) {
			payloads = append(payloads, payload)
		}
	}
	return payloads
}

var strippingEdgeCases = []string{
	`{"logprobs":null}`,
	`{"a":1,"logprobs":{"x":[1,2,{"logprobs":3}]},"b":2}`,
	`{"logprobs":1,"a":1}`,
	`{"a":1,"logprobs":1}`,
	` { "a" : [ 1 , { "token_ids" : [ 1 ] , "b" : "}]\"" } ] , "c" : true } `,
	`{"a":"\u00e9\n\"logprobs\"","logprobs":"{[\""}`,
	`{"nested":{"deep":{"top_logprobs":[[[]]],"keep":[{},[]]}}}`,
	`[{"logprobs":1},{"keep":-1.5e300}]`,
	`{"big":123456789012345678901234567890,"logprob":-0.1}`,
	`{"top_logprobs":[ ],"inner":{"top_logprobs":null,"token_ids":[1]}}`,
	`{"a":{"top_logprobs":[ ]}}`,
	`{"top_logprobs":[],"logprobs":{"content":[{"top_logprobs":[{"token":"a"}]}]}}`,
	`{}`,
	`[]`,
	`"text"`,
	`7`,
}

// Test flow:
//  1. Strip, once per logprob intent, every payload in the SSE testdata, a stand-shaped chunk, and payloads built around whitespace, nesting, empty and null top_logprobs, and strings that look like JSON.
//  2. Assert each is handled, decodes to what decoding and stripDecodedFields produce, and reports a change exactly when that changed the value.
func TestStrippingWithoutDecodingMatchesDecoding(t *testing.T) {
	payloads := streamPayloads(t)
	for _, edgeCase := range strippingEdgeCases {
		payloads = append(payloads, []byte(edgeCase))
	}
	for name, intent := range logprobIntents {
		for index, payload := range payloads {
			t.Run(fmt.Sprintf("%s/%d", name, index), func(t *testing.T) {
				if _, _, handled := stripMembersWithoutDecoding(payload, intent); !handled {
					t.Fatalf("stripMembersWithoutDecoding(%s) declined, want it handled", payload)
				}
				requireStripMatchesDecoding(t, payload, intent)
			})
		}
	}
}

// Test flow:
//  1. Strip payloads the decoding path reads differently from their bytes: a NaN bareword, a repeated key, an escaped key, an invalid object, a second value after the first, an empty payload, and a key repeated past the keys scanned one by one.
//  2. Assert each is declined, so the caller falls back to decoding.
func TestStrippingWithoutDecodingDeclinesWhatDecodingReadsDifferently(t *testing.T) {
	for _, payload := range []string{
		`{"logprob":NaN}`,
		`{"a":1,"a":2}`,
		`{"log\u0070robs":1}`,
		`{"a":}`,
		`{"a":1} {"b":2}`,
		``,
		manyKeysWithRepeat(linearKeyScanLimit + 4),
	} {
		t.Run(payload, func(t *testing.T) {
			if _, _, handled := stripMembersWithoutDecoding([]byte(payload), LogprobIntent{}); handled {
				t.Errorf("stripMembersWithoutDecoding(%s) handled, want it declined", payload)
			}
		})
	}
}

// Test flow:
//  1. Seed the corpus with every stream payload and edge case above, and strip each generated payload once per logprob intent.
//  2. Assert a payload decoding refuses is declined, and any other decodes to what decoding and stripDecodedFields produce.
func manyKeysWithRepeat(count int) string {
	var builder strings.Builder
	builder.WriteString("{")
	for index := range count {
		fmt.Fprintf(&builder, `"key%d":%d,`, index, index)
	}
	builder.WriteString(`"key0":0}`)
	return builder.String()
}

func requireSameRewrite(t *testing.T, event []byte, intent LogprobIntent, keepUsage bool) {
	t.Helper()
	dataLines, payload, held := eventPayload(event)
	if !held {
		return
	}
	got, gotMalformed := rewriteEvent(event, intent, keepUsage)
	want, wantMalformed := rewriteDecodedEvent(event, dataLines, payload, intent, keepUsage)
	if gotMalformed != wantMalformed || (got == nil) != (want == nil) {
		t.Fatalf("rewriteEvent(%q, %+v) = %q (malformed %v), the full rewrite %q (malformed %v)", event, intent, got, gotMalformed, want, wantMalformed)
	}
	if got == nil || bytes.Equal(got, want) {
		return
	}
	_, gotPayload, _ := eventPayload(got)
	_, wantPayload, _ := eventPayload(want)
	gotValue, gotDecoded := decodeJSONValue(gotPayload)
	wantValue, wantDecoded := decodeJSONValue(wantPayload)
	if !gotDecoded || !wantDecoded || !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("rewriteEvent(%q, %+v) = %q, which the full rewrite forwards as %q", event, intent, got, want)
	}
}

// Test flow:
//  1. Rewrite every stream payload and edge case above as a one-line event, once per logprob intent and usage choice, through rewriteEvent and through the full rewrite it may skip.
//  2. Assert both drop or keep the event alike and forward the same decoded value.
func TestRewritingWithoutDecodingForwardsWhatTheFullRewriteDoes(t *testing.T) {
	payloads := streamPayloads(t)
	for _, edgeCase := range strippingEdgeCases {
		payloads = append(payloads, []byte(edgeCase))
	}
	for name, intent := range logprobIntents {
		for index, payload := range payloads {
			for _, keepUsage := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/usage=%v", name, index, keepUsage), func(t *testing.T) {
					requireSameRewrite(t, append([]byte("data: "), payload...), intent, keepUsage)
				})
			}
		}
	}
}

// Test flow:
//  1. Generate one-line events and rewrite each through rewriteEvent and through the full rewrite, for the generated intent and usage choice.
//  2. Assert both drop or keep the event alike and forward the same decoded value.
func FuzzRewritingWithoutDecodingForwardsWhatTheFullRewriteDoes(f *testing.F) {
	for _, payload := range streamPayloads(f) {
		f.Add(payload, false, false, false)
	}
	for _, edgeCase := range strippingEdgeCases {
		f.Add([]byte(edgeCase), true, true, false)
	}
	f.Add([]byte(`{"choices":[{"message":{"content":"hi"},"logprobs":{}}]}`), true, false, false)
	f.Add([]byte(`{"choices":[],"usage":{"total_tokens":3},"logprobs":1}`), false, true, true)
	f.Fuzz(func(t *testing.T, payload []byte, keepUsage, keep, keepTop bool) {
		if bytes.ContainsAny(payload, "\r\n") {
			return
		}
		requireSameRewrite(t, append([]byte("data: "), payload...), LogprobIntent{Keep: keep, KeepTop: keepTop}, keepUsage)
	})
}

func FuzzStrippingWithoutDecodingMatchesDecoding(f *testing.F) {
	for _, payload := range streamPayloads(f) {
		f.Add(payload)
	}
	for _, edgeCase := range strippingEdgeCases {
		f.Add([]byte(edgeCase))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		for _, intent := range logprobIntents {
			if _, ok := decodeJSONValue(payload); !ok {
				if _, _, handled := stripMembersWithoutDecoding(payload, intent); handled {
					t.Fatalf("stripMembersWithoutDecoding(%q) handled a payload decoding refuses", payload)
				}
				continue
			}
			requireStripMatchesDecoding(t, payload, intent)
		}
	})
}
