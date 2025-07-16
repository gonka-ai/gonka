package public

import (
	"bufio"
	"decentralized-api/completionapi"
	"decentralized-api/logging"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
	"io"
	"net"
	"net/http"
	"strings"
)

func proxyResponse(
	resp *http.Response,
	w http.ResponseWriter,
	excludeContentLength bool,
	responseProcessor completionapi.ResponseProcessor,
) {
	// Make sure to copy response headers to the client
	for key, values := range resp.Header {
		// Skip Content-Length, because we're modifying body
		if excludeContentLength && key == "Content-Length" {
			continue
		}

		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		logging.Debug("Proxying text/event-stream response", types.Inferences, "status_code", resp.StatusCode)
		proxyTextStreamResponse(resp, w, responseProcessor)
	} else {
		logging.Debug("Proxying JSON response", types.Inferences, "status_code", resp.StatusCode, "content_type", contentType)
		proxyJsonResponse(resp, w, responseProcessor)
	}
}

func proxyTextStreamResponse(resp *http.Response, w http.ResponseWriter, responseProcessor completionapi.ResponseProcessor) {
	w.WriteHeader(resp.StatusCode)

	// Stream the response from the completion server to the client
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// DEBUG LOG
		logging.Debug("Chunk", types.Inferences, "line", line)

		var lineToProxy = line
		if responseProcessor != nil {
			var err error
			lineToProxy, err = responseProcessor.ProcessStreamedResponse(line)
			if err != nil {
				logging.Error("Failed to process streamed response line", types.Inferences, "error", err, "line", line)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		logging.Debug("Chunk to proxy", types.Inferences, "line", lineToProxy)

		// Forward the line to the client
		_, err := fmt.Fprintln(w, lineToProxy)
		if err != nil {
			if opErr, ok := err.(*net.OpError); ok {
				logging.Warn("Stream cancelled during streaming", types.Inferences, "error", opErr)
				resp.Body.Close()
				return
			}

			logging.Error("Error while streaming response", types.Inferences, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := scanner.Err(); err != nil {
		logging.Error("Error after streaming response", types.Inferences, "error", err)
	}
}

func proxyJsonResponse(resp *http.Response, w http.ResponseWriter, responseProcessor completionapi.ResponseProcessor) {
	var bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "Failed to read inference node response body", http.StatusInternalServerError)
		return
	}

	if responseProcessor != nil {
		bodyBytes, err = responseProcessor.ProcessJsonResponse(bodyBytes)
		if err != nil {
			logging.Error("Failed to process inference node response", types.Inferences, "error", err)
			http.Error(w, "Failed to add ID to response", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(bodyBytes)
}
