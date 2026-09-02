package completionapi

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const MaxResponseBytes = 50 << 20

var ErrResponseTooLarge = fmt.Errorf("response exceeds %d byte limit", MaxResponseBytes)

type cappedReader struct {
	r         io.Reader
	remaining int64
}

func NewCappedResponseReader(body io.Reader) io.Reader {
	return &cappedReader{r: body, remaining: MaxResponseBytes + 1}
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, ErrResponseTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	return n, err
}

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
	scanner := bufio.NewScanner(NewCappedResponseReader(body))
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
	data, err := io.ReadAll(NewCappedResponseReader(body))
	if err != nil {
		return err
	}
	_, err = processor.ProcessJsonResponse(data)
	return err
}
