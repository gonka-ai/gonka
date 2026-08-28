package inference

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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
	inferenceId string,
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
	if strings.HasPrefix(contentType, "text/event-stream") {
		logging.Debug("Proxying text/event-stream response", types.Inferences, "status_code", resp.StatusCode, "content_type", contentType, "inference_id", inferenceId)
		proxyTextStreamResponse(resp, w, responseProcessor, inferenceId)
	} else {
		logging.Debug("Proxying JSON response", types.Inferences, "status_code", resp.StatusCode, "content_type", contentType, "inference_id", inferenceId)
		proxyJSONResponse(resp, w, responseProcessor, inferenceId)
	}
}

func proxyTextStreamResponse(resp *http.Response, w http.ResponseWriter, responseProcessor completionapi.ResponseProcessor, inferenceId string) {
	w.WriteHeader(resp.StatusCode)

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
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		logging.Debug("Chunk to proxy", types.Inferences, "inference_id", inferenceId, "line", lineToProxy)

		_, err := fmt.Fprintln(w, lineToProxy)
		if err != nil {
			if opErr, ok := err.(*net.OpError); ok {
				logging.Warn("Stream cancelled during streaming", types.Inferences, "inferenceId", inferenceId, "error", opErr)
				resp.Body.Close()
				return
			}
			logging.Error("Error while streaming response", types.Inferences, "inferenceId", inferenceId, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		logging.Error("Error after streaming response", types.Inferences, "inferenceId", inferenceId, "error", err)
	}
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
