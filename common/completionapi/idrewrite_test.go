package completionapi

// Differential tests and benchmarks for the per-chunk inference-id rewrite.
//
// Production streamed path is the memoized surgical patcher in idrewrite.go
// (wired through ExecutorResponseProcessor). The stdlib / goccy map round-trips
// remain here as reference candidates: same document semantically; goccy is
// byte-identical to stdlib; surgical rewrite preserves upstream formatting and
// therefore moves ResponseHash (documented migration cost).
//
// See devshard/docs/gateway-attempt-reconnect-plan.md, Step 5b.

import (
	stdjson "encoding/json"
	"strings"
	"testing"

	goccyjson "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

// --- reference candidates (map round-trip) --------------------------------

func idRewriteStdlib(line, id string) (string, error) {
	if !strings.HasPrefix(line, DataPrefix) {
		return line, nil
	}
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, DataPrefix))
	if strings.HasPrefix(trimmed, "[DONE]") {
		return line, nil
	}
	var bodyMap map[string]interface{}
	if err := stdjson.Unmarshal([]byte(trimmed), &bodyMap); err != nil {
		return line, err
	}
	bodyMap["id"] = id
	out, err := stdjson.Marshal(bodyMap)
	if err != nil {
		return line, err
	}
	return DataPrefix + string(out), nil
}

func idRewriteGoccy(line, id string) (string, error) {
	if !strings.HasPrefix(line, DataPrefix) {
		return line, nil
	}
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, DataPrefix))
	if strings.HasPrefix(trimmed, "[DONE]") {
		return line, nil
	}
	var bodyMap map[string]interface{}
	if err := goccyjson.Unmarshal([]byte(trimmed), &bodyMap); err != nil {
		return line, err
	}
	bodyMap["id"] = id
	out, err := goccyjson.Marshal(bodyMap)
	if err != nil {
		return line, err
	}
	return DataPrefix + string(out), nil
}

// --- fixtures -------------------------------------------------------------

const benchInferenceID = "gonka-inference-0000000000000001"

// dataChunks returns the non-blank, non-[DONE] `data:` lines of a fixture.
func dataChunks(t testing.TB, name string) []string {
	t.Helper()
	f, ok := t.(*testing.T)
	var lines []string
	if ok {
		lines = readLines(f, name)
	} else {
		lines = readLinesTB(t, name)
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.HasPrefix(line, DataPrefix) {
			continue
		}
		if strings.Contains(line, "[DONE]") {
			continue
		}
		out = append(out, line)
	}
	require.NotEmpty(t, out, "fixture %s yielded no data chunks", name)
	return out
}

func readLinesTB(tb testing.TB, name string) []string {
	tb.Helper()
	data, err := loadJson(name)
	if err != nil {
		tb.Fatalf("read fixture: %v", err)
	}
	return strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
}

func semanticJSON(tb testing.TB, line string) map[string]interface{} {
	tb.Helper()
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, DataPrefix))
	var m map[string]interface{}
	require.NoError(tb, stdjson.Unmarshal([]byte(trimmed), &m), "unmarshal %s", trimmed)
	return m
}

// --- tests ----------------------------------------------------------------

// The whole optimization rests on this: every candidate must produce the same
// document, differing only in key order and whitespace.
func TestIDRewriteCandidatesAgree(t *testing.T) {
	for _, fixture := range []string{
		"test_data/response_streamed.txt",
		"test_data/response_streamed_token_ids.txt",
	} {
		t.Run(fixture, func(t *testing.T) {
			chunks := dataChunks(t, fixture)
			patcher := &idPatcher{id: benchInferenceID}
			var buf []byte

			for i, chunk := range chunks {
				want, err := idRewriteStdlib(chunk, benchInferenceID)
				require.NoError(t, err, "chunk %d", i)
				wantDoc := semanticJSON(t, want)
				require.Equal(t, benchInferenceID, wantDoc["id"])

				goccy, err := idRewriteGoccy(chunk, benchInferenceID)
				require.NoError(t, err, "chunk %d", i)
				require.Equal(t, want, goccy,
					"goccy must be a byte-identical drop-in (chunk %d)", i)

				buf, err = idRewriteSplice(buf[:0], []byte(chunk), benchInferenceID)
				require.NoError(t, err, "chunk %d", i)
				require.Equal(t, wantDoc, semanticJSON(t, string(buf)),
					"splice diverged semantically (chunk %d)", i)

				buf, err = patcher.rewrite(buf[:0], []byte(chunk))
				require.NoError(t, err, "chunk %d", i)
				require.Equal(t, wantDoc, semanticJSON(t, string(buf)),
					"memo patch diverged semantically (chunk %d)", i)
			}

			// One learn for the stream, then pure pattern hits.
			require.Equal(t, 1, patcher.learns, "expected a single learn per stream")
			require.Equal(t, len(chunks)-1, patcher.hits)
		})
	}
}

// The surgical candidates must leave everything except the id value untouched —
// that is what makes them cheap, and what makes the output differ from today's
// alphabetized re-marshal.
func TestIDRewriteSplicePreservesUpstreamBytes(t *testing.T) {
	chunks := dataChunks(t, "test_data/response_streamed.txt")
	const upstreamID = "chatcmpl-747d884a63684dd78424cc8333fa3399"

	out, err := idRewriteSplice(nil, []byte(chunks[0]), benchInferenceID)
	require.NoError(t, err)
	require.Equal(t,
		strings.Replace(chunks[0], upstreamID, benchInferenceID, 1),
		string(out),
		"splice must only touch the id value")

	stdlib, err := idRewriteStdlib(chunks[0], benchInferenceID)
	require.NoError(t, err)
	require.NotEqual(t, string(out), stdlib,
		"sanity: today's path reorders keys, so bytes are expected to differ")
}

// Pins the exact blast radius of switching to a surgical rewrite.
//
// Everything validation consumes — the typed event stream, usage, inference id,
// the reconstructed completion text — is unchanged, because those all come from
// unmarshalling each event into a typed struct where key order is irrelevant.
//
// The response *hash* does change, and that is not incidental:
// StreamedCompletionResponse.GetHash hashes json.Marshal of the raw event lines,
// so it is sensitive to chunk formatting. It stays self-consistent (the hash is
// always recomputed from the same stored payload, never compared against a body
// generated elsewhere), but it means a surgical rewrite is a coordinated change
// with a byte-form migration — which is why goccy (byte-identical) is the safe
// first step and the splice is a second, deliberate one.
func TestIDRewriteDownstreamSemanticsUnchanged(t *testing.T) {
	for _, fixture := range []string{
		"test_data/response_streamed.txt",
		"test_data/response_streamed_token_ids.txt",
	} {
		t.Run(fixture, func(t *testing.T) {
			chunks := dataChunks(t, fixture)

			stdlibLines := make([]string, 0, len(chunks))
			spliceLines := make([]string, 0, len(chunks))
			var buf []byte
			for _, chunk := range chunks {
				line, err := idRewriteStdlib(chunk, benchInferenceID)
				require.NoError(t, err)
				stdlibLines = append(stdlibLines, line)

				buf, err = idRewriteSplice(buf[:0], []byte(chunk), benchInferenceID)
				require.NoError(t, err)
				spliceLines = append(spliceLines, string(buf))
			}

			stdlibResp, err := NewCompletionResponseFromLines(stdlibLines)
			require.NoError(t, err)
			spliceResp, err := NewCompletionResponseFromLines(spliceLines)
			require.NoError(t, err)

			stdlibTyped, ok := stdlibResp.(*StreamedCompletionResponse)
			require.True(t, ok)
			spliceTyped, ok := spliceResp.(*StreamedCompletionResponse)
			require.True(t, ok)
			require.Equal(t, stdlibTyped.Resp, spliceTyped.Resp,
				"typed event stream must be identical; only formatting may differ")

			stdlibID, err := stdlibResp.GetInferenceId()
			require.NoError(t, err)
			require.Equal(t, benchInferenceID, stdlibID)
			spliceID, err := spliceResp.GetInferenceId()
			require.NoError(t, err)
			require.Equal(t, benchInferenceID, spliceID)

			// Not every fixture carries a usage chunk, so compare outcomes
			// rather than requiring success.
			stdlibUsage, stdlibUsageErr := stdlibResp.GetUsage()
			spliceUsage, spliceUsageErr := spliceResp.GetUsage()
			require.Equal(t, stdlibUsageErr == nil, spliceUsageErr == nil)
			require.Equal(t, stdlibUsage, spliceUsage)

			stdlibText, stdlibTextErr := stdlibResp.GetEnforcedStr()
			spliceText, spliceTextErr := spliceResp.GetEnforcedStr()
			require.Equal(t, stdlibTextErr == nil, spliceTextErr == nil)
			require.Equal(t, stdlibText, spliceText,
				"completion text must be byte-identical")

			stdlibHash, err := stdlibResp.GetHash()
			require.NoError(t, err)
			spliceHash, err := spliceResp.GetHash()
			require.NoError(t, err)
			require.NotEqual(t, stdlibHash, spliceHash,
				"documented cost: GetHash covers the raw event lines, so a "+
					"formatting change moves ResponseHash and needs a migration")
		})
	}
}

// goccy must be a true drop-in: same bytes in, same bytes out, same hash. This
// is what makes it deployable without any byte-form migration.
func TestIDRewriteGoccyPreservesHash(t *testing.T) {
	for _, fixture := range []string{
		"test_data/response_streamed.txt",
		"test_data/response_streamed_token_ids.txt",
	} {
		t.Run(fixture, func(t *testing.T) {
			chunks := dataChunks(t, fixture)
			stdlibLines := make([]string, 0, len(chunks))
			goccyLines := make([]string, 0, len(chunks))
			for _, chunk := range chunks {
				line, err := idRewriteStdlib(chunk, benchInferenceID)
				require.NoError(t, err)
				stdlibLines = append(stdlibLines, line)

				line, err = idRewriteGoccy(chunk, benchInferenceID)
				require.NoError(t, err)
				goccyLines = append(goccyLines, line)
			}

			stdlibResp, err := NewCompletionResponseFromLines(stdlibLines)
			require.NoError(t, err)
			goccyResp, err := NewCompletionResponseFromLines(goccyLines)
			require.NoError(t, err)

			stdlibHash, err := stdlibResp.GetHash()
			require.NoError(t, err)
			goccyHash, err := goccyResp.GetHash()
			require.NoError(t, err)
			require.Equal(t, stdlibHash, goccyHash,
				"goccy must not move ResponseHash")
		})
	}
}

func TestIDRewriteEdgeCases(t *testing.T) {
	t.Run("done marker passes through", func(t *testing.T) {
		line := "data: [DONE]"
		out, err := idRewriteSplice(nil, []byte(line), benchInferenceID)
		require.NoError(t, err)
		require.Equal(t, line, string(out))

		std, err := idRewriteStdlib(line, benchInferenceID)
		require.NoError(t, err)
		require.Equal(t, line, std)
	})

	t.Run("non-data line passes through", func(t *testing.T) {
		line := ": keep-alive"
		out, err := idRewriteSplice(nil, []byte(line), benchInferenceID)
		require.NoError(t, err)
		require.Equal(t, line, string(out))
	})

	t.Run("id absent is inserted", func(t *testing.T) {
		line := `data: {"object":"chat.completion.chunk","choices":[]}`
		out, err := idRewriteSplice(nil, []byte(line), benchInferenceID)
		require.NoError(t, err)
		require.Equal(t, benchInferenceID, semanticJSON(t, string(out))["id"])
	})

	t.Run("empty object gets id without stray comma", func(t *testing.T) {
		line := `data: {}`
		out, err := idRewriteSplice(nil, []byte(line), benchInferenceID)
		require.NoError(t, err)
		require.Equal(t, `data: {"id":"`+benchInferenceID+`"}`, string(out))
	})

	t.Run("id not first key", func(t *testing.T) {
		line := `data: {"object":"chunk","id":"upstream-1","choices":[]}`
		out, err := idRewriteSplice(nil, []byte(line), benchInferenceID)
		require.NoError(t, err)
		require.Equal(t,
			`data: {"object":"chunk","id":"`+benchInferenceID+`","choices":[]}`,
			string(out))
	})

	// The collision the memoized fast path must not fall for.
	t.Run("nested tool_call id untouched", func(t *testing.T) {
		line := `data: {"id":"upstream-1","choices":[{"delta":{"tool_calls":[{"id":"call_abc","index":0}]}}]}`
		out, err := idRewriteSplice(nil, []byte(line), benchInferenceID)
		require.NoError(t, err)
		require.Contains(t, string(out), `"id":"call_abc"`)
		require.Equal(t, benchInferenceID, semanticJSON(t, string(out))["id"])
	})

	// Content that literally spells out an `"id":"…"` pair before the real key
	// would defeat a naive bytes.Index; the offset-anchored patcher ignores it.
	t.Run("id-like text in content", func(t *testing.T) {
		line := `data: {"id":"upstream-1","choices":[{"delta":{"content":"{\"id\":\"upstream-1\"}"}}]}`
		out, err := idRewriteSplice(nil, []byte(line), benchInferenceID)
		require.NoError(t, err)
		doc := semanticJSON(t, string(out))
		require.Equal(t, benchInferenceID, doc["id"])
		require.Contains(t, string(out), `{\"id\":\"upstream-1\"}`,
			"content must be preserved verbatim")
	})

	t.Run("truncated after id: splice succeeds where the map path errors", func(t *testing.T) {
		// A behavior difference worth pinning: the map round-trip validates the
		// whole chunk as a side effect, so a truncated line is diverted to the
		// error branch (retained by the processor, never written to the hub).
		// A surgical rewrite stops once it has the depth-1 id, so the chunk flows
		// through and both copies agree. That removes the error-line divergence
		// 5b has to work around, but it also means malformed chunks are no longer
		// filtered here — they surface later, at GetResponse.
		line := `data: {"id":"upstream-1",`

		out, err := idRewriteSplice(nil, []byte(line), benchInferenceID)
		require.NoError(t, err)
		require.Contains(t, string(out), benchInferenceID)

		_, err = idRewriteStdlib(line, benchInferenceID)
		require.Error(t, err)

		_, err = NewCompletionResponseFromLines([]string{string(out)})
		require.Error(t, err, "malformed chunk still fails at finish, not at drain")
	})

	t.Run("malformed before id is reported", func(t *testing.T) {
		line := `data: {"object" "chunk","id":"upstream-1"}`
		_, err := idRewriteSplice(nil, []byte(line), benchInferenceID)
		require.Error(t, err)
	})
}

// A stream whose chunk layout shifts must degrade to a re-learn, not corruption.
func TestIDPatcherRelearnsWhenLayoutShifts(t *testing.T) {
	patcher := &idPatcher{id: benchInferenceID}
	var buf []byte

	first := `data: {"id":"upstream-1","object":"chunk"}`
	buf, err := patcher.rewrite(buf[:0], []byte(first))
	require.NoError(t, err)
	require.Equal(t, benchInferenceID, semanticJSON(t, string(buf))["id"])
	require.Equal(t, 1, patcher.learns)

	shifted := `data: {"object":"chunk","id":"upstream-2"}`
	buf, err = patcher.rewrite(buf[:0], []byte(shifted))
	require.NoError(t, err)
	require.Equal(t, benchInferenceID, semanticJSON(t, string(buf))["id"])
	require.Equal(t, 2, patcher.learns, "layout change must force a re-learn")
	require.Equal(t, 0, patcher.hits)
}

// Production processor must use the surgical path: same bytes as splice, not the
// alphabetized map round-trip, and nested tool_call ids must survive.
func TestProcessStreamedResponseUsesSurgicalRewrite(t *testing.T) {
	chunks := dataChunks(t, "test_data/response_streamed.txt")
	processor := NewExecutorResponseProcessor(benchInferenceID)
	var buf []byte

	for i, chunk := range chunks {
		got, err := processor.ProcessStreamedResponse(chunk)
		require.NoError(t, err, "chunk %d", i)

		buf, err = idRewriteSplice(buf[:0], []byte(chunk), benchInferenceID)
		require.NoError(t, err, "chunk %d", i)
		require.Equal(t, string(buf), got, "processor must match surgical splice (chunk %d)", i)

		stdlib, err := idRewriteStdlib(chunk, benchInferenceID)
		require.NoError(t, err, "chunk %d", i)
		require.NotEqual(t, stdlib, got,
			"sanity: production path must not still be the map round-trip (chunk %d)", i)
	}

	require.Equal(t, 1, processor.patcher.learns)
	require.Equal(t, len(chunks)-1, processor.patcher.hits)

	line := `data: {"id":"upstream-1","choices":[{"delta":{"tool_calls":[{"id":"call_abc","index":0}]}}]}`
	out, err := processor.ProcessStreamedResponse(line)
	require.NoError(t, err)
	require.Contains(t, out, `"id":"call_abc"`)
	require.Equal(t, benchInferenceID, semanticJSON(t, out)["id"])
}

func TestIDPatcherRewriteAllocs(t *testing.T) {
	chunks := dataChunks(t, "test_data/response_streamed_token_ids.txt")
	raw := make([][]byte, len(chunks))
	for i, c := range chunks {
		raw[i] = []byte(c)
	}

	patcher := &idPatcher{id: benchInferenceID}
	var buf []byte
	var err error
	// Warm the learn so the measured path is the memoized hit.
	buf, err = patcher.rewrite(buf[:0], raw[0])
	require.NoError(t, err)
	require.Equal(t, 1, patcher.learns)

	allocs := testing.AllocsPerRun(1000, func() {
		for _, chunk := range raw[1:] {
			if buf, err = patcher.rewrite(buf[:0], chunk); err != nil {
				t.Fatal(err)
			}
		}
	})
	require.Equal(t, 0.0, allocs, "memoized rewrite must be allocation-free after learn")
}

func TestAppendIDPairEscapesUnsafeIDs(t *testing.T) {
	out := appendIDPair(nil, `evil"id`)
	require.Equal(t, `"id":"evil\"id"`, string(out))
}

// --- benchmarks -----------------------------------------------------------

func benchmarkFixtures() []struct {
	name string
	path string
} {
	return []struct {
		name string
		path string
	}{
		{"plain", "test_data/response_streamed.txt"},
		{"logprobs_token_ids", "test_data/response_streamed_token_ids.txt"},
	}
}

func BenchmarkIDRewrite(b *testing.B) {
	for _, fixture := range benchmarkFixtures() {
		chunks := dataChunks(b, fixture.path)
		rawChunks := make([][]byte, len(chunks))
		var totalBytes int64
		for i, c := range chunks {
			rawChunks[i] = []byte(c)
			totalBytes += int64(len(c))
		}

		b.Run(fixture.name+"/stdlib_map_roundtrip", func(b *testing.B) {
			b.SetBytes(totalBytes)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for _, chunk := range chunks {
					if _, err := idRewriteStdlib(chunk, benchInferenceID); err != nil {
						b.Fatal(err)
					}
				}
			}
		})

		b.Run(fixture.name+"/goccy_map_roundtrip", func(b *testing.B) {
			b.SetBytes(totalBytes)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for _, chunk := range chunks {
					if _, err := idRewriteGoccy(chunk, benchInferenceID); err != nil {
						b.Fatal(err)
					}
				}
			}
		})

		b.Run(fixture.name+"/splice", func(b *testing.B) {
			b.SetBytes(totalBytes)
			b.ReportAllocs()
			var buf []byte
			var err error
			for i := 0; i < b.N; i++ {
				for _, chunk := range rawChunks {
					if buf, err = idRewriteSplice(buf[:0], chunk, benchInferenceID); err != nil {
						b.Fatal(err)
					}
				}
			}
		})

		b.Run(fixture.name+"/memo_patch", func(b *testing.B) {
			b.SetBytes(totalBytes)
			b.ReportAllocs()
			patcher := &idPatcher{id: benchInferenceID}
			var buf []byte
			var err error
			for i := 0; i < b.N; i++ {
				for _, chunk := range rawChunks {
					if buf, err = patcher.rewrite(buf[:0], chunk); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// BenchmarkProcessStreamedResponse measures the production entry point, i.e. the
// rewrite plus the per-line retention that 5b wants to share with the live log.
func BenchmarkProcessStreamedResponse(b *testing.B) {
	for _, fixture := range benchmarkFixtures() {
		chunks := dataChunks(b, fixture.path)
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				processor := NewExecutorResponseProcessor(benchInferenceID)
				for _, chunk := range chunks {
					if _, err := processor.ProcessStreamedResponse(chunk); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
