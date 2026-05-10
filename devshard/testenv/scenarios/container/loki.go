//go:build testenvci

package container

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const lokiBaseURL = "http://127.0.0.1:3100"

// LokiQueryRange hits GET /loki/api/v1/query_range and returns raw log lines (newline-separated payloads).
func LokiQueryRange(t *testing.T, c *http.Client, logQL string, start, end time.Time, limit int) []string {
	t.Helper()
	if limit <= 0 {
		limit = 1000
	}
	q := url.Values{}
	q.Set("query", logQL)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	u := lokiBaseURL + "/loki/api/v1/query_range?" + q.Encode()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatalf("loki query_range: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("loki read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("loki http %d: %s", resp.StatusCode, string(b))
	}
	var parsed struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Values [][]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("loki json: %v", err)
	}
	if parsed.Status != "success" {
		t.Fatalf("loki status=%q body=%s", parsed.Status, string(b))
	}
	var lines []string
	for _, stream := range parsed.Data.Result {
		for _, pair := range stream.Values {
			if len(pair) >= 2 {
				lines = append(lines, pair[1])
			}
		}
	}
	return lines
}

// ParseLogKV extracts key=value pairs from a slog-style JSON log line (flat object).
func ParseLogKV(line string) map[string]string {
	line = strings.TrimSpace(line)
	out := make(map[string]string)
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return out
	}
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// CountHeightSyncEmitModes counts heightsync: emit lines with direction=request and given mode.
func CountHeightSyncEmitModes(lines []string, direction, mode string) int {
	n := 0
	for _, ln := range lines {
		kv := ParseLogKV(ln)
		if kv["msg"] != "heightsync: emit" {
			continue
		}
		if kv["direction"] != direction {
			continue
		}
		if strings.EqualFold(kv["mode"], mode) {
			n++
		}
	}
	return n
}
