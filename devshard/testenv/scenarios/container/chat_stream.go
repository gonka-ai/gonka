package container

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func postChatCompletionStream(t *testing.T, ctx context.Context, c *http.Client, inferIdx int) error {
	t.Helper()
	body := []byte(`{"model":"llama","stream":true,"max_tokens":50}`)
	return postChatCompletionStreamWithBody(t, ctx, c, inferIdx, body)
}

func postChatCompletionStreamWithBody(t *testing.T, ctx context.Context, c *http.Client, inferIdx int, body []byte) error {
	t.Helper()
	started := time.Now()
	t.Logf("inference %d: POST devshardctl chat/completions (host=%s)", inferIdx, hostServiceForNonce(uint64(inferIdx)))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8081/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Logf("inference %d: POST devshardctl: %v (after %s)", inferIdx, err, time.Since(started).Round(time.Millisecond))
		maybeAuditSlowInference(t, inferIdx, started, time.Since(started), lokiHTTPClient())
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		msg := strings.TrimSpace(truncateForLog(buf.String(), 800))
		elapsed := time.Since(started)
		t.Logf("inference %d: non-OK %d after %s body=%q", inferIdx, resp.StatusCode, elapsed.Round(time.Millisecond), msg)
		maybeAuditSlowInference(t, inferIdx, started, elapsed, lokiHTTPClient())
		return fmt.Errorf("http %d: %s", resp.StatusCode, buf.String())
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	const tailN = 12
	tail := make([]string, 0, tailN)
	var sawReceipt bool
	for sc.Scan() {
		line := sc.Text()
		if len(tail) >= tailN {
			tail = tail[1:]
		}
		tail = append(tail, truncateForLog(line, 400))
		if strings.Contains(line, "devshard_receipt") {
			sawReceipt = true
		}
		if sseInferenceFailed(line) {
			elapsed := time.Since(started)
			t.Logf("inference %d: SSE error after %s line=%q", inferIdx, elapsed.Round(time.Millisecond), truncateForLog(line, 400))
			maybeAuditSlowInference(t, inferIdx, started, elapsed, lokiHTTPClient())
			return fmt.Errorf("inference %d: %s", inferIdx, line)
		}
		if strings.Contains(line, "[DONE]") {
			elapsed := time.Since(started)
			t.Logf("inference %d: OK after %s (saw_receipt=%v)", inferIdx, elapsed.Round(time.Millisecond), sawReceipt)
			if elapsed >= slowInferenceAuditThreshold {
				maybeAuditSlowInference(t, inferIdx, started, elapsed, lokiHTTPClient())
			}
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		elapsed := time.Since(started)
		t.Logf("inference %d: SSE scanner error after %s: %v; last lines=%q", inferIdx, elapsed.Round(time.Millisecond), err, tail)
		maybeAuditSlowInference(t, inferIdx, started, elapsed, lokiHTTPClient())
		return fmt.Errorf("read SSE inference %d: %w", inferIdx, err)
	}
	elapsed := time.Since(started)
	t.Logf("inference %d: EOF without [DONE] after %s; last SSE lines=%q", inferIdx, elapsed.Round(time.Millisecond), tail)
	maybeAuditSlowInference(t, inferIdx, started, elapsed, lokiHTTPClient())
	return fmt.Errorf("inference %d: stream ended without [DONE]", inferIdx)
}

func lokiHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}

func sseInferenceFailed(line string) bool {
	return strings.Contains(line, `"error"`) && strings.Contains(line, `"message"`)
}

func truncateForLog(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
