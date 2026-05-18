package container

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// auditInferenceStall dumps compose + Loki lines for a slow inference (RefusalTimeout is ~65s).
func auditInferenceStall(t *testing.T, ws, project string, lokiClient *http.Client, nonce int, started time.Time, elapsed time.Duration) {
	t.Helper()
	executor := hostServiceForNonce(uint64(nonce))
	t.Logf("━━ inference %d stall audit (elapsed=%s, executor=%s) ━━", nonce, elapsed.Round(time.Millisecond), executor)

	needle := fmt.Sprintf("%d", nonce)
	patterns := []string{
		fmt.Sprintf("inference nonce=%s", needle),
		fmt.Sprintf("inference %s:", needle),
		fmt.Sprintf("inference_id=%s", needle),
		fmt.Sprintf(`"inference_id":%s`, needle),
		fmt.Sprintf(`"inference_id": %s`, needle),
		"RefusalTimeout",
		"TIMEOUT_REASON_REFUSED",
		"SendOnly failed",
		"timed out",
		"testenv inference",
		"response hold",
		"heightsync:",
	}

	services := []string{"devshardctl", executor}
	for _, svc := range services {
		lines := composeLogsSince(t, ws, project, svc, started.Add(-15*time.Second), 800)
		hits := filterLogLines(lines, patterns...)
		t.Logf("compose %s: %d matching lines (of %d tail)", svc, len(hits), len(lines))
		for _, ln := range tailLines(hits, 40) {
			t.Logf("  [%s] %s", svc, truncateForLog(ln, 500))
		}
	}

	if lokiClient != nil {
		windowStart := started.Add(-30 * time.Second)
		windowEnd := time.Now().Add(10 * time.Second)
		queries := []struct {
			name string
			ql   string
		}{
			{"devshardctl", fmt.Sprintf(`{service_name="devshardctl"} |~ "inference.*%s|nonce.*%s|timed out|SendOnly"`, needle, needle)},
			{"executor", fmt.Sprintf(`{service_name="%s"} |~ "inference_id|%s|testenv inference|hold|heightsync"`, executor, needle)},
		}
		for _, q := range queries {
			lines := LokiQueryRange(t, lokiClient, q.ql, windowStart, windowEnd, 300)
			t.Logf("loki %s: %d lines", q.name, len(lines))
			for _, ln := range tailLines(lines, 25) {
				kv := ParseLogKV(ln)
				msg := kv["msg"]
				if msg == "" {
					msg = truncateForLog(ln, 120)
				}
				t.Logf("  [loki/%s] msg=%q nonce=%s inference_id=%s elapsed_ms=%s",
					q.name, msg, kv["nonce"], kv["inference_id"], kv["elapsed_ms"])
			}
		}
	}

	logRefusalHintFromCompose(t, ws, project, nonce, started)
	t.Logf("━━ end inference %d audit — if gap≈65s, devshardctl likely waited RefusalTimeout (60s)+buffer; grep SendOnly failed / no MsgFinishInference ━━", nonce)
}

func composeLogsSince(t *testing.T, ws, project, service string, since time.Time, tail int) []string {
	t.Helper()
	composeFile := filepath.Join(ws, "docker-compose.yml")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{
		"compose", "-f", composeFile, "-p", project,
		"logs", "--timestamps", "--since", since.UTC().Format(time.RFC3339),
		fmt.Sprintf("--tail=%d", tail), service,
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("compose logs %s: %v", service, err)
		return nil
	}
	var lines []string
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	return lines
}

func filterLogLines(lines []string, patterns ...string) []string {
	var out []string
	for _, ln := range lines {
		for _, p := range patterns {
			if strings.Contains(ln, p) {
				out = append(out, ln)
				break
			}
		}
	}
	return out
}

func tailLines(lines []string, n int) []string {
	if n <= 0 || len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// slowInferenceAuditThreshold triggers compose/Loki audit after postChatCompletionStream.
const slowInferenceAuditThreshold = 8 * time.Second

func maybeAuditSlowInference(t *testing.T, inferIdx int, started time.Time, elapsed time.Duration, lokiClient *http.Client) {
	t.Helper()
	if elapsed < slowInferenceAuditThreshold {
		return
	}
	if os.Getenv("TESTENV_REUSE_STACK") != "1" {
		t.Logf("inference %d slow (%s); set TESTENV_REUSE_STACK=1 and re-run for compose/Loki audit", inferIdx, elapsed)
		return
	}
	ws := TestenvDir(t)
	project := ContainerE2EComposeProject()
	auditInferenceStall(t, ws, project, lokiClient, inferIdx, started, elapsed)
}

// extractRefusalWaitHint returns a one-line guess from devshardctl logs.
var refusalWaitRe = regexp.MustCompile(`inference (\d+) timed out: TIMEOUT_REASON_REFUSED`)

func logRefusalHintFromCompose(t *testing.T, ws, project string, nonce int, started time.Time) {
	t.Helper()
	lines := composeLogsSince(t, ws, project, "devshardctl", started, 200)
	for _, ln := range lines {
		if m := refusalWaitRe.FindStringSubmatch(ln); len(m) == 2 && m[1] == fmt.Sprintf("%d", nonce) {
			t.Logf("hint: %s → proxy exhausted RefusalTimeout (60s)+5s buffer waiting for executor receipt/finish", truncateForLog(ln, 200))
			return
		}
		if strings.Contains(ln, fmt.Sprintf("inference nonce=%d", nonce)) && strings.Contains(ln, "SendOnly failed") {
			t.Logf("hint: %s → host never returned SSE body; proxy will sleep RefusalTimeout before retry", truncateForLog(ln, 200))
			return
		}
	}
}
