package completionapi

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxJSONResponseBytes = 50 << 20

// ProcessHTTPResponse reads an HTTP response body, detects SSE vs JSON from Content-Type,
// and feeds the data through the given ResponseProcessor.
// For SSE: uses bufio.Scanner line-by-line on non-empty lines.
// For JSON: reads full body via io.ReadAll.
func ProcessHTTPResponse(resp *http.Response, processor ResponseProcessor) error {
	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		return processSSE(resp.Body, processor)
	}
	return processJSON(resp.Body, processor)
}

func processSSE(body io.Reader, processor ResponseProcessor) error {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if _, err := processor.ProcessStreamedResponse(line); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func processJSON(body io.Reader, processor ResponseProcessor) error {
	data, err := io.ReadAll(io.LimitReader(body, maxJSONResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxJSONResponseBytes {
		return fmt.Errorf("response exceeds %d byte limit", maxJSONResponseBytes)
	}
	_, err = processor.ProcessJsonResponse(data)
	return err
}
