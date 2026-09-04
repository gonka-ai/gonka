package completionapi

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func position(sampled string, sampledLogprob float64, alternatives ...TopLogprobs) Logprob {
	return Logprob{Token: sampled, Logprob: sampledLogprob, Bytes: []int{32, 97}, TopLogprobs: alternatives}
}

func alternative(token string, logprob float64) TopLogprobs {
	spelled := make([]int, len(token))
	for index := range token {
		spelled[index] = int(token[index])
	}
	return TopLogprobs{Token: token, Logprob: logprob, Bytes: spelled}
}

// The compressed form parses back into the type validation reads, with the same floats.
func TestCompressedLogprobsRoundTripThroughTheOrdinaryType(t *testing.T) {
	t.Parallel()
	content := []Logprob{
		position("758", 0, alternative("758", 0), alternative("2", -9999)),
		position("258", -0.9628103971481323,
			alternative("258", -0.9628103971481323),
			alternative("494", -1.2128103971481323),
			alternative("653", -1.7128103971481323)),
	}
	payload, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"logprobs": map[string]any{"content": content}}}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	compressed, err := CompressResponsePayload(payload)
	if err != nil {
		t.Fatalf("CompressResponsePayload: %v", err)
	}
	var restored struct {
		Choices []struct {
			Logprobs struct {
				Content []Logprob `json:"content"`
			} `json:"logprobs"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(compressed, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	got := restored.Choices[0].Logprobs.Content
	if len(got) != len(content) {
		t.Fatalf("restored %d positions, want %d", len(got), len(content))
	}
	for index, entry := range content {
		if got[index].Token != entry.Token {
			t.Fatalf("position %d: token %q, want %q", index, got[index].Token, entry.Token)
		}
		if len(got[index].TopLogprobs) != len(entry.TopLogprobs) {
			t.Fatalf("position %d: %d alternatives, want %d", index, len(got[index].TopLogprobs), len(entry.TopLogprobs))
		}
		for rank, want := range entry.TopLogprobs {
			if alternative := got[index].TopLogprobs[rank]; alternative.Token != want.Token || alternative.Logprob != want.Logprob {
				t.Fatalf("position %d rank %d: got %q/%v, want %q/%v", index, rank, alternative.Token, alternative.Logprob, want.Token, want.Logprob)
			}
			if len(got[index].TopLogprobs[rank].Bytes) != 0 {
				t.Fatalf("position %d rank %d still carries bytes", index, rank)
			}
		}
		if len(got[index].Bytes) != 0 || got[index].Logprob != 0 {
			t.Fatalf("position %d still carries its own bytes or logprob", index)
		}
	}
}

// Dropping a field is only safe while it is redundant. When it stops being so the payload is refused,
// and the executor stores the response whole rather than losing what it could not verify.
func TestCompressRefusesWhatIsNotRedundant(t *testing.T) {
	t.Parallel()
	payloadFor := func(entry Logprob) []byte {
		body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"logprobs": map[string]any{"content": []Logprob{entry}}}}})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return body
	}

	honest := payloadFor(position("758", -0.5, alternative("758", -0.5), alternative("2", -9999)))
	if _, err := CompressResponsePayload(honest); err != nil {
		t.Fatalf("CompressResponsePayload(honest) = %v, want nil", err)
	}

	textBytes := payloadFor(position("758", -0.5, TopLogprobs{Token: "758", Logprob: -0.5, Bytes: []int{84, 104, 101}}))
	if _, err := CompressResponsePayload(textBytes); err == nil {
		t.Fatal("compressed an alternative whose bytes are not its token")
	}

	unmatched := payloadFor(position("999", -0.5, alternative("758", -0.5)))
	if _, err := CompressResponsePayload(unmatched); err == nil {
		t.Fatal("compressed a position whose logprob no alternative explains")
	}

	drifted := payloadFor(position("758", -0.25, alternative("758", -0.5)))
	if _, err := CompressResponsePayload(drifted); err == nil {
		t.Fatal("compressed a position whose logprob disagrees with its own alternative")
	}
}

// A refusal leaves the document untouched, so a caller can marshal it after the attempt.
func TestCompressLeavesADocumentItRefusesExactlyAsItArrived(t *testing.T) {
	t.Parallel()
	honest := position("758", -0.5, alternative("758", -0.5))
	unexplained := position("999", -0.5, alternative("758", -0.5))
	body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"logprobs": map[string]any{"content": []Logprob{honest, unexplained}}}}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	document, err := decodeJSONDocument(body)
	if err != nil {
		t.Fatalf("decodeJSONDocument: %v", err)
	}
	if err := compressLogprobsIn(document); err == nil {
		t.Fatal("compressed a document whose second position no alternative explains")
	}

	refused, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var same, arrived any
	if err := json.Unmarshal(refused, &same); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := json.Unmarshal(body, &arrived); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(same, arrived) {
		t.Fatalf("the refused document was mutated:\n got %s\nwant %s", refused, body)
	}
}

// Both shapes a stored payload takes must compress.
// Inside the envelope a key reads as \"bytes\", which no search for "bytes" matches, so assertions
// have to land on the decoded chunk rather than on the envelope's own bytes.
func storedChunks(t *testing.T, payload []byte) []map[string]any {
	t.Helper()
	var envelope SerializedStreamedResponse
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("the stored payload is not a streamed envelope: %v", err)
	}
	if len(envelope.Events) == 0 {
		t.Fatal("the stored envelope carries no events")
	}
	chunks := make([]map[string]any, 0, len(envelope.Events))
	for _, event := range envelope.Events {
		body, isData := streamedLineBody(event)
		if !isData {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(body), &chunk); err != nil {
			t.Fatalf("a stored event is not JSON: %v", err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		t.Fatal("the stored envelope carries no data events")
	}
	return chunks
}

func requireNoField(t *testing.T, node any, field string, where string) {
	t.Helper()
	switch typed := node.(type) {
	case map[string]any:
		if _, present := typed[field]; present {
			t.Fatalf("%s still carries %q", where, field)
		}
		for _, child := range typed {
			requireNoField(t, child, field, where)
		}
	case []any:
		for _, child := range typed {
			requireNoField(t, child, field, where)
		}
	}
}

func TestSlimResponsePayloadHandlesBothShapes(t *testing.T) {
	t.Parallel()
	positionJSON := `{"token":"258","logprob":-0.9628103971481323,"bytes":[32,97],"top_logprobs":[{"token":"258","logprob":-0.9628103971481323,"bytes":[50,53,56]},{"token":"0","logprob":-9999,"bytes":[48]}]}`
	shapes := map[string]string{
		"plain completion": `{"id":"x","created":1786458557,"choices":[{"index":0,"message":{"content":"Hi"},"logprobs":{"content":[` + positionJSON + `]}}],"usage":{"prompt_tokens":123187}}`,
		"streamed envelope": `{"events":["data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"logprobs\":{\"content\":[` +
			`{\"token\":\"258\",\"logprob\":-0.5,\"bytes\":[32,97],\"top_logprobs\":[{\"token\":\"258\",\"logprob\":-0.5,\"bytes\":[50,53,56]}]}]}}]}"]}`,
	}

	for name, payload := range shapes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			compressed, err := CompressResponsePayload([]byte(payload))
			if err != nil {
				t.Fatalf("CompressResponsePayload: %v", err)
			}
			inspected := any(nil)
			if name == "streamed envelope" {
				for _, chunk := range storedChunks(t, compressed) {
					inspected = chunk
					requireNoField(t, chunk, "bytes", name)
				}
			} else {
				if err := json.Unmarshal(compressed, &inspected); err != nil {
					t.Fatalf("the compressed payload is not JSON: %v", err)
				}
				requireNoField(t, inspected, "bytes", name)
			}

			response, err := NewCompletionResponseFromLinesFromResponsePayload(compressed)
			if err != nil {
				t.Fatalf("the compressed payload no longer parses: %v", err)
			}
			enforced, err := response.GetEnforcedTokens()
			if err != nil {
				t.Fatalf("GetEnforcedTokens: %v", err)
			}
			if len(enforced.Tokens) == 0 {
				t.Fatal("the compressed payload carries no enforced tokens")
			}
			if enforced.Tokens[0].Token != "258" || enforced.Tokens[0].TopTokens[0] != "258" {
				t.Fatalf("enforced tokens came back as %+v", enforced.Tokens[0])
			}
		})
	}
}

// The engine bookkeeping no validator reads must leave the stored payload in either shape.
func TestSlimResponsePayloadDropsWhatNoValidatorReads(t *testing.T) {
	t.Parallel()
	envelope := `{"events":["data: {\"prompt_token_ids\":[1,2],\"prompt_logprobs\":null,\"choices\":[{\"index\":0,\"token_ids\":[3],\"delta\":{\"content\":\"Hi\"}}]}"]}`
	compressed, err := CompressResponsePayload([]byte(envelope))
	if err != nil {
		t.Fatalf("CompressResponsePayload: %v", err)
	}
	for _, chunk := range storedChunks(t, compressed) {
		for _, field := range fieldsNoValidatorReads {
			requireNoField(t, chunk, field, "the stored envelope")
		}
		choices, kept := chunk["choices"].([]any)
		if !kept || len(choices) == 0 {
			t.Fatalf("the walk took the answer with it: %v", chunk)
		}
		delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		if delta["content"] != "Hi" {
			t.Fatalf("the answer did not survive the walk: %v", chunk)
		}
	}
}

// What the walk must not touch: the answer itself, and every number in it.
func TestSlimResponsePayloadLeavesEverythingElseAlone(t *testing.T) {
	t.Parallel()
	payload := `{"id":"x","created":1786458557,"choices":[{"index":0,"message":{"content":"Hi","reasoning":"because"},"logprobs":{"content":[{"token":"7","logprob":-0.5,"bytes":[55],"top_logprobs":[{"token":"7","logprob":-0.5,"bytes":[55]}]}]}}],"usage":{"prompt_tokens":123187,"completion_tokens":4096}}`
	compressed, err := CompressResponsePayload([]byte(payload))
	if err != nil {
		t.Fatalf("CompressResponsePayload: %v", err)
	}

	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(compressed))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := decoded["created"].(json.Number).String(); got != "1786458557" {
		t.Fatalf("created came back as %q, want the digits it arrived with", got)
	}
	usage := decoded["usage"].(map[string]any)
	if got := usage["prompt_tokens"].(json.Number).String(); got != "123187" {
		t.Fatalf("prompt_tokens came back as %q", got)
	}
	choice := decoded["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "Hi" || message["reasoning"] != "because" {
		t.Fatalf("the answer itself changed: %+v", message)
	}

	position := choice["logprobs"].(map[string]any)["content"].([]any)[0].(map[string]any)
	for _, field := range []string{"bytes", "logprob"} {
		if _, present := position[field]; present {
			t.Fatalf("the compressed position still carries %q", field)
		}
	}
	if got := position["top_logprobs"].([]any)[0].(map[string]any)["logprob"].(json.Number).String(); got != "-0.5" {
		t.Fatalf("an alternative's logprob came back as %q, want the digits it arrived with", got)
	}
}

// A host that wrote one chunk inconsistently must not cost the whole stream its slimming: that chunk
// keeps its fields, every other one is still slimmed, and no event goes missing.
func TestSlimResponsePayloadKeepsSlimmingPastAChunkItCannotSlim(t *testing.T) {
	t.Parallel()
	good := `{\"choices\":[{\"logprobs\":{\"content\":[{\"token\":\"7\",\"logprob\":-0.5,\"bytes\":[55],` +
		`\"top_logprobs\":[{\"token\":\"7\",\"logprob\":-0.5,\"bytes\":[55]}]}]}}]}`
	// No alternative explains this position's logprob, so the compressor refuses this chunk alone.
	bad := `{\"choices\":[{\"logprobs\":{\"content\":[{\"token\":\"7\",\"logprob\":-0.5,\"bytes\":[55],` +
		`\"top_logprobs\":[{\"token\":\"9\",\"logprob\":-3.5,\"bytes\":[57]}]}]}}]}`
	envelope := `{"events":["data: ` + bad + `","data: ` + good + `","data: [DONE]"]}`

	compressed, err := CompressResponsePayload([]byte(envelope))
	if err != nil {
		t.Fatalf("one unslimmable chunk must not fail the envelope: %v", err)
	}
	var restored SerializedStreamedResponse
	if err := json.Unmarshal(compressed, &restored); err != nil {
		t.Fatalf("the compressed payload is not an envelope: %v", err)
	}
	if len(restored.Events) != 3 {
		t.Fatalf("the walk lost an event: %v", restored.Events)
	}
	if !strings.Contains(restored.Events[0], `"bytes"`) {
		t.Fatalf("the chunk that could not slim was slimmed anyway: %s", restored.Events[0])
	}
	if strings.Contains(restored.Events[1], `"bytes"`) {
		t.Fatalf("a slimmable chunk kept its fields because a neighbour refused: %s", restored.Events[1])
	}
	if restored.Events[2] != "data: [DONE]" {
		t.Fatalf("the terminator was rewritten: %q", restored.Events[2])
	}
}

// A backfill runs over a store holding both fat and already-slimmed payloads, so slimming twice has
// to be a no-op rather than an error on its own output.
func TestSlimResponsePayloadCanRunTwice(t *testing.T) {
	t.Parallel()
	payload := `{"id":"x","choices":[{"index":0,"message":{"content":"Hi"},"logprobs":{"content":[{"token":"7","logprob":-0.5,"bytes":[55],"top_logprobs":[{"token":"7","logprob":-0.5,"bytes":[55]}]}]}}]}`
	once, err := CompressResponsePayload([]byte(payload))
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	twice, err := CompressResponsePayload(once)
	if err != nil {
		t.Fatalf("a second pass over already-slim bytes must be a no-op: %v", err)
	}
	if string(twice) != string(once) {
		t.Fatalf("the second pass changed the payload:\n once=%s\ntwice=%s", once, twice)
	}
}

func TestStreamedLineBodyOnlyClaimsRealDataLines(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]string{
		"terminator":      "data: [DONE]",
		"blank":           "",
		"empty data":      "data: ",
		"comment":         ": keep-alive",
		"named event":     "event: ping",
		"not a data line": "id: 7",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, isData := streamedLineBody(line); isData {
				t.Fatalf("%q was taken for a data line", line)
			}
			slimmed, err := slimStreamedLine(line)
			if err != nil {
				t.Fatalf("slimStreamedLine(%q): %v", line, err)
			}
			if slimmed != line {
				t.Fatalf("%q was rewritten as %q", line, slimmed)
			}
		})
	}
}

// A host's malformed chunk must reach the gateway as malformed, not as empty or rewritten.
func TestSlimStreamedLineForwardsWhatItCannotParse(t *testing.T) {
	t.Parallel()
	for _, line := range []string{`data: {"choices":`, "data: not json at all", "data: [1,2,3]"} {
		slimmed, err := slimStreamedLine(line)
		if err != nil {
			t.Fatalf("slimStreamedLine(%q): %v", line, err)
		}
		if slimmed != line {
			t.Fatalf("%q was rewritten as %q", line, slimmed)
		}
	}
}
