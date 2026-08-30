package payloads

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"common/completionapi"
	"common/utils"
	"common/validation"
)

const (
	testEscrowID = "60453"
	testEpoch    = 11
)

// writeRaw puts bytes on disk under a chosen suffix, which is how a directory left by another
// version, or by a write that failed halfway, actually looks.
func writeRaw(t *testing.T, baseDir string, inferenceID uint64, suffix string, body []byte) {
	t.Helper()
	dir := filepath.Join(baseDir, "11", testEscrowID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "7"+suffix), body, 0o644))
}

func marshalStored(t *testing.T, prompt, response []byte) []byte {
	t.Helper()
	body, err := json.Marshal(storedPayload{PromptPayload: prompt, ResponsePayload: response})
	require.NoError(t, err)
	return body
}

// The suffix decides how a file is read, so every state a payload directory can be in has to resolve
// to the payload that was written or to a refusal -- never to another payload, and never to garbage.
func TestReadingResolvesEverySuffixState(t *testing.T) {
	prompt, response := []byte(`{"messages":[]}`), []byte(`{"choices":[]}`)

	for name, arrange := range map[string]func(*testing.T, string){
		"plain only": func(t *testing.T, baseDir string) {
			require.NoError(t, NewFileStorage(baseDir).Store(context.Background(), testEscrowID, 7, testEpoch, prompt, response))
		},
		"compressed only": func(t *testing.T, baseDir string) {
			require.NoError(t, NewCompressingFileStorage(baseDir).Store(context.Background(), testEscrowID, 7, testEpoch, prompt, response))
		},
		"both suffixes, same payload": func(t *testing.T, baseDir string) {
			stored := marshalStored(t, prompt, response)
			compressed, err := compressPayloadFile(stored)
			require.NoError(t, err)
			writeRaw(t, baseDir, 7, plainSuffix, stored)
			writeRaw(t, baseDir, 7, compressedSuffix, compressed)
		},
	} {
		t.Run(name, func(t *testing.T) {
			baseDir := t.TempDir()
			arrange(t, baseDir)

			// The gate governs writing alone, so a reader in either mode must resolve the same bytes.
			for readerName, reader := range map[string]*FileStorage{
				"reader with the gate off": NewFileStorage(baseDir),
				"reader with the gate on":  NewCompressingFileStorage(baseDir),
			} {
				gotPrompt, gotResponse, err := reader.Retrieve(context.Background(), testEscrowID, 7, testEpoch)
				require.NoErrorf(t, err, "%s could not read a payload written as %q", readerName, name)
				require.Equalf(t, string(prompt), string(gotPrompt), "%s got a different prompt", readerName)
				require.Equalf(t, string(response), string(gotResponse), "%s got a different response", readerName)
			}
		})
	}
}

func TestReadingRefusesWhatItCannotResolve(t *testing.T) {
	t.Run("nothing on disk", func(t *testing.T) {
		_, _, err := NewFileStorage(t.TempDir()).Retrieve(context.Background(), testEscrowID, 7, testEpoch)
		require.ErrorIs(t, err, ErrNotFound)
	})

	// What a store that named an uncompressed body after the compressed format leaves behind. It must
	// not read as an absent payload either: the file is there and the operator has to hear about it.
	t.Run("plain bytes under the compressed name", func(t *testing.T) {
		baseDir := t.TempDir()
		writeRaw(t, baseDir, 7, compressedSuffix, marshalStored(t, []byte(`{"a":1}`), []byte(`{"b":2}`)))

		_, _, err := NewFileStorage(baseDir).Retrieve(context.Background(), testEscrowID, 7, testEpoch)
		require.Error(t, err, "a body that is not zstd must not pass as one")
		require.NotErrorIs(t, err, ErrNotFound, "a file that exists must not report as missing")
	})

	t.Run("compressed bytes under the plain name", func(t *testing.T) {
		baseDir := t.TempDir()
		compressed, err := compressPayloadFile(marshalStored(t, []byte(`{"a":1}`), []byte(`{"b":2}`)))
		require.NoError(t, err)
		writeRaw(t, baseDir, 7, plainSuffix, compressed)

		_, _, err = NewFileStorage(baseDir).Retrieve(context.Background(), testEscrowID, 7, testEpoch)
		require.Error(t, err, "zstd bytes must not pass as JSON")
	})
}

// executorStores reproduces what the executor commits: it slims the host's answer chunk by chunk,
// assembles the payload, and hashes exactly the bytes it is about to store.
func executorStores(t *testing.T, lines []string) (stored []byte, committed [32]byte) {
	t.Helper()
	processor := completionapi.NewExecutorResponseProcessor("devshard-60453-7")
	for _, line := range lines {
		_, err := processor.ProcessStreamedResponse(line)
		require.NoError(t, err)
	}
	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)
	return stored, sha256.Sum256(stored)
}

func executorStoresJSON(t *testing.T, body []byte) (stored []byte, committed [32]byte) {
	t.Helper()
	processor := completionapi.NewExecutorResponseProcessor("devshard-60453-7")
	_, err := processor.ProcessJsonResponse(body)
	require.NoError(t, err)
	stored, err = processor.GetResponseBytes()
	require.NoError(t, err)
	return stored, sha256.Sum256(stored)
}

// A validator fetches the stored payload and re-derives the hash the chain committed. Compression is
// a storage detail, so it must not move that hash -- in either response shape, in either mode.
func TestAStoredPayloadKeepsItsHashThroughEveryStorageMode(t *testing.T) {
	chunk := `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","prompt_token_ids":[8,9],` +
		`"choices":[{"index":0,"token_ids":[758],"delta":{"content":"The"},` +
		`"logprobs":{"content":[{"token":"758","logprob":-0.31,"bytes":[84,104,101],` +
		`"top_logprobs":[{"token":"758","logprob":-0.31,"bytes":[55,53,56]}]}]},"finish_reason":null}]}`
	jsonBody := `{"id":"x","object":"chat.completion","created":1,"model":"m","prompt_token_ids":[8,9],` +
		`"choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":"The"},` +
		`"logprobs":{"content":[{"token":"758","logprob":-0.31,"bytes":[84,104,101],` +
		`"top_logprobs":[{"token":"758","logprob":-0.31,"bytes":[55,53,56]}]}]}}],` +
		`"usage":{"prompt_tokens":7,"completion_tokens":1}}`
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	shapes := map[string][]byte{}
	streamed, streamedHash := executorStores(t, []string{chunk, "data: [DONE]"})
	shapes["streamed envelope"] = streamed
	assembled, assembledHash := executorStoresJSON(t, []byte(jsonBody))
	shapes["plain completion"] = assembled
	hashes := map[string][32]byte{"streamed envelope": streamedHash, "plain completion": assembledHash}

	canonicalPrompt, err := utils.CanonicalizeJSON(prompt)
	require.NoError(t, err)
	expectedPromptHash := utils.GenerateSHA256Hash(canonicalPrompt)

	for shape, responsePayload := range shapes {
		for mode, open := range map[string]func(string) *FileStorage{
			"gate off": NewFileStorage,
			"gate on":  NewCompressingFileStorage,
		} {
			t.Run(shape+", "+mode, func(t *testing.T) {
				baseDir := t.TempDir()
				storage := open(baseDir)
				require.NoError(t, storage.Store(context.Background(), testEscrowID, 7, testEpoch, prompt, responsePayload))

				gotPrompt, gotResponse, err := storage.Retrieve(context.Background(), testEscrowID, 7, testEpoch)
				require.NoError(t, err)
				require.Equal(t, sha256.Sum256(responsePayload), sha256.Sum256(gotResponse),
					"storage changed the bytes the chain committed a hash over")
				require.Equal(t, hashes[shape], sha256.Sum256(gotResponse))

				parsed, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(gotResponse)
				require.NoError(t, err, "a validator must be able to parse what came back")
				responseHash, err := parsed.GetHash()
				require.NoError(t, err)

				require.NoError(t,
					validation.VerifyPayloadHashes(gotPrompt, gotResponse, expectedPromptHash, responseHash, "7"),
					"the validator's own hash check must pass on the retrieved payload")
			})
		}
	}
}

// The check has to be able to fail, or the test above proves nothing.
func TestTheHashCheckRejectsAPayloadThatChanged(t *testing.T) {
	baseDir := t.TempDir()
	storage := NewCompressingFileStorage(baseDir)
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	served, _ := executorStoresJSON(t, []byte(`{"id":"x","object":"chat.completion","created":1,"model":"m",`+
		`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"The"}}],`+
		`"usage":{"prompt_tokens":7,"completion_tokens":1}}`))
	require.NoError(t, storage.Store(context.Background(), testEscrowID, 7, testEpoch, prompt, served))

	tampered, _ := executorStoresJSON(t, []byte(`{"id":"x","object":"chat.completion","created":1,"model":"m",`+
		`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Other"}}],`+
		`"usage":{"prompt_tokens":7,"completion_tokens":1}}`))
	tamperedResponse, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(tampered)
	require.NoError(t, err)
	tamperedHash, err := tamperedResponse.GetHash()
	require.NoError(t, err)

	_, gotResponse, err := storage.Retrieve(context.Background(), testEscrowID, 7, testEpoch)
	require.NoError(t, err)
	require.True(t, errors.Is(
		validation.VerifyPayloadHashes(prompt, gotResponse, "", tamperedHash, "7"),
		validation.ErrHashMismatch), "a payload that is not the one hashed must be rejected")
}
