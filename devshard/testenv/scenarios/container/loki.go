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
	q.Set("direction", "backward")
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

// LokiWindowStart returns a query_range start time for logs emitted after recent traffic.
func LokiWindowStart() time.Time {
	return time.Now().Add(-3 * time.Minute)
}

// parseLogPayloadFromLine extracts slog JSON from Loki lines or docker compose log prefixes.
func parseLogPayloadFromLine(line string) map[string]string {
	line = strings.TrimSpace(line)
	if i := strings.Index(line, "{"); i >= 0 {
		return ParseLogKV(line[i:])
	}
	return ParseLogKV(line)
}

// ParseLogKV extracts key=value pairs from a slog-style JSON log line (flat object).
// Alloy/docker may wrap the payload as {"log":"{...}\n","stream":"stderr"}.
func ParseLogKV(line string) map[string]string {
	line = strings.TrimSpace(line)
	out := make(map[string]string)
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return out
	}
	if wrapped, ok := m["log"].(string); ok {
		inner := strings.TrimSpace(wrapped)
		var nested map[string]any
		if err := json.Unmarshal([]byte(inner), &nested); err == nil {
			m = nested
		}
	}
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// waitLokiHeightSyncEmitMode polls until a heightsync: emit line matches service, direction, mode, and nonce.
// end bounds the query_range upper bound (use time.Now().Add(30s) while polling).
func waitLokiHeightSyncEmitMode(
	t *testing.T, c *http.Client, logQL string, t0, end time.Time, wantNonce int, direction, mode string, timeout time.Duration,
) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		qEnd := end
		if qEnd.Before(time.Now()) {
			qEnd = time.Now().Add(30 * time.Second)
		}
		for _, ln := range LokiQueryRange(t, c, logQL, t0, qEnd, 5000) {
			kv := ParseLogKV(ln)
			if kv["msg"] != "heightsync: emit" || kv["direction"] != direction {
				continue
			}
			if !strings.EqualFold(kv["mode"], mode) {
				continue
			}
			if parseNonce(kv["nonce"]) == wantNonce {
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// waitLokiPeerAttestationMode polls heightsync: peer attestation received for direction + mode + nonce.
func waitLokiPeerAttestationMode(
	t *testing.T, c *http.Client, logQL string, t0, end time.Time, wantNonce int, direction, mode string, timeout time.Duration,
) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		qEnd := end
		if qEnd.Before(time.Now()) {
			qEnd = time.Now().Add(30 * time.Second)
		}
		for _, ln := range LokiQueryRange(t, c, logQL, t0, qEnd, 5000) {
			kv := ParseLogKV(ln)
			if kv["msg"] != "heightsync: peer attestation received" || kv["direction"] != direction {
				continue
			}
			if !strings.EqualFold(kv["mode"], mode) {
				continue
			}
			if parseNonce(kv["nonce"]) == wantNonce {
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// waitLokiHostPeerAttestationMode polls host inbound peer-attestation logs for mode + nonce.
func waitLokiHostPeerAttestationMode(
	t *testing.T, c *http.Client, logQL string, t0, end time.Time, wantNonce int, mode string, timeout time.Duration,
) bool {
	return waitLokiPeerAttestationMode(t, c, logQL, t0, end, wantNonce, "request", mode, timeout)
}

// parseNonce is shared by container Loki assertions.
func parseNonce(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	u, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0
	}
	return int(u)
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

// HasHeightSyncRequestEmit reports whether lines contain a request-direction emit for nonce/mode.
func HasHeightSyncRequestEmit(lines []string, nonce int, mode string) bool {
	for _, ln := range lines {
		kv := ParseLogKV(ln)
		if kv["msg"] != "heightsync: emit" || kv["direction"] != "request" {
			continue
		}
		if !strings.EqualFold(kv["mode"], mode) {
			continue
		}
		if parseNonce(kv["nonce"]) == nonce {
			return true
		}
	}
	return false
}

// CountHeightSyncRequestEmitInRange counts distinct request-direction emits in [startNonce, endNonce].
func CountHeightSyncRequestEmitInRange(lines []string, startNonce, endNonce int, mode string) int {
	return CountHeightSyncEmitInRange(lines, startNonce, endNonce, "request", mode)
}

// CountHeightSyncEmitInRange counts distinct nonces in [startNonce, endNonce] for direction+mode.
func CountHeightSyncEmitInRange(lines []string, startNonce, endNonce int, direction, mode string) int {
	seen := make(map[int]struct{})
	for _, ln := range lines {
		kv := ParseLogKV(ln)
		if kv["msg"] != "heightsync: emit" || kv["direction"] != direction {
			continue
		}
		if !strings.EqualFold(kv["mode"], mode) {
			continue
		}
		n := parseNonce(kv["nonce"])
		if n < startNonce || n > endNonce {
			continue
		}
		seen[n] = struct{}{}
	}
	return len(seen)
}
