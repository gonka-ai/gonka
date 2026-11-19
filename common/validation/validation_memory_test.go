package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

// TestExecuteValidationAllocatesEachPayloadOnce measures the bytes a validation
// allocates against the previous sequence: the stored response was decoded
// twice (a preflight parse, then the parse that was kept) and the replay body
// was decoded three times (twice inside NormalizeReplayStream, then the parse
// that was kept). The stored response was also decoded into a generic map
// just to notice the "events" key.
//
// One validation now decodes the stored response once and the replay once.
func TestExecuteValidationAllocatesEachPayloadOnce(t *testing.T) {
	const tokens = 800
	original, replay := validationMemoryPayloads(tokens)

	// Warm the one-time decoder and logging paths so they sit outside the delta.
	_, err := ExecuteValidation(
		context.Background(), "warmup",
		minimalPrompt, original,
		staticExecutor(200, replay),
		10, uint64(tokens), "processed_logprobs", 0,
	)
	require.NoError(t, err)

	oneOriginal := allocated(func() {
		parsed, err := UnmarshalResponsePayload(original)
		require.NoError(t, err)
		payloadSink = parsed
	})
	oneMap := allocated(func() {
		var generic map[string]interface{}
		require.NoError(t, json.Unmarshal(original, &generic))
		mapSink = generic
	})
	oneReplay := allocated(func() {
		parsed, err := parseReplayResponse(replay)
		require.NoError(t, err)
		payloadSink = parsed
	})
	current := allocated(func() {
		result, err := ExecuteValidation(
			context.Background(), "measure",
			minimalPrompt, original,
			staticExecutor(200, replay),
			10, uint64(tokens), "processed_logprobs", 0,
		)
		require.NoError(t, err)
		payloadSinkResult = result
	})

	// The previous validation decoded the stored response twice (preflight,
	// then the kept parse) and built the generic map on each of those, and
	// decoded the replay three times. Prompt handling and the logit compare
	// are in both numbers.
	previous := current + oneOriginal + 2*oneMap + 2*oneReplay
	cut := float64(previous) / float64(current)
	t.Logf("one stored response %d bytes, events map %d bytes, one replay %d bytes", oneOriginal, oneMap, oneReplay)
	t.Logf("validation before %d bytes, validation now %d bytes, cut %.2fx", previous, current, cut)
	if cut < 2 {
		t.Fatalf("validation allocated %d bytes, previously %d (%.2fx); want at least 2x", current, previous, cut)
	}
}

var (
	payloadSink       completionapi.CompletionResponse
	payloadSinkResult ValidationResult
	mapSink           map[string]interface{}
)

// validationMemoryPayloads builds a stored streamed response and a replay SSE
// body with ForcedTopLogprobs alternatives on every token.
func validationMemoryPayloads(tokens int) (original, replay []byte) {
	lp := -0.1
	events := make([]string, 0, tokens+1)
	var sse strings.Builder
	for range tokens {
		top := make([]map[string]interface{}, completionapi.ForcedTopLogprobs)
		for rank := range top {
			top[rank] = map[string]interface{}{"token": strconv.Itoa(42 + rank), "logprob": lp}
		}
		chunk, err := json.Marshal(map[string]interface{}{
			"id":     "mem",
			"object": "chat.completion.chunk",
			"choices": []map[string]interface{}{{
				"index": 0,
				"delta": map[string]interface{}{"content": "x"},
				"logprobs": map[string]interface{}{"content": []interface{}{
					map[string]interface{}{"token": "42", "logprob": lp, "top_logprobs": top},
				}},
			}},
		})
		if err != nil {
			panic(err)
		}
		line := "data: " + string(chunk)
		events = append(events, line)
		sse.WriteString(line)
		sse.WriteString("\n\n")
	}
	events = append(events, "data: [DONE]")
	original, err := json.Marshal(map[string]interface{}{"events": events})
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(&sse, "data: {\"id\":\"mem\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":%d}}\n\n", tokens)
	sse.WriteString("data: [DONE]\n\n")
	return original, []byte(sse.String())
}

func allocated(fn func()) uint64 {
	runtime.GC()
	prev := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(prev)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}
