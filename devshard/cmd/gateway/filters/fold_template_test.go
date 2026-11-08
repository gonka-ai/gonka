package filters

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func foldEvents(t *testing.T, events [][]byte, intent LogprobIntent, withTemplate bool) []byte {
	t.Helper()
	folder := NewBodyFolder(intent)
	for _, event := range events {
		if !withTemplate {
			folder.template = nil
		}
		if _, err := folder.Write(event); err != nil {
			t.Fatalf("Write(): %v", err)
		}
	}
	return folder.Body()
}

func topLevelKeyCount(payloads ...[]byte) int {
	keys := map[string]bool{}
	for _, payload := range payloads {
		decoded, _ := decodeStreamedEvent(bytes.TrimSpace(payload))
		for key := range decoded {
			keys[key] = true
		}
	}
	return len(keys)
}

func plainContentStream(chunks int) [][]byte {
	events := make([][]byte, 0, chunks+2)
	for index := range chunks {
		events = append(events, fmt.Appendf(nil, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,`+
			`"model":"model-a","choices":[{"index":0,"delta":{"content":" token%d"},"logprobs":null,"finish_reason":null}]}`+"\n\n", index))
	}
	events = append(events, []byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,`+
		`"model":"model-a","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":17,"completion_tokens":64,"total_tokens":81}}`+"\n\n"))
	return append(events, SSEDoneEvent)
}

// Test flow:
//  1. Fold a stream of plain content chunks, whose events differ only in their content text, with the template on and with it cleared before every event.
//  2. Assert both folds arrive at the same body, and that it carries every token in order.
func TestContentTemplateFoldsWhatTheFullMergeWould(t *testing.T) {
	events := plainContentStream(300)

	templated := foldEvents(t, events, LogprobIntent{}, true)
	merged := foldEvents(t, events, LogprobIntent{}, false)

	if !bytes.Equal(templated, merged) {
		t.Fatalf("templated fold = %s, want %s", templated, merged)
	}
	if !bytes.Contains(templated, []byte(`" token0 token1 token2`)) || !bytes.Contains(templated, []byte(`token298 token299"`)) {
		t.Fatalf("folded body lost tokens: %s", templated)
	}
}

func logprobsStreamWithIrregularEntries(tokens int) [][]byte {
	events := standStream(tokens)
	irregular := [][]byte{
		[]byte(`data: {"choices":[{"delta":{"content":"\n"},"finish_reason":null,"index":0,"logprobs":{"content":[{"bytes":[10],"logprob":-0.1,"token":"\n","top_logprobs":[]}]}}],"created":1792336825,"id":"devshard-1-143","model":"model-a","object":"chat.completion.chunk"}` + "\n\n"),
		[]byte(`data: {"choices":[{"delta":{"content":"c"},"finish_reason":null,"index":0,"logprobs":{"content":[{"bytes":[99],"logprob":-0.1,"token":"\u0063","top_logprobs":[]}]}}],"created":1792336825,"id":"devshard-1-143","model":"model-a","object":"chat.completion.chunk"}` + "\n\n"),
		[]byte(`data: {"choices":[{"delta":{"content":"z"},"finish_reason":null,"index":0,"logprobs":{"content":[{"token":"z","logprob":-0.1,"bytes":[122],"top_logprobs":[]}]}}],"created":1792336825,"id":"devshard-1-143","model":"model-a","object":"chat.completion.chunk"}` + "\n\n"),
		[]byte(`data: {"choices":[{"delta":{"content":"y"},"finish_reason":null,"index":0,"logprobs":{"content":[ {"bytes":[121],"logprob":-0.1,"token":"y","top_logprobs":[]}]}}],"created":1792336825,"id":"devshard-1-143","model":"model-a","object":"chat.completion.chunk"}` + "\n\n"),
	}
	middle := len(events) / 2
	return slices.Concat(events[:middle], irregular, events[middle:])
}

// Test flow:
//  1. Fold a stand-shaped stream whose chunks carry logprobs, with entries escaped, out of key order and spaced spliced in, once per logprob intent, with the template on and with it cleared before every event.
//  2. Assert both folds arrive at the same body byte for byte.
func TestLogprobsTemplateFoldsWhatTheFullMergeWould(t *testing.T) {
	events := logprobsStreamWithIrregularEntries(300)
	for name, intent := range logprobIntents {
		t.Run(name, func(t *testing.T) {
			templated, merged := foldEvents(t, events, intent, true), foldEvents(t, events, intent, false)

			if !bytes.Equal(templated, merged) {
				t.Fatalf("templated fold differs from the full merge:\ntemplated %.400s\nmerged    %.400s", templated, merged)
			}
		})
	}
}

// Test flow:
//  1. Fold a stand-shaped stream for a client that asked for logprobs.
//  2. Assert a template carrying logprobs entries forms, so the equality above is the fast path's and not the full merge's alone.
func TestStandStreamFormsALogprobsTemplate(t *testing.T) {
	folder := NewBodyFolder(LogprobIntent{Keep: true})
	for _, event := range standStream(3) {
		if _, err := folder.Write(event); err != nil {
			t.Fatal(err)
		}
		if folder.template != nil && folder.template.logprobs != nil {
			return
		}
	}
	t.Fatal("no logprobs template formed over a stand-shaped stream")
}

// Test flow:
//  1. Form a logprobs template on a stand-shaped chunk, then send one whose bytes around the text match it but whose logprobs content is a number, an object, a boolean or a string, once per logprob intent.
//  2. Assert the fold neither panics nor differs from the full merge: a host's chunk decides what sits where the template expects an array.
func TestLogprobsTemplateDeclinesContentThatIsNotAnArray(t *testing.T) {
	stand := standStream(2)
	for _, content := range []string{`7`, `{"a":1}`, `true`, `"zz"`} {
		odd := []byte(`data: {"choices":[{"delta":{"content":"q"},"finish_reason":null,"index":0,"logprobs":{"content":` + content + `}}],"created":1792336825,"id":"devshard-1-143","model":"model-a","object":"chat.completion.chunk"}` + "\n\n")
		events := [][]byte{stand[0], stand[1], odd, stand[1]}
		for name, intent := range logprobIntents {
			t.Run(name+"/"+content, func(t *testing.T) {
				templated, merged := foldEvents(t, events, intent, true), foldEvents(t, events, intent, false)

				if !bytes.Equal(templated, merged) {
					t.Fatalf("templated fold differs from the full merge:\ntemplated %.300s\nmerged    %.300s", templated, merged)
				}
			})
		}
	}
}

// Test flow:
//  1. Fold streams whose logprobs entries carry a numeric index — every chunk indexed, and one opening on an empty content array — once per logprob intent, with the template on and with it cleared before every event.
//  2. Assert both folds arrive at the same body byte for byte: the full merge folds indexed entries into one another, so the template must not append them.
func TestLogprobsTemplateFoldsIndexedEntriesAsTheFullMergeDoes(t *testing.T) {
	chunk := func(text, entries, finish string) []byte {
		return []byte(`data: {"choices":[{"delta":{"content":"` + text + `"},"finish_reason":` + finish + `,"index":0,"logprobs":{"content":` + entries + `}}],"id":"c"}` + "\n\n")
	}
	indexed := func(logprob, token string) string {
		return `[{"index":0,"logprob":` + logprob + `,"token":"` + token + `"}]`
	}
	streams := map[string][][]byte{
		"every chunk indexed":   {chunk("a", indexed("-1", "a"), "null"), chunk("b", indexed("-2", "b"), "null"), chunk("c", indexed("-3", "c"), "null")},
		"opening on no entries": {chunk("a", "[]", "null"), chunk("b", indexed("-2", "b"), "null"), chunk("c", indexed("-3", "c"), `"stop"`)},
	}
	for streamName, events := range streams {
		for intentName, intent := range logprobIntents {
			t.Run(streamName+"/"+intentName, func(t *testing.T) {
				templated, merged := foldEvents(t, events, intent, true), foldEvents(t, events, intent, false)

				if !bytes.Equal(templated, merged) {
					t.Fatalf("templated fold differs from the full merge:\ntemplated %s\nmerged    %s", templated, merged)
				}
			})
		}
	}
}

// Test flow:
//  1. Fold a chunk naming as many choices as the fold holds, then two plain chunks for choice 0, with the template on and with it cleared before every event.
//  2. Assert both folds arrive at the same body: past the choice limit the full merge drops a chunk, so the template must not grow its text either.
func TestContentTemplateRespectsTheChoiceLimit(t *testing.T) {
	choices := make([]string, maxIndexedElements)
	for index := range choices {
		choices[index] = fmt.Sprintf(`{"index":%d,"delta":{"content":"c%d"}}`, index, index)
	}
	events := [][]byte{
		[]byte(`data: {"object":"chat.completion.chunk","choices":[` + strings.Join(choices, ",") + "]}\n\n"),
		[]byte(`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"a"}}]}` + "\n\n"),
		[]byte(`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"b"}}]}` + "\n\n"),
	}

	templated, merged := foldEvents(t, events, LogprobIntent{}, true), foldEvents(t, events, LogprobIntent{}, false)

	if !bytes.Equal(templated, merged) {
		t.Fatalf("templated fold differs from the full merge past the choice limit:\ntemplated %.200s\nmerged    %.200s", templated, merged)
	}
}

// Test flow:
//  1. Generate three payloads, skipping those past maxTopLevelFields keys between them, where the full merge keeps an arbitrary subset, and fold them as one stream: first, the second twice, third, then first and third again, once with the template and once with it cleared before every event.
//  2. Assert both folds arrive at the same body: the template only ever skips a decode whose merge would have appended the same text.
func FuzzContentTemplateAgreesWithTheFullMerge(f *testing.F) {
	plain := func(text string) []byte {
		return fmt.Appendf(nil, `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"%s"},"logprobs":null,"finish_reason":null}]}`, text)
	}
	f.Add(plain("a"), plain(`b\u0041`), []byte(`{"choices":[{"index":0,"delta":{"content":"a"},"logprobs":{"content":[]}}]}`), false, false)
	f.Add([]byte(`{"choices":[{"index":0,"delta":{"content":"a","role":null}}]}`), []byte(`{"choices":[{"index":0,"delta":{"content":"b","role":null}}]}`), []byte(`{"choices":[{"index":0,"delta":{"content":"b\u0000","role":null}}]}`), true, false)
	f.Add([]byte(`{"choices":[{"index":0,"delta":{"content":"a","tool_calls":[]}}]}`), []byte(`{"choices":[{"index":0,"delta":{"content":"a"}}],"value":NaN}`), []byte(`{"choices":[{"index":0,"delta":{"content":"a\"},"x":{"y":"z"}}]}`), true, true)
	f.Add(plain("a"), []byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"content":"x"}}]}`), plain("\xff"), false, false)
	f.Add([]byte(`{"choices":[{"index":0,"delta":{"content":"a","reasoning":"r"}}]}`), []byte(`{"choices":[{"index":0,"delta":{"content":"b","reasoning":"r"}}]}`), plain("c"), false, false)
	f.Add([]byte(`{"choices":[{"index":0,"delta":{"content":"NaN"}}],"v":NaN}`), []byte(`{"choices":[{"index":0,"delta":{"content":"Infinity"}}],"v":NaN}`), []byte(`{"choices":[{"index":0,"delta":{"content":"-Infinity"}}],"v":NaN}`), false, false)
	f.Add([]byte(`{"choices":[{"index":0,"delta":{"content":"a","reasoning_content":null}}]}`), []byte(`{"choices":[{"index":0,"delta":{"content":"b","reasoning_content":null}}]}`), []byte(`{"choices":[{"index":0,"delta":{"content":"c","reasoning_content":"r"}}]}`), false, false)
	logprobsChunk := func(text, token string) []byte {
		return fmt.Appendf(nil, `{"choices":[{"delta":{"content":"%s"},"finish_reason":null,"index":0,"logprobs":{"content":[{"bytes":[1],"logprob":-0.1,"token":"%s","top_logprobs":[{"logprob":-1,"token":"x"}]}]}}],"id":"c"}`, text, token)
	}
	f.Add(logprobsChunk("a", "a"), logprobsChunk("b", "b"), logprobsChunk("c", `\u0063`), true, false)
	f.Add(logprobsChunk("a", "a"), logprobsChunk("b", "b"), logprobsChunk("c", "c"), true, true)
	f.Add(logprobsChunk("a", "a"), []byte(`{"choices":[{"delta":{"content":"b"},"finish_reason":null,"index":0,"logprobs":{"content":[{"token":"b","bytes":[1]}]}}],"id":"c"}`), logprobsChunk("c", "c"), true, false)
	f.Add(logprobsChunk("a", "a"), []byte(`{"choices":[{"delta":{"content":"b"},"finish_reason":null,"index":0,"logprobs":{"content":7}}],"id":"c"}`), logprobsChunk("c", "c"), true, false)
	f.Add([]byte(`{"choices":[{"delta":{"content":"a"},"finish_reason":null,"index":0,"logprobs":{"content":[{"index":0,"token":"a"}]}}],"id":"c"}`), []byte(`{"choices":[{"delta":{"content":"b"},"finish_reason":null,"index":0,"logprobs":{"content":[{"index":0,"token":"b"}]}}],"id":"c"}`), logprobsChunk("c", "c"), true, true)
	f.Fuzz(func(t *testing.T, first, second, third []byte, keep, keepTop bool) {
		if bytes.ContainsAny(first, "\r\n") || bytes.ContainsAny(second, "\r\n") || bytes.ContainsAny(third, "\r\n") || topLevelKeyCount(first, second, third) > maxTopLevelFields {
			return
		}
		events := make([][]byte, 0, 6)
		for _, payload := range [][]byte{first, second, second, third, first, third} {
			events = append(events, append(append([]byte("data: "), payload...), "\n\n"...))
		}
		intent := LogprobIntent{Keep: keep, KeepTop: keepTop}

		templated, merged := foldEvents(t, events, intent, true), foldEvents(t, events, intent, false)

		if !bytes.Equal(templated, merged) {
			t.Fatalf("first %q second %q third %q intent %+v:\ntemplated %s\nmerged    %s", first, second, third, intent, templated, merged)
		}
	})
}
