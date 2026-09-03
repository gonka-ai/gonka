package completionapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeDocumentWithoutUnreadFieldsMatchesTreeWalk(t *testing.T) {
	pastTheGate := bodyPastTheSizeGate(t)
	for name, body := range map[string]string{
		"past the size gate":  pastTheGate,
		"prompt token ids":    `{"id":"c","prompt_token_ids":[1,2,3],"choices":[]}`,
		"prompt logprobs":     `{"prompt_logprobs":[{"1":{"logprob":-0.5}},null],"id":"c"}`,
		"nested token ids":    `{"choices":[{"index":0,"token_ids":[163586],"delta":{"content":"hi"}}]}`,
		"no unread fields":    `{"id":"c","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		"empty array":         `{"prompt_token_ids":[],"id":"c"}`,
		"already null":        `{"prompt_token_ids":null,"id":"c"}`,
		"only key in object":  `{"prompt_token_ids":[1,2]}`,
		"key as string value": `{"id":"prompt_token_ids","choices":[]}`,
		"top level array":     `[{"prompt_token_ids":[1]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			want, err := decodeJSONDocument([]byte(body))
			require.NoError(t, err)
			dropFields(want, fieldsNoValidatorReads)

			got, err := decodeDocumentWithoutUnreadFields([]byte(body))
			require.NoError(t, err)
			dropFields(got, fieldsNoValidatorReads)

			require.Equal(t, want, got)
		})
	}
}

// Junk stays junk: the chain treats an unparseable chunk as a miss.
func TestDecodeDocumentWithoutUnreadFieldsAcceptsWhatThePlainDecodeAccepts(t *testing.T) {
	pastTheGate := bodyPastTheSizeGate(t)
	for name, body := range map[string]string{
		"truncated past the size gate": pastTheGate[:len(pastTheGate)-1],
		"junk appended past the gate":  pastTheGate + " junk",
		"unclosed array":               `{"prompt_token_ids":[1,2}`,
		"unclosed object":              `{"id":"c"`,
		"not json":                     `nonsense`,
		"empty":                        ``,
		"truncated mid array":          `{"prompt_token_ids":[1,`,
	} {
		t.Run(name, func(t *testing.T) {
			_, wantErr := decodeJSONDocument([]byte(body))

			_, err := decodeDocumentWithoutUnreadFields([]byte(body))

			require.Equal(t, wantErr != nil, err != nil, "acceptance changed for %q: was %v, now %v", body, wantErr, err)
		})
	}
}

// The stored payload is hashed, so not one byte of it may move.
func TestStoredEnvelopeMatchesTheBackfillPath(t *testing.T) {
	const inferenceID = "devshard-1-1"
	chunks := []string{
		`data: {"id":"` + inferenceID + `","object":"chat.completion.chunk","created":1,"model":"m",` +
			`"prompt_token_ids":[1,2,3],"choices":[{"index":0,"delta":{"role":"assistant"},"logprobs":null}]}`,
		`data: {"id":"` + inferenceID + `","object":"chat.completion.chunk","created":1,"model":"m",` +
			`"choices":[{"index":0,"delta":{"content":" word"},"token_ids":[7],"logprobs":{"content":[{"token":" word",` +
			`"logprob":-0.5,"bytes":[32,119,111,114,100],"top_logprobs":[{"token":" word","logprob":-0.5,` +
			`"bytes":[32,119,111,114,100]}]}]}}]}`,
		`data: {"id":"` + inferenceID + `","object":"chat.completion.chunk","created":1,"model":"m","choices":[],` +
			`"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		"data: [DONE]",
	}

	processor := NewExecutorResponseProcessor(inferenceID, true)
	for _, chunk := range chunks {
		_, err := processor.ProcessStreamedResponse(chunk)
		require.NoError(t, err)
	}
	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)

	asArrived, err := json.Marshal(SerializedStreamedResponse{Events: chunks})
	require.NoError(t, err)
	want, err := CompressResponsePayload(asArrived)
	require.NoError(t, err)

	require.Equal(t, string(want), string(stored))
	require.NotContains(t, string(stored), "token_ids")
}

// Only a body past skipDecodeAboveBytes reaches the skipping branch, so equivalence is proven there.
func bodyPastTheSizeGate(t *testing.T) string {
	t.Helper()
	ids := strings.TrimPrefix(strings.Repeat(",163586", 1_000), ",")
	body := `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","prompt_token_ids":[` + ids +
		`],"choices":[{"index":0,"token_ids":[7],"delta":{"content":"hi"}}]}`
	require.Greater(t, len(body), skipDecodeAboveBytes, "fixture no longer passes the size gate")
	return body
}

// dropFields removes the keys anyway, so the skip only shows up as allocations.
func TestDecodeDocumentWithoutUnreadFieldsLeavesTheArrayUndecoded(t *testing.T) {
	body := []byte(bodyPastTheSizeGate(t))

	allocations := testing.AllocsPerRun(20, func() {
		if _, err := decodeDocumentWithoutUnreadFields(body); err != nil {
			t.Error(err)
		}
	})

	require.Less(t, allocations, float64(500), "the prompt's 1000 ids were decoded one by one")
}

// Each unread field on its own, in each place a host can put it, and then all of them at once.
func TestEveryUnreadFieldIsDroppedWhereverItSits(t *testing.T) {
	places := map[string]string{
		"at the top level": `{"id":"c","%s":[1,2],"choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
		"inside a choice":  `{"id":"c","choices":[{"index":0,"%s":[1,2],"delta":{"content":"Hi"}}]}`,
		"inside a delta":   `{"id":"c","choices":[{"index":0,"delta":{"content":"Hi","%s":[1,2]}}]}`,
	}
	for _, field := range fieldsNoValidatorReads {
		for place, shape := range places {
			t.Run(field+" "+place, func(t *testing.T) {
				requireStoredChunkKeepsOnlyTheAnswer(t, fmt.Sprintf(shape, field))
			})
		}
	}

	t.Run("all of them at once", func(t *testing.T) {
		requireStoredChunkKeepsOnlyTheAnswer(t, `{"id":"c","prompt_token_ids":[1,2],"prompt_logprobs":[{"1":{"logprob":-0.5}}],`+
			`"choices":[{"index":0,"token_ids":[3],"delta":{"content":"Hi","token_ids":[4]}}]}`)
	})
}

// requireStoredChunkKeepsOnlyTheAnswer stores one chunk and asserts no unread field survived it.
func requireStoredChunkKeepsOnlyTheAnswer(t *testing.T, chunk string) {
	t.Helper()
	processor := NewExecutorResponseProcessor("devshard-1-1", true)
	_, err := processor.ProcessStreamedResponse(DataPrefix + chunk)
	require.NoError(t, err)

	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)
	for _, field := range fieldsNoValidatorReads {
		require.NotContains(t, string(stored), `\"`+field+`\"`, "the stored payload kept %q", field)
	}
	require.Contains(t, string(stored), `\"content\":\"Hi\"`, "the answer did not survive")
}
