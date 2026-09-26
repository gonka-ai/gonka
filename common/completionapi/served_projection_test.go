package completionapi

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const answeredJSONBody = `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,` +
	`"message":{"role":"assistant","content":"9 <b>&</b>"},"logprobs":{"content":[{"token":"9","logprob":0.0,"bytes":[57],` +
	`"top_logprobs":[{"token":"9","logprob":0.0,"bytes":[57]},{"token":"8","logprob":-23.125,"bytes":[56]}]}]},` +
	`"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`

var (
	incompressibleStream = []string{INCOMPRESSIBLE, DataPrefix + "[DONE]"}
	keptAliveStream      = []string{": keep-alive", strings.TrimSpace(EVENT), "event: ping", DataPrefix + "[DONE]"}
)

type processedStream struct {
	stored     []byte
	servedHash [32]byte
	forwarded  []string
}

func processStream(t *testing.T, events []string, forwardLogprobs, optimizationEnabled bool) processedStream {
	t.Helper()
	processor := NewExecutorResponseProcessor("dummy-id", forwardLogprobs)
	processor.SetLogprobsOptimization(nil, optimizationEnabled)
	var forwarded []string
	for _, line := range events {
		relayed, err := processor.ProcessStreamedResponse(line)
		require.NoError(t, err)
		forwarded = append(forwarded, relayed)
	}
	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)
	servedHash, err := processor.GetServedHash()
	require.NoError(t, err)
	return processedStream{stored: stored, servedHash: servedHash, forwarded: forwarded}
}

func receivedSums(lines []string) [][32]byte {
	hasher := NewReceivedResponseHasher()
	for _, line := range lines {
		hasher.Add(line)
	}
	return hasher.Sums()
}

// Test flow:
//  1. Run each stream through the executor processor with the case's logprobs request and optimization setting.
//  2. Strip the stored envelope the way a validator would and compare its hash to the processor's served hash.
//  3. Hash the forwarded lines the way the gateway would and assert they match the stored hash when the caller asked or the optimization is off, the served hash otherwise.
func TestServedBytesAreTheStripOfTheStoredBytes(t *testing.T) {
	for _, testCase := range []struct {
		name                string
		events              []string
		forwardLogprobs     bool
		optimizationEnabled bool
		wantForwardedStored bool
	}{
		{name: "optimized, caller asked", events: answeredStream, forwardLogprobs: true, optimizationEnabled: true, wantForwardedStored: true},
		{name: "optimized, caller did not ask", events: answeredStream, optimizationEnabled: true},
		{name: "optimized, refused stream", events: refusedStream, optimizationEnabled: true},
		{name: "optimized, chunk will not compress", events: incompressibleStream, optimizationEnabled: true},
		{name: "not optimized, caller asked", events: answeredStream, forwardLogprobs: true, wantForwardedStored: true},
		{name: "not optimized, caller did not ask", events: answeredStream, wantForwardedStored: true},
		{name: "not optimized, refused stream", events: refusedStream, wantForwardedStored: true},
		{name: "optimized, stream with non-data lines", events: keptAliveStream, optimizationEnabled: true},
		{name: "not optimized, stream with non-data lines", events: keptAliveStream, wantForwardedStored: true},
		{name: "not optimized, caller asked, stream with non-data lines", events: keptAliveStream, forwardLogprobs: true, wantForwardedStored: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			processed := processStream(t, testCase.events, testCase.forwardLogprobs, testCase.optimizationEnabled)

			stripped, err := StripForGateway(processed.stored)
			require.NoError(t, err)
			require.Equal(t, sha256.Sum256(stripped), processed.servedHash, "a validator re-derives the served view from the stored one")
			require.NotContains(t, string(stripped), "logprobs")

			wantSum := processed.servedHash
			if testCase.wantForwardedStored {
				wantSum = sha256.Sum256(processed.stored)
			}
			require.Contains(t, receivedSums(processed.forwarded), wantSum,
				"the gateway rebuilds one of the two signed views from the wire: %v", processed.forwarded)
		})
	}
}

// Test flow:
//  1. Process one plain JSON completion with the optimization on, once for a caller that asked for logprobs and once for one that did not.
//  2. Assert the served hash equals the hash of StripForGateway over the stored body.
//  3. Relay the forwarded body as one data line plus [DONE] and assert the gateway's bare hash matches the view that caller was owed.
func TestServedBytesOfAJSONBodyAreTheStripOfTheStoredBody(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		forwardLogprobs bool
	}{
		{name: "caller asked for logprobs", forwardLogprobs: true},
		{name: "caller did not ask", forwardLogprobs: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			forwardLogprobs := testCase.forwardLogprobs
			processor := NewExecutorResponseProcessor("dummy-id", forwardLogprobs)
			processor.SetLogprobsOptimization(nil, true)
			forwarded, err := processor.ProcessJsonResponse([]byte(answeredJSONBody))
			require.NoError(t, err)

			stored, err := processor.GetResponseBytes()
			require.NoError(t, err)
			servedHash, err := processor.GetServedHash()
			require.NoError(t, err)
			stripped, err := StripForGateway(stored)
			require.NoError(t, err)
			require.Equal(t, sha256.Sum256(stripped), servedHash)

			relayedLines := []string{DataPrefix + string(forwarded), DataPrefix + "[DONE]"}
			wantSum := servedHash
			if forwardLogprobs {
				wantSum = sha256.Sum256(stored)
			}
			require.Contains(t, receivedSums(relayedLines), wantSum, "a relayed body is hashed bare, as it was stored")
		})
	}
}

// Test flow:
//  1. Store a stream whole, with logprobs kept.
//  2. Strip it twice and assert the second strip changes nothing.
func TestStripForGatewayIsIdempotent(t *testing.T) {
	processed := processStream(t, answeredStream, true, false)
	once, err := StripForGateway(processed.stored)
	require.NoError(t, err)
	twice, err := StripForGateway(once)
	require.NoError(t, err)
	require.Equal(t, string(once), string(twice))
}

// Test flow:
//  1. Build a stored envelope with a comment line, a chunk carrying logprobs, a non-JSON data line and [DONE].
//  2. Strip it and assert every line survives in order, and only the chunk lost its logprobs.
func TestStripForGatewayDropsOnlyLogprobs(t *testing.T) {
	stored, err := json.Marshal(SerializedStreamedResponse{Events: []string{
		": keep-alive",
		strings.TrimSpace(EVENT),
		"data: not json",
		DataPrefix + "[DONE]",
	}})
	require.NoError(t, err)

	stripped, err := StripForGateway(stored)
	require.NoError(t, err)
	var envelope SerializedStreamedResponse
	require.NoError(t, json.Unmarshal(stripped, &envelope))
	require.Len(t, envelope.Events, 4, "every stored line reaches the gateway: %s", stripped)
	require.Equal(t, ": keep-alive", envelope.Events[0])
	require.Contains(t, envelope.Events[1], `"content":"9"`)
	require.NotContains(t, envelope.Events[1], "logprobs")
	require.Equal(t, "data: not json", envelope.Events[2])
	require.Equal(t, DataPrefix+"[DONE]", envelope.Events[3])
}

// Test flow:
//  1. Process an answered stream and keep its forwarded lines.
//  2. Change the answer's content in one forwarded line.
//  3. Assert no hash the gateway computes matches either signed view.
func TestReceivedResponseHasherDetectsAChangedLine(t *testing.T) {
	processed := processStream(t, answeredStream, false, true)
	tampered := append([]string(nil), processed.forwarded...)
	tampered[0] = strings.Replace(tampered[0], `"content":"9"`, `"content":"8"`, 1)

	storedSum := sha256.Sum256(processed.stored)
	tamperedSums := receivedSums(tampered)
	require.NotEmpty(t, tamperedSums)
	for _, sum := range tamperedSums {
		require.NotEqual(t, processed.servedHash, sum)
		require.NotEqual(t, storedSum, sum)
	}
}

// Test flow:
//  1. Process a stream that carries only a comment line.
//  2. Assert the served hash still equals the hash of StripForGateway over the stored envelope.
func TestServedHashOfAStreamWithoutDataLinesIsTheStripOfItsStoredBytes(t *testing.T) {
	processed := processStream(t, []string{": keep-alive"}, false, true)
	stripped, err := StripForGateway(processed.stored)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(stripped), processed.servedHash)
}

// Test flow:
//  1. Add no lines to a fresh hasher.
//  2. Assert it reports no hash, so an empty stream is never treated as bound.
func TestReceivedResponseHasherHasNothingForAnEmptyStream(t *testing.T) {
	require.Empty(t, NewReceivedResponseHasher().Sums())
}

// Test flow:
//  1. Process a stream whose comment line carries a byte that is not UTF-8, which the stored envelope escapes.
//  2. Strip the stored envelope the way a validator would.
//  3. Assert its hash equals the served hash the executor signed, so an honest executor is not voted invalid.
func TestStripForGatewayKeepsAnEscapedNonDataLineByteForByte(t *testing.T) {
	processed := processStream(t, []string{": comment \xfe", strings.TrimSpace(EVENT), DataPrefix + "[DONE]"}, false, true)

	stripped, err := StripForGateway(processed.stored)
	require.NoError(t, err)
	require.Equal(t, processed.servedHash, sha256.Sum256(stripped))
}

// Test flow:
//  1. Process a plain JSON completion that happens to carry a top-level events array.
//  2. Strip the stored body the way a validator would.
//  3. Assert it is treated as a completion, not a stream envelope, so its hash equals the executor's served hash.
func TestStripForGatewayDoesNotMistakeABodyWithAnEventsFieldForAnEnvelope(t *testing.T) {
	processor := NewExecutorResponseProcessor("dummy-id", false)
	processor.SetLogprobsOptimization(nil, true)
	_, err := processor.ProcessJsonResponse([]byte(`{"events":[1],"choices":[{"index":0,"message":{"content":"9"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	require.NoError(t, err)
	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)
	servedHash, err := processor.GetServedHash()
	require.NoError(t, err)

	stripped, err := StripForGateway(stored)
	require.NoError(t, err)
	require.Equal(t, servedHash, sha256.Sum256(stripped))
}

// Test flow:
//  1. Feed the hasher the lines of a stream cut before its [DONE], as a clean EOF mid-stream leaves them.
//  2. Assert it reports no hash, so a stream that never completed is not judged against the executor's Finish.
func TestReceivedResponseHasherHasNothingForAStreamCutBeforeDone(t *testing.T) {
	processed := processStream(t, answeredStream, false, true)

	require.Empty(t, receivedSums(processed.forwarded[:len(processed.forwarded)-1]))
}
