package inference

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"common/completionapi"
	"common/logging"
	"github.com/productscience/inference/x/inference/types"
)

const (
	defaultScannerBufferSize = 64 * 1024   // 64KB initial scanner buffer
	maxScannerBufferSize     = 1024 * 1024 // 1MB max line size for SSE chunks

	mlNodeHTTPTimeout = 5 * time.Minute
)

// NewNoRedirectClient returns an HTTP client that does not follow redirects.
func NewNoRedirectClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func proxyResponse(
	resp *http.Response,
	w http.ResponseWriter,
	excludeContentLength bool,
	responseProcessor completionapi.ResponseProcessor,
	inferenceID string,
) {
	for key, values := range resp.Header {
		if excludeContentLength && key == "Content-Length" {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if completionapi.IsEventStream(resp) {
		logging.Debug("Proxying text/event-stream response", types.Inferences, "status_code", resp.StatusCode, "content_type", contentType, "inference_id", inferenceID)
		proxyTextStreamResponse(resp, w, responseProcessor, inferenceID)
	} else {
		logging.Debug("Proxying JSON response", types.Inferences, "status_code", resp.StatusCode, "content_type", contentType, "inference_id", inferenceID)
		proxyJSONResponse(resp, w, responseProcessor, inferenceID)
	}
}

func proxyTextStreamResponse(resp *http.Response, w http.ResponseWriter, responseProcessor completionapi.ResponseProcessor, inferenceID string) {
	w.WriteHeader(resp.StatusCode)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, defaultScannerBufferSize), maxScannerBufferSize)
	for scanner.Scan() {
		line := scanner.Text()

		logging.Debug("Chunk", types.Inferences, "inferenceID", inferenceID, "line", line)

		lineToProxy := line
		if responseProcessor != nil && line != "" {
			var err error
			lineToProxy, err = responseProcessor.ProcessStreamedResponse(line)
			if err != nil {
				logging.Error("Failed to process streamed response line", types.Inferences,
					"inferenceID", inferenceID, "error", err, "line", line,
				)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		logging.Debug("Chunk to proxy", types.Inferences, "inference_id", inferenceID, "line", lineToProxy)

		_, err := fmt.Fprintln(w, lineToProxy)
		if err != nil {
			opErr := &net.OpError{}
			if errors.As(err, &opErr) {
				logging.Warn("Stream cancelled during streaming", types.Inferences, "inferenceID", inferenceID, "error", opErr)
				resp.Body.Close()
				return
			}
			logging.Error("Error while streaming response", types.Inferences, "inferenceID", inferenceID, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		logging.Error("Error after streaming response", types.Inferences, "inferenceID", inferenceID, "error", err)
	}
}

func proxyJSONResponse(resp *http.Response, w http.ResponseWriter, responseProcessor completionapi.ResponseProcessor, inferenceID string) {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logging.Error("Failed to read inference node response body", types.Inferences, "inferenceID", inferenceID, "error", err)
		http.Error(w, fmt.Sprintf("Failed to read inference node response body. inferenceID = %s", inferenceID), http.StatusInternalServerError)
		return
	}

	if responseProcessor != nil {
		bodyBytes, err = responseProcessor.ProcessJsonResponse(bodyBytes)
		if err != nil {
			logging.Error("Failed to process inference node response", types.Inferences, "inferenceID", inferenceID, "error", err)
			http.Error(w, fmt.Sprintf("Failed to process inference node response. inferenceID = %s", inferenceID), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(bodyBytes)
}
