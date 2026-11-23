package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/jsonshape"
)

const largeFoldEventBytes = 1 << 20

const largeFoldBodyBytes = 15 << 20

type foldShapeCase struct {
	name   string
	intent clientResponseIntent
	body   func() []byte
}

func foldShapeKey(i int) string {
	return "k" + strconv.FormatInt(int64(i), 36)
}

func sseBody(events func(emit func(string))) []byte {
	var b bytes.Buffer
	events(func(p string) {
		b.WriteString("data: ")
		b.WriteString(p)
		b.WriteString("\n\n")
	})
	b.WriteString("data: [DONE]\n\n")
	return b.Bytes()
}

func fanoutObject(next *int, limit int, member func(i int) string) string {
	var b strings.Builder
	b.WriteByte('{')
	for first := true; b.Len() < limit; first = false {
		if !first {
			b.WriteByte(',')
		}
		b.WriteString(member(*next))
		*next++
	}
	b.WriteByte('}')
	return b.String()
}

func repeatedArray(elem string, limit int) string {
	n := limit / (len(elem) + 1)
	return "[" + strings.TrimSuffix(strings.Repeat(elem+",", n), ",") + "]"
}

func eventsUntil(total int, build func(i int) string) []byte {
	return sseBody(func(emit func(string)) {
		emit(`{"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`)
		size := 0
		for i := 0; size < total; i++ {
			p := build(i)
			size += len(p)
			emit(p)
		}
	})
}

func deltaEvent(delta string) string {
	return `{"choices":[{"index":0,"delta":` + delta + `}]}`
}

func largeFoldShapes() []foldShapeCase {
	keyed := func(value string, eventBytes int) func() []byte {
		return func() []byte {
			next := 0
			return eventsUntil(largeFoldBodyBytes, func(int) string {
				return deltaEvent(fanoutObject(&next, eventBytes, func(i int) string {
					return strconv.Quote(foldShapeKey(i)) + ":" + value
				}))
			})
		}
	}
	return []foldShapeCase{
		{name: "message_keys_empty_values", body: keyed(`""`, largeFoldEventBytes)},
		{name: "message_keys_single_event", body: keyed(`""`, largeFoldBodyBytes)},
		{name: "message_keys_numeric_values", body: keyed(`0`, largeFoldEventBytes)},
		{name: "message_keys_empty_objects", body: keyed(`{}`, largeFoldEventBytes)},
		{name: "message_long_keys", body: func() []byte {
			return eventsUntil(largeFoldBodyBytes, func(i int) string {
				return deltaEvent(`{"` + foldShapeKey(i) + strings.Repeat("x", largeFoldEventBytes) + `":0}`)
			})
		}},
		{name: "message_empty_container_arrays", body: func() []byte {
			return eventsUntil(largeFoldBodyBytes, func(i int) string {
				return deltaEvent(`{"` + foldShapeKey(i) + `":` + repeatedArray(`{}`, largeFoldEventBytes) + `}`)
			})
		}},
		{name: "message_nested_empty_arrays", body: func() []byte {
			return eventsUntil(largeFoldBodyBytes, func(i int) string {
				return deltaEvent(`{"` + foldShapeKey(i) + `":` + repeatedArray(`[]`, largeFoldEventBytes) + `}`)
			})
		}},
		{name: "tool_call_field_keys", body: func() []byte {
			next := 0
			return eventsUntil(largeFoldBodyBytes, func(int) string {
				fields := fanoutObject(&next, largeFoldEventBytes, func(i int) string {
					return strconv.Quote(foldShapeKey(i)) + ":0"
				})
				return deltaEvent(`{"tool_calls":[{"index":0,` + fields[1:] + `]}`)
			})
		}},
		{name: "tool_call_function_keys", body: func() []byte {
			next := 0
			return eventsUntil(largeFoldBodyBytes, func(int) string {
				fn := fanoutObject(&next, largeFoldEventBytes, func(i int) string {
					return strconv.Quote(foldShapeKey(i)) + ":0"
				})
				return deltaEvent(`{"tool_calls":[{"index":0,"function":` + fn + `}]}`)
			})
		}},
		{name: "top_level_keys", body: func() []byte {
			next := 0
			return eventsUntil(largeFoldBodyBytes, func(int) string {
				top := fanoutObject(&next, largeFoldEventBytes, func(i int) string {
					return strconv.Quote(foldShapeKey(i)) + ":0"
				})
				return `{"choices":[{"index":0,"delta":{}}],` + top[1:]
			})
		}},
		{name: "choice_level_keys", body: func() []byte {
			next := 0
			return eventsUntil(largeFoldBodyBytes, func(int) string {
				ch := fanoutObject(&next, largeFoldEventBytes, func(i int) string {
					return strconv.Quote(foldShapeKey(i)) + ":0"
				})
				return `{"choices":[{"index":0,` + ch[1:] + `]}`
			})
		}},
		{name: "choices_array_elements", body: func() []byte {
			return eventsUntil(largeFoldBodyBytes, func(int) string {
				return `{"choices":` + repeatedArray(`{}`, largeFoldEventBytes) + `}`
			})
		}},
		{name: "reserved_top_level_values", body: func() []byte {
			reserved := []string{"id", "model", "system_fingerprint", "service_tier", "created"}
			return eventsUntil(largeFoldBodyBytes, func(i int) string {
				return `{"` + reserved[i%len(reserved)] + `":"` + strings.Repeat("x", 3*largeFoldEventBytes) + `","choices":[{"index":0,"delta":{}}]}`
			})
		}},
		{name: "logprobs_empty_entries", intent: clientResponseIntent{keepLogprobs: true, keepTopLogprobs: true}, body: func() []byte {
			return eventsUntil(largeFoldBodyBytes, func(int) string {
				return `{"choices":[{"index":0,"delta":{},"logprobs":{"content":` + repeatedArray(`{}`, largeFoldEventBytes) + `}}]}`
			})
		}},
		{name: "passthrough_logprobs_keys", body: func() []byte {
			next := 0
			lp := fanoutObject(&next, largeFoldBodyBytes, func(i int) string {
				return strconv.Quote(foldShapeKey(i)) + ":0"
			})
			return sseBody(func(emit func(string)) {
				emit(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"token_ids":[1],"logprobs":` + lp + `}]}`)
			})
		}},
		{name: "logprobs_object_keys", intent: clientResponseIntent{keepLogprobs: true, keepTopLogprobs: true}, body: func() []byte {
			next := 0
			return eventsUntil(largeFoldBodyBytes, func(int) string {
				lp := fanoutObject(&next, largeFoldEventBytes, func(i int) string {
					return strconv.Quote(foldShapeKey(i)) + ":0"
				})
				return `{"choices":[{"index":0,"delta":{},"logprobs":` + lp + `}]}`
			})
		}},
	}
}

func measureFold(body []byte, intent clientResponseIntent) ([]byte, uint64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	out := aggregateSSEStreamReader(bytes.NewReader(body), intent)
	runtime.ReadMemStats(&after)
	return out, after.TotalAlloc - before.TotalAlloc
}

func requireBoundedFoldResult(t *testing.T, out []byte) {
	t.Helper()
	if isAggregateFoldTooLargePayload(out) {
		return
	}
	var resp map[string]any
	require.NoError(t, json.Unmarshal(out, &resp), "%.200s", out)
	require.LessOrEqual(t, int64(len(out)), 4*defaultAggregateMaxMemoryBytes, "fold output must stay within a small multiple of the RAM budget")
}

func useTempAggregateSpool(t *testing.T) {
	prev := currentAggregateSpoolDir()
	t.Cleanup(func() { setAggregateSpoolDir(prev) })
	setAggregateSpoolDir(t.TempDir())
}

func TestAggregateFold_LargeShapesStayWithinMemoryBound(t *testing.T) {
	if testing.Short() {
		t.Skip("builds ~15 MiB bodies per shape")
	}
	useTempAggregateSpool(t)
	for _, shape := range largeFoldShapes() {
		t.Run(shape.name, func(t *testing.T) {
			body := shape.body()
			require.Greater(t, len(body), largeFoldBodyBytes)
			out, allocated := measureFold(body, shape.intent)
			requireBoundedFoldResult(t, out)
			limit := uint64(8*len(body)) + 32<<20
			t.Logf("allocated %d MiB folding a %d MiB body, rejected=%v", allocated>>20, len(body)>>20, isAggregateFoldTooLargePayload(out))
			require.LessOrEqualf(t, allocated, limit,
				"%s allocated %d MiB folding a %d MiB body", shape.name, allocated>>20, len(body)>>20)
		})
	}
}

func TestAggregateFold_HonestLargeLogprobsEventStillFolds(t *testing.T) {
	useTempAggregateSpool(t)
	entry := `{"token":"tok","logprob":-0.125,"bytes":[116,111,107],"top_logprobs":[{"token":"tok","logprob":-0.125,"bytes":[116,111,107]},{"token":"alt","logprob":-2.5,"bytes":[97,108,116]}]}`
	n := (12 << 20) / (len(entry) + 1)
	content := strings.Repeat("tok", n)
	payload := `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `"},"logprobs":{"content":` + repeatedArray(entry, (len(entry)+1)*n) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":` + strconv.Itoa(n) + `}}`
	out := aggregateSSEStream(sseData(payload, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`), clientResponseIntent{keepLogprobs: true, keepTopLogprobs: true})
	require.False(t, isAggregateFoldTooLargePayload(out))
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Logprobs struct {
				Content []json.RawMessage `json:"content"`
			} `json:"logprobs"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(out, &resp))
	require.Len(t, resp.Choices, 1)
	require.Equal(t, content, resp.Choices[0].Message.Content)
	require.Len(t, resp.Choices[0].Logprobs.Content, n)
}

func TestAggregateFold_MessageKeyCapKeepsReservedKeys(t *testing.T) {
	before := aggregateDroppedMessageFanoutTotal.Load()
	keys := make([]string, 0, aggregateMaxMessageKeys+36)
	for i := 0; i < cap(keys); i++ {
		keys = append(keys, strconv.Quote("x_"+foldShapeKey(i))+":1")
	}
	flood := "{" + strings.Join(keys, ",") + "}"
	got := aggregateSSEStream(sseData(
		deltaEvent(flood),
		deltaEvent(`{"role":"assistant","name":"bot","content":"ok"}`),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	), clientResponseIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	require.Equal(t, "assistant", msg["role"])
	require.Equal(t, "bot", msg["name"])
	require.Equal(t, "ok", msg["content"])
	extra := 0
	for k := range msg {
		if strings.HasPrefix(k, "x_") {
			extra++
		}
	}
	require.Equal(t, aggregateMaxMessageKeys, extra)
	require.Greater(t, aggregateDroppedMessageFanoutTotal.Load(), before)
}

func TestAggregateFold_ToolCallFieldCap(t *testing.T) {
	before := aggregateDroppedToolCallFieldFanoutTotal.Load()
	var fields, fn []string
	for i := 0; i < aggregateMaxToolCallFieldKeys+10; i++ {
		fields = append(fields, fmt.Sprintf(`"f%d":%d`, i, i))
		fn = append(fn, fmt.Sprintf(`"g%d":%d`, i, i))
	}
	got := aggregateSSEStream(sseData(
		deltaEvent(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]}`),
		deltaEvent(`{"tool_calls":[{"index":0,`+strings.Join(fields, ",")+`,"function":{`+strings.Join(fn, ",")+`}}]}`),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	), clientResponseIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	tc := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	require.Equal(t, "call_1", tc["id"])
	require.Equal(t, "function", tc["type"])
	require.LessOrEqual(t, len(tc), aggregateMaxToolCallFieldKeys+1)
	fnOut := tc["function"].(map[string]any)
	require.Equal(t, "f", fnOut["name"])
	require.Equal(t, "{}", fnOut["arguments"])
	require.LessOrEqual(t, len(fnOut), aggregateMaxToolCallFieldKeys+1)
	require.Greater(t, aggregateDroppedToolCallFieldFanoutTotal.Load(), before)
}

func TestRewriteStreamingPayload_StopsAtEventWithRejectedShape(t *testing.T) {
	next := 0
	wide := fanoutObject(&next, 8192, func(i int) string {
		return strconv.Quote(foldShapeKey(i)) + `:""`
	})
	honest := `{"choices":[{"index":0,"delta":{"content":"hi"},"logprobs":{"content":[{"token":"hi","logprob":-0.1,"token_ids":[1]}]}}],"usage":{"completion_tokens":1}}`
	later := `{"choices":[{"index":0,"delta":{"content":"later"}}]}`
	p := []byte("data: " + honest + "\n\n" + "data: " + deltaEvent(wide)[:len(deltaEvent(wide))-3] + `}],"usage":{"completion_tokens":1}}` + "\n\n" + "data: " + later + "\n\n")
	out, err := rewriteStreamingPayloadChecked(p, clientResponseIntent{keepLogprobs: true})
	require.ErrorIs(t, err, ErrAggregateFoldTooLarge)
	require.Contains(t, string(out), `"content":"hi"`)
	require.NotContains(t, string(out), foldShapeKey(0))
	require.NotContains(t, string(out), "later")
	require.NotContains(t, string(out), "token_ids")
	require.NotContains(t, string(out), "usage")
}

var genericDecodeAllowlist = map[string]string{
	"decodeChoiceRawSlice": "choices bounded by foldEventShapeLimits in ingest",
	"decodeChoiceForFold":  "choice object bounded by foldEventShapeLimits in ingest",
}

var boundingCalls = map[string]struct{}{
	"Check":       {},
	"checkShape":  {},
	"decodeValue": {},
}

func honestChunkedStream(chunks int, logprobs bool) []byte {
	return sseBody(func(emit func(string)) {
		emit(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
		for i := 0; i < chunks; i++ {
			lp := ""
			if logprobs {
				lp = `,"logprobs":{"content":[{"token":" tok","logprob":-0.25,"bytes":[32,116,111,107],"top_logprobs":[{"token":" tok","logprob":-0.25,"bytes":[32,116,111,107]},{"token":" alt","logprob":-1.5,"bytes":[32,97,108,116]}]}]}`
			}
			emit(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":" tok"}` + lp + `,"finish_reason":null}]}`)
		}
		emit(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2000,"total_tokens":2010}}`)
	})
}

func BenchmarkAggregateFold_HonestChunkedStream(b *testing.B) {
	for _, tc := range []struct {
		name   string
		intent clientResponseIntent
	}{
		{name: "content", intent: clientResponseIntent{}},
		{name: "logprobs", intent: clientResponseIntent{keepLogprobs: true, keepTopLogprobs: true}},
	} {
		body := honestChunkedStream(2000, tc.intent.keepLogprobs)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				out := aggregateSSEStreamReader(bytes.NewReader(body), tc.intent)
				if isAggregateFoldTooLargePayload(out) {
					b.Fatal("honest stream rejected")
				}
			}
		})
	}
}

func genericTarget(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.InterfaceType:
		return true
	case *ast.Ident:
		return t.Name == "any"
	case *ast.MapType:
		return true
	case *ast.ArrayType:
		return genericTarget(t.Elt) || isRawMessage(t.Elt)
	case *ast.StarExpr:
		return genericTarget(t.X)
	}
	return false
}

func isRawMessage(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "RawMessage"
}

func TestFoldSources_GenericDecodesAreBounded(t *testing.T) {
	files := []string{"stream_aggregate.go", "stream_aggregate_logprobs.go", "stream_rewrite.go", "sse_chunk.go"}
	fset := token.NewFileSet()
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			types := map[string]ast.Expr{}
			if fn.Type.Results != nil {
				for _, field := range fn.Type.Results.List {
					for _, n := range field.Names {
						types[n.Name] = field.Type
					}
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if spec, ok := n.(*ast.ValueSpec); ok && spec.Type != nil {
					for _, id := range spec.Names {
						types[id.Name] = spec.Type
					}
				}
				return true
			})
			firstBound := token.NoPos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var callee string
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					callee = f.Sel.Name
				case *ast.Ident:
					callee = f.Name
				}
				if _, ok := boundingCalls[callee]; ok && (firstBound == token.NoPos || call.Pos() < firstBound) {
					firstBound = call.Pos()
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Unmarshal" {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "json" {
					return true
				}
				target, ok := call.Args[1].(*ast.UnaryExpr)
				if !ok {
					return true
				}
				id, ok := target.X.(*ast.Ident)
				if !ok {
					return true
				}
				typ, known := types[id.Name]
				if !known || !genericTarget(typ) {
					return true
				}
				if _, allowed := genericDecodeAllowlist[fn.Name.Name]; allowed {
					return true
				}
				require.Truef(t, firstBound != token.NoPos && firstBound < call.Pos(),
					"%s: generic json.Unmarshal into %s in %s has no shape bound before it",
					fset.Position(call.Pos()), id.Name, fn.Name.Name)
				return true
			})
		}
	}
}

func TestAggregateFold_StripsLargeInternalArraysBeforeDecode(t *testing.T) {
	arr := repeatedArray("{}", 1<<20)
	for _, p := range []string{
		deltaEvent(`{"content":"ok","token_ids":` + arr + "}"),
		`{"object":"chat.completion","choices":[{"message":{"content":"ok"}}],"prompt_logprobs":` + arr + "}",
	} {
		body := sseData(p)
		out, allocated := measureFold(body, clientResponseIntent{})
		require.Contains(t, string(out), `"content":"ok"`)
		require.NotContains(t, string(out), "token_ids")
		require.NotContains(t, string(out), "prompt_logprobs")
		require.LessOrEqual(t, allocated, uint64(8*len(body)+(32<<20)))
	}
}

func TestRewriteStreamingPayloadChecked_RejectsExpandingCompletion(t *testing.T) {
	const n = 1024
	tokens := strings.TrimSuffix(strings.Repeat(`{"token":"x"},`, n), ",")
	body := sseData(`{"id":"` + strings.Repeat("x", 64<<10) + `","choices":[{"message":{"content":"ok"},"logprobs":{"content":[` + tokens + `]}}]}`)
	out, err := rewriteStreamingPayloadChecked(body, clientResponseIntent{})
	require.ErrorIs(t, err, ErrAggregateFoldTooLarge)
	require.NotContains(t, string(out), `"content":"x"`)
}

func TestClientFieldFiltersFailClosedOnDeepNesting(t *testing.T) {
	deep := strings.Repeat(`{"x":`, jsonshape.MaxDepth+1) + `{"token_ids":[1]}` + strings.Repeat(`}`, jsonshape.MaxDepth+1)

	_, err := filterClientInternalFields([]byte(deep), clientResponseIntent{})
	require.ErrorIs(t, err, ErrAggregateFoldTooLarge)

	_, err = rewriteLogprobEntry([]byte(deep), false, internalStrippedFields)
	require.ErrorIs(t, err, ErrAggregateFoldTooLarge)

	event := `{"choices":[{"index":0,"delta":{"content":"x"}}],"usage":` + deep + `}`
	out, err := rewriteStreamingPayloadChecked([]byte("data: "+event+"\n\n"), clientResponseIntent{})
	require.ErrorIs(t, err, ErrAggregateFoldTooLarge)
	require.NotContains(t, string(out), "token_ids")

	passthrough := `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"x"},"extension":` + deep + `}]}`
	require.True(t, isAggregateFoldTooLargePayload(aggregateSSEStream(sseData(passthrough), clientResponseIntent{})))

	invalid := []byte(`{"token_ids":[1],`)
	got, err := filterClientInternalFields(invalid, clientResponseIntent{})
	require.NoError(t, err)
	require.Equal(t, string(invalid), string(got))
}

func TestRewriteStreamingPayloadChecked_ValidatesWhitespaceAfterDataPrefix(t *testing.T) {
	next := 0
	wide := fanoutObject(&next, 8192, func(i int) string {
		return strconv.Quote(foldShapeKey(i)) + ":0"
	})
	body := []byte("data:  \t" + deltaEvent(wide) + "\n\n")
	out, err := rewriteStreamingPayloadChecked(body, clientResponseIntent{})
	require.ErrorIs(t, err, ErrAggregateFoldTooLarge)
	require.Empty(t, out)
}

func TestAggregateFold_RetainedErrorDoesNotAliasEventBuffer(t *testing.T) {
	body := sseData(
		deltaEvent(`{"content":"some text"}`),
		`{"error":{"message":"provider error"},"logprobs":null}`,
		deltaEvent(`{"content":"later text with no finish reason"}`),
	)
	out := aggregateSSEStreamReader(bytes.NewReader(body), clientResponseIntent{keepLogprobs: true, keepTopLogprobs: true})
	require.JSONEq(t, `{"error":{"message":"provider error"},"logprobs":null}`, string(out))
}

func wideRootEvent(content string, extraKeys int) string {
	var b strings.Builder
	for i := 0; i < extraKeys; i++ {
		fmt.Fprintf(&b, `,"p%d":0`, i)
	}
	return `{"choices":[{"index":0,"delta":{"content":"` + content + `"}}]` + b.String() + `}`
}

func TestScanSSEChunk_IgnoresEventsTheGatewayWouldReject(t *testing.T) {
	ok := "data: " + wideRootEvent("hello", 10) + "\n\n"
	require.True(t, scanSSEChunk([]byte(ok)).HasContent)

	wide := "data: " + wideRootEvent("hello", aggregateMaxObjectMembers+1) + "\n\n"
	require.False(t, scanSSEChunk([]byte(wide)).HasContent)
	_, err := rewriteStreamingPayloadChecked([]byte(wide), clientResponseIntent{})
	require.ErrorIs(t, err, ErrAggregateFoldTooLarge)

	nodes := "[" + strings.TrimSuffix(strings.Repeat("0,", aggregateMaxValueNodes), ",") + "]"
	heavy := `data: {"choices":[{"index":0,"delta":{"content":"hello","x":` + nodes + `}}]}` + "\n\n"
	require.False(t, scanSSEChunk([]byte(heavy)).HasContent)
	require.True(t, isAggregateFoldTooLargePayload(aggregateSSEStream([]byte(heavy+"data: [DONE]\n\n"), clientResponseIntent{})))
}

func TestAggregateFold_MetadataDoesNotShrinkTextBudget(t *testing.T) {
	content := strings.Repeat("x", int(defaultAggregateMaxMemoryBytes)-100)
	got := aggregateSSEStream(sseData(
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"model","system_fingerprint":"fp","choices":[{"index":0,"delta":{"role":"assistant","content":"`+content+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	), clientResponseIntent{keepUsage: true})
	require.False(t, isAggregateFoldTooLargePayload(got))
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "chatcmpl-1", resp.ID)
	require.Equal(t, "model", resp.Model)
	require.Len(t, resp.Choices[0].Message.Content, len(content))

	over := strings.Repeat("x", int(defaultAggregateMaxMemoryBytes)+1)
	require.True(t, isAggregateFoldTooLargePayload(aggregateSSEStream(sseData(deltaEvent(`{"content":"`+over+`"}`)), clientResponseIntent{})))
}

func TestAggregateFold_MalformedLogprobsContentIsIgnored(t *testing.T) {
	for _, content := range []string{`"x"`, `5`, `{}`, `true`} {
		t.Run(content, func(t *testing.T) {
			got := aggregateSSEStream(sseData(
				`{"choices":[{"index":0,"delta":{"content":"a"},"logprobs":{"content":`+content+`}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"b"},"finish_reason":"stop"}]}`,
			), clientResponseIntent{keepLogprobs: true, keepTopLogprobs: true})
			var resp map[string]any
			require.NoError(t, json.Unmarshal(got, &resp), "%s", got)
			msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
			require.Equal(t, "ab", msg["content"])
		})
	}
}

func TestAggregateFold_CompletionsFormatLogprobsFold(t *testing.T) {
	const n = 5000
	tokens := make([]string, n)
	lps := make([]string, n)
	tops := make([]string, n)
	offsets := make([]string, n)
	for i := range tokens {
		tokens[i] = `"t"`
		lps[i] = "-0.5"
		tops[i] = `{"t":-0.5,"u":-1.5}`
		offsets[i] = strconv.Itoa(i)
	}
	logprobs := `{"tokens":[` + strings.Join(tokens, ",") + `],"token_logprobs":[` + strings.Join(lps, ",") + `],"top_logprobs":[` + strings.Join(tops, ",") + `],"text_offset":[` + strings.Join(offsets, ",") + `]}`
	got := aggregateSSEStream(sseData(
		`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"`+strings.Repeat("t", n)+`"},"logprobs":`+logprobs+`,"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":5000}}`,
	), clientResponseIntent{keepLogprobs: true, keepTopLogprobs: true})
	require.False(t, isAggregateFoldTooLargePayload(got))
	var resp struct {
		Choices []struct {
			Logprobs struct {
				Tokens      []string             `json:"tokens"`
				TopLogprobs []map[string]float64 `json:"top_logprobs"`
			} `json:"logprobs"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Len(t, resp.Choices[0].Logprobs.Tokens, n)
	require.Len(t, resp.Choices[0].Logprobs.TopLogprobs, n)
}

func TestJudgeLogprobs_CompletionsKeysAreCaseInsensitive(t *testing.T) {
	scan := scanSSEChunk([]byte(`data: {"choices":[{"delta":{"content":"x"},"logprobs":{"Tokens":["abc"]}}]}` + "\n\n"))
	require.True(t, scan.LogprobsFound)
	require.True(t, scan.LogprobsDecoded)
	scan = scanSSEChunk([]byte(`data: {"choices":[{"delta":{"content":"x"},"logprobs":{"tokens":["token_id:12"],"TOP_LOGPROBS":[{"abc":-1}]}}]}` + "\n\n"))
	require.True(t, scan.LogprobsFound)
	require.True(t, scan.LogprobsDecoded)
}

func TestDeferredWriter_KeepsEventsBeforeARejectedOne(t *testing.T) {
	rec := httptest.NewRecorder()
	dw := newDeferredWriter(context.Background(), rec, "escrow", nil)
	good := "data: " + deltaEvent(`{"content":"kept"}`) + "\n\n"
	bad := "data: " + wideRootEvent("dropped", aggregateMaxObjectMembers+1) + "\n\n"
	after := "data: " + deltaEvent(`{"content":"after"}`) + "\n\n"
	_, err := dw.Write([]byte(good))
	require.NoError(t, err)
	_, err = dw.Write([]byte(good + bad + after))
	require.NoError(t, err)
	require.Error(t, dw.shapeErr)
	body := rec.Body.String()
	require.Equal(t, 2, strings.Count(body, "kept"))
	require.NotContains(t, body, "dropped")
	require.NotContains(t, body, "after")

	(&Proxy{escrowID: "escrow"}).finishShapeRejectedStream(context.Background(), rec, dw, time.Now())
	require.Contains(t, rec.Body.String(), aggregateFoldTooLargeJSON)
}

func TestFinishShapeRejectedStream_BeforeHeadersSendsGenericError(t *testing.T) {
	rec := httptest.NewRecorder()
	dw := newDeferredWriter(context.Background(), rec, "escrow", nil)
	_, err := dw.Write([]byte("data: " + wideRootEvent("x", aggregateMaxObjectMembers+1) + "\n\n"))
	require.NoError(t, err)
	require.False(t, dw.started)
	(&Proxy{escrowID: "escrow"}).finishShapeRejectedStream(context.Background(), rec, dw, time.Now())
	require.Equal(t, gatewayStatusCodeForError(ErrAggregateFoldTooLarge), rec.Code)
	require.Contains(t, rec.Body.String(), ErrAggregateFoldTooLarge.Error())
	require.NotContains(t, rec.Body.String(), "object members")
}

func TestRaceWriter_RejectsOversizePreludeBeforePromotion(t *testing.T) {
	ctx := context.Background()
	rec := httptest.NewRecorder()
	dw := newDeferredWriter(ctx, rec, "escrow", nil)
	group := newRaceGroup(ctx, ctx, "escrow", dw)
	first := &raceWriter{group: group, nonce: 1, inf: &inflight{nonce: 1}}
	rival := &raceWriter{group: group, nonce: 2, inf: &inflight{nonce: 2}}
	write := func(writer *raceWriter, p string) {
		t.Helper()
		n, err := writer.Write([]byte(p))
		require.NoError(t, err)
		require.Equal(t, len(p), n)
	}
	write(first, "data: "+deltaEvent(`{"role":"assistant"}`)+"\n\n")
	require.NotEmpty(t, first.inf.pendingBuf)

	wide := "data: " + wideRootEvent("wide", aggregateMaxObjectMembers+1) + "\n\n"
	write(first, wide[:len(wide)/2])
	write(first, wide[len(wide)/2:])
	require.True(t, first.inf.pendingShapeRejected.Load())
	require.True(t, isFailedStreamAttempt(first.inf))
	require.Empty(t, first.inf.pendingBuf)
	require.Empty(t, first.inf.classifyPartial)

	write(first, "data: "+deltaEvent(`{"content":"discarded"}`)+"\n\n")
	require.Zero(t, group.winnerNonce())
	write(rival, "data: "+deltaEvent(`{"content":"served"}`)+"\n\n")
	require.EqualValues(t, 2, group.winnerNonce())
	require.NoError(t, dw.shapeErr)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "served")
	require.NotContains(t, rec.Body.String(), "discarded")
}

func TestScanSSEChunk_StripsNestedInternalFieldsBeforeCountingNodes(t *testing.T) {
	tokens := "[" + strings.TrimSuffix(strings.Repeat("12,", aggregateMaxValueNodes+1), ",") + "]"
	for _, key := range []string{`"token_ids"`, `"\u0074oken_ids"`} {
		t.Run(key, func(t *testing.T) {
			body := sseData(deltaEvent(`{"content":"ok",` + key + ":" + tokens + "}"))
			scan := scanSSEChunk(body)
			require.True(t, scan.HasContent)
			require.False(t, scan.RejectedShape)

			out, err := rewriteStreamingPayloadChecked(body, clientResponseIntent{})
			require.NoError(t, err)
			require.Contains(t, string(out), `"content":"ok"`)
			require.NotContains(t, string(out), tokens)
			folded := aggregateSSEStreamReader(bytes.NewReader(body), clientResponseIntent{})
			require.Contains(t, string(folded), `"content":"ok"`)
			require.NotContains(t, string(folded), tokens)
		})
	}
}
