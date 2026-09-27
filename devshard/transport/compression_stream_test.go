package transport

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/types"
)

// stubAnswerMarker is the content stub.InferenceEngine answers with.
const stubAnswerMarker = `"content":"stub"`

// requireSnapshotBetween asserts a flush carried written before pending.
func requireSnapshotBetween(t *testing.T, snapshots [][]byte, written, pending string) {
	t.Helper()
	for _, snapshot := range snapshots {
		decoded := testutil.GzipDecodeSoFar(t, snapshot)
		if strings.Contains(decoded, written) && !strings.Contains(decoded, pending) {
			return
		}
	}
	t.Fatalf("no flush carried %q before %q was written; the answer was held back", written, pending)
}

// Drives the real HandleInference and pins both flushes on the path.
func TestServer_CompressedStreamingInferenceStaysFrameByFrame(t *testing.T) {
	env := setupServerEnv(t)

	router := echo.New()
	router.HideBanner = true
	router.POST("/devshard/v2/sessions/:id/chat/completions", func(c echo.Context) error {
		return env.server.AuthMiddleware(env.server.RateLimitMiddleware(true)(env.server.HandleInference))(c)
	}, ResponseCompressionMiddleware)

	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	diffJSON, err := DiffToJSON(diff)
	require.NoError(t, err)
	body, err := json.Marshal(InferenceRequest{
		Diffs: []DiffJSON{diffJSON},
		Nonce: 1,
		Payload: &PayloadJSON{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		},
		Stream: true,
	})
	require.NoError(t, err)

	timestamp := time.Now().Unix()
	signature, err := SignRequest(env.userSigner, "escrow-1", body, timestamp)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost,
		"/devshard/v2/sessions/escrow-1/chat/completions", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(HeaderSignature, hex.EncodeToString(signature))
	request.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))
	request.Header.Set("Accept-Encoding", gzipEncoding)

	recorder := testutil.NewFlushRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code())
	require.Equal(t, gzipEncoding, recorder.Header().Get("Content-Encoding"))

	final := testutil.GzipDecodeSoFar(t, recorder.Body())
	require.Contains(t, final, "devshard_receipt")
	require.Contains(t, final, stubAnswerMarker)
	require.Contains(t, final, "[DONE]")

	requireSnapshotBetween(t, recorder.Flushes(), "devshard_receipt", stubAnswerMarker)
	requireSnapshotBetween(t, recorder.Flushes(), stubAnswerMarker, "[DONE]")
}
