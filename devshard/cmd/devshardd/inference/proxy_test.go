package inference

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"common/completionapi"
	devshardpkg "devshard"
	"devshard/observability"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type failAfterWriter struct {
	header     http.Header
	buf        bytes.Buffer
	failAfter  int
	writes     int
	statusCode int
}

func (w *failAfterWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.failAfter >= 0 && w.writes > w.failAfter {
		return 0, &net.OpError{Op: "write", Err: errors.New("broken pipe")}
	}
	return w.buf.Write(p)
}

func (w *failAfterWriter) WriteHeader(statusCode int) { w.statusCode = statusCode }

func (w *failAfterWriter) Flush() {}

func sseBody(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func TestProxyTextStreamResponse_WriteFailureContinuesDrain(t *testing.T) {
	beforeDetach := testutil.ToFloat64(observability.InferenceClientDetachedDrainCounterForTest())
	beforeCompleted := testutil.ToFloat64(observability.InferenceDrainOutcomeCounterForTest(observability.DrainOutcomeCompleted))

	body := sseBody(
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":2}}`,
		`data: [DONE]`,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	w := &failAfterWriter{failAfter: 1} // first Fprintln ok, second fails
	processor := completionapi.NewExecutorResponseProcessor("inf-1")

	outcome := proxyTextStreamResponse(resp, w, processor, "inf-1")
	require.True(t, outcome.clientDetached)
	require.True(t, outcome.sawDone)
	require.NoError(t, outcome.err)

	completion, err := processor.GetResponse()
	require.NoError(t, err)
	usage, err := completion.GetUsage()
	require.NoError(t, err)
	require.Equal(t, uint64(9), usage.PromptTokens)
	require.Equal(t, uint64(2), usage.CompletionTokens)

	require.Equal(t, beforeDetach+1, testutil.ToFloat64(observability.InferenceClientDetachedDrainCounterForTest()))
	require.Equal(t, beforeCompleted+1, testutil.ToFloat64(observability.InferenceDrainOutcomeCounterForTest(observability.DrainOutcomeCompleted)))
}

// hubDetachWriter models LiveStream: Write always succeeds, detach is async via ClientDetached.
type hubDetachWriter struct {
	header   http.Header
	buf      bytes.Buffer
	writes   int
	detached bool
}

func (w *hubDetachWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *hubDetachWriter) WriteHeader(int) {}
func (w *hubDetachWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes >= 2 {
		w.detached = true
	}
	return w.buf.Write(p)
}
func (w *hubDetachWriter) Flush()                      {}
func (w *hubDetachWriter) ClientDetached() bool        { return w.detached }

func TestProxyTextStreamResponse_ClientDetachedKeepsBuffering(t *testing.T) {
	beforeDetach := testutil.ToFloat64(observability.InferenceClientDetachedDrainCounterForTest())

	body := sseBody(
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":2}}`,
		`data: [DONE]`,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	w := &hubDetachWriter{}
	processor := completionapi.NewExecutorResponseProcessor("inf-hub")

	outcome := proxyTextStreamResponse(resp, w, processor, "inf-hub")
	require.True(t, outcome.clientDetached)
	require.True(t, outcome.sawDone)
	require.NoError(t, outcome.err)
	require.Equal(t, 4, w.writes, "must keep writing into the hub after detach")
	require.Equal(t, beforeDetach+1, testutil.ToFloat64(observability.InferenceClientDetachedDrainCounterForTest()))
}

func TestProxyTextStreamResponse_DrainDeadlineCounted(t *testing.T) {
	beforeDeadline := testutil.ToFloat64(observability.InferenceDrainOutcomeCounterForTest(observability.DrainOutcomeDeadline))

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       pr,
	}
	w := &failAfterWriter{failAfter: 0} // first write fails → detach immediately
	processor := completionapi.NewExecutorResponseProcessor("inf-deadline")

	done := make(chan streamProxyOutcome, 1)
	go func() {
		done <- proxyTextStreamResponse(resp, w, processor, "inf-deadline")
	}()

	// Deliver one line so the writer fails and detach is recorded, then block.
	_, err := io.WriteString(pw, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`+"\n")
	require.NoError(t, err)

	// Close the pipe with a deadline-shaped error by canceling via CloseWithError.
	time.Sleep(20 * time.Millisecond)
	_ = pw.CloseWithError(context.DeadlineExceeded)

	outcome := <-done
	require.True(t, outcome.clientDetached)
	require.False(t, outcome.sawDone)
	require.ErrorIs(t, outcome.err, context.DeadlineExceeded)
	require.Equal(t, beforeDeadline+1, testutil.ToFloat64(observability.InferenceDrainOutcomeCounterForTest(observability.DrainOutcomeDeadline)))
}

func TestExecutionContextSurvivesParentCancel(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	drainCtx, cancelDrain := executionContext(parent)
	defer cancelDrain()

	cancelParent()
	require.ErrorIs(t, parent.Err(), context.Canceled)
	require.NoError(t, drainCtx.Err(), "drain context must not inherit request cancellation")

	select {
	case <-drainCtx.Done():
		t.Fatal("drain context ended immediately after parent cancel")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestExecuteInference_ClientCancelDoesNotAbortML(t *testing.T) {
	var mlCtxCanceled atomic.Bool
	chunks := []string{
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"B"},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":2}}`,
		`data: [DONE]`,
	}

	mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i, chunk := range chunks {
			select {
			case <-r.Context().Done():
				mlCtxCanceled.Store(true)
				return
			default:
			}
			_, _ = fmt.Fprintln(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}
			if i == 0 {
				time.Sleep(30 * time.Millisecond)
			}
		}
	}))
	t.Cleanup(mlSrv.Close)

	reqCtx, cancelReq := context.WithCancel(context.Background())
	writer := &failAfterWriter{failAfter: 1}

	go func() {
		time.Sleep(15 * time.Millisecond)
		cancelReq()
	}()

	store := &memPayloadStore{}
	result, err := executeInference(
		reqCtx,
		devshardExecuteRequest(writer),
		store,
		1,
		func(ctx context.Context, model string, body []byte) (*http.Response, error) {
			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, mlSrv.URL+"/v1/chat/completions", bytes.NewReader(body))
			require.NoError(t, err)
			httpReq.Header.Set("Content-Type", "application/json")
			return http.DefaultClient.Do(httpReq)
		},
		fixedLogprobsMode{},
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, uint64(11), result.InputTokens)
	require.Equal(t, uint64(2), result.OutputTokens)
	require.NotEmpty(t, result.ResponseBody)
	require.False(t, result.PartialResponse)
	require.False(t, mlCtxCanceled.Load(), "ML request context must outlive client cancel")
	require.NotEmpty(t, store.response)
}

func TestProcessExecutionHTTPResponse_PartialWithoutDone(t *testing.T) {
	body := sseBody(
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
		// no [DONE]
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	w := &failAfterWriter{failAfter: -1} // never fail

	processed, err := processExecutionHTTPResponse(devshardExecuteRequest(w), resp, "inf-partial")
	require.NoError(t, err)
	require.True(t, processed.partialResponse)
	require.Equal(t, string(observability.ReasonPartialResponseInterrupted), processed.partialResponseReason)
	require.Equal(t, string(observability.WhereRuntimeDrainML), processed.partialResponseWhere)
	require.Equal(t, uint64(5), processed.inputTokens)
}

// --- test helpers ---

type memPayloadStore struct {
	response []byte
}

func (m *memPayloadStore) Store(ctx context.Context, escrowId string, inferenceId, epochId uint64, promptPayload, responsePayload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.response = append([]byte(nil), responsePayload...)
	return nil
}

type fixedLogprobsMode struct{}

func (fixedLogprobsMode) LogprobsMode() string { return "" }

func devshardExecuteRequest(w http.ResponseWriter) devshardpkg.ExecuteRequest {
	return devshardpkg.ExecuteRequest{
		InferenceID:    7,
		Model:          "m",
		Prompt:         []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		EscrowID:       "escrow",
		ResponseWriter: w,
	}
}
