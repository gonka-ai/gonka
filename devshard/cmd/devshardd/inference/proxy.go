package inference

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"common/completionapi"
	"common/logging"
	"devshard/observability"

	"github.com/productscience/inference/x/inference/types"
)

const (
	defaultScannerBufferSize = 64 * 1024   // 64KB initial scanner buffer
	maxScannerBufferSize     = 1024 * 1024 // 1MB max line size for SSE chunks

	mlNodeHTTPTimeout = 5 * time.Minute
)

// executionDrainTimeout bounds ML generation + body drain after the gateway
// HTTP request context is detached. Overridable in tests.
var executionDrainTimeout = mlNodeHTTPTimeout

// NewNoRedirectClient returns an HTTP client that does not follow redirects.
func NewNoRedirectClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// executionContext returns a context that survives client/request cancellation
// but is still bounded by executionDrainTimeout so a hung ML node cannot pin
// the host forever. Values from parent are preserved for observability.
func executionContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), executionDrainTimeout)
}

// streamProxyOutcome summarizes an SSE proxy/drain pass.
type streamProxyOutcome struct {
	clientDetached bool
	sawDone        bool
	err            error
}

func proxyResponse(
	resp *http.Response,
	w http.ResponseWriter,
	excludeContentLength bool,
	responseProcessor completionapi.ResponseProcessor,
	inferenceId string,
) streamProxyOutcome {
	for key, values := range resp.Header {
		if excludeContentLength && key == "Content-Length" {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		logging.Debug("Proxying text/event-stream response", types.Inferences, "status_code", resp.StatusCode, "content_type", contentType, "inference_id", inferenceId)
		return proxyTextStreamResponse(resp, w, responseProcessor, inferenceId)
	}
	logging.Debug("Proxying JSON response", types.Inferences, "status_code", resp.StatusCode, "content_type", contentType, "inference_id", inferenceId)
	proxyJSONResponse(resp, w, responseProcessor, inferenceId)
	// Non-SSE upstream is a single complete body.
	return streamProxyOutcome{sawDone: true}
}

func proxyTextStreamResponse(resp *http.Response, w http.ResponseWriter, responseProcessor completionapi.ResponseProcessor, inferenceId string) streamProxyOutcome {
	outcome := streamProxyOutcome{}
	w.WriteHeader(resp.StatusCode)

	writerAlive := true
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, defaultScannerBufferSize), maxScannerBufferSize)
	for scanner.Scan() {
		line := scanner.Text()

		logging.Debug("Chunk", types.Inferences, "inferenceId", inferenceId, "line", line)

		lineToProxy := line
		if responseProcessor != nil && line != "" {
			var err error
			lineToProxy, err = responseProcessor.ProcessStreamedResponse(line)
			if err != nil {
				logging.Error("Failed to process streamed response line", types.Inferences,
					"inferenceId", inferenceId, "error", err, "line", line,
				)
				// Keep draining ML so the executor can still accumulate a body.
				if isSSEDoneLine(line) {
					outcome.sawDone = true
				}
				continue
			}
		}
		if isSSEDoneLine(line) || isSSEDoneLine(lineToProxy) {
			outcome.sawDone = true
		}

		if !writerAlive {
			continue
		}

		logging.Debug("Chunk to proxy", types.Inferences, "inference_id", inferenceId, "line", lineToProxy)

		_, err := fmt.Fprintln(w, lineToProxy)
		if err != nil {
			logging.Warn("Client writer failed; continuing ML drain", types.Inferences,
				"inferenceId", inferenceId, "error", err)
			writerAlive = false
			outcome.clientDetached = true
			observability.IncInferenceClientDetachedDrain()
			continue
		}
		// LiveStream.Write always succeeds (append-only hub). Primary detach is
		// reported via ClientDetached(); keep writing into the hub so resume /
		// finish retain the full body — only count the metric once.
		if d, ok := w.(interface{ ClientDetached() bool }); ok && d.ClientDetached() {
			if !outcome.clientDetached {
				logging.Warn("Client writer detached; continuing ML drain into live buffer", types.Inferences,
					"inferenceId", inferenceId)
				outcome.clientDetached = true
				observability.IncInferenceClientDetachedDrain()
			}
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		outcome.err = err
		logging.Error("Error after streaming response", types.Inferences, "inferenceId", inferenceId, "error", err)
	}
	recordDrainOutcome(outcome)
	return outcome
}

func recordDrainOutcome(outcome streamProxyOutcome) {
	if !outcome.clientDetached {
		return
	}
	switch {
	case outcome.err != nil && isDrainDeadlineErr(outcome.err):
		observability.IncInferenceDrainOutcome(observability.DrainOutcomeDeadline)
	case outcome.err != nil:
		observability.IncInferenceDrainOutcome(observability.DrainOutcomeMLError)
	case !outcome.sawDone:
		observability.IncInferenceDrainOutcome(observability.DrainOutcomeMLError)
	default:
		observability.IncInferenceDrainOutcome(observability.DrainOutcomeCompleted)
	}
}

func isDrainDeadlineErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func isSSEDoneLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, completionapi.DataPrefix) {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, completionapi.DataPrefix))
	}
	return trimmed == "[DONE]"
}

func proxyJSONResponse(resp *http.Response, w http.ResponseWriter, responseProcessor completionapi.ResponseProcessor, inferenceId string) {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logging.Error("Failed to read inference node response body", types.Inferences, "inferenceId", inferenceId, "error", err)
		http.Error(w, fmt.Sprintf("Failed to read inference node response body. inferenceId = %s", inferenceId), http.StatusInternalServerError)
		return
	}

	if responseProcessor != nil {
		bodyBytes, err = responseProcessor.ProcessJsonResponse(bodyBytes)
		if err != nil {
			logging.Error("Failed to process inference node response", types.Inferences, "inferenceId", inferenceId, "error", err)
			http.Error(w, fmt.Sprintf("Failed to process inference node response. inferenceId = %s", inferenceId), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(bodyBytes)
}
