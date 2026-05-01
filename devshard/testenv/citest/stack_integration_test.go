//go:build testenvci

package citest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStackIntegrationI1andSection8_7 runs §8.2 I1 (height-sync reachable) and
// §8.7 observability wiring. Requires a working Docker daemon, substantial
// images/build, and free host ports (e.g. 3000, 3100, 8200+). Skip locally with
// TESTENV_SKIP_DOCKER_STACK=1.
func TestStackIntegrationI1andSection8_7(t *testing.T) {
	if os.Getenv("TESTENV_SKIP_DOCKER_STACK") == "1" {
		t.Skip("TESTENV_SKIP_DOCKER_STACK=1")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}

	testenvDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	composeFile := filepath.Join(testenvDir, "docker-compose.yml")
	if _, err := os.Stat(composeFile); err != nil {
		t.Fatalf("docker-compose.yml: %v", err)
	}

	project := fmt.Sprintf("citest%d", os.Getpid())
	deadline, ok := t.Deadline()
	if !ok {
		deadline = time.Now().Add(7 * time.Minute)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	down := func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer dcancel()
		_ = exec.CommandContext(dctx, "docker", "compose", "-f", composeFile, "-p", project, "down", "--remove-orphans", "--timeout", "60").Run()
	}
	down() // best-effort cleanup if a prior run was interrupted
	t.Cleanup(down)

	// --build: cold CI needs images; can take several minutes.
	up := exec.CommandContext(ctx, "docker", "compose", "-f", composeFile, "-p", project, "up", "-d", "--build")
	up.Dir = testenvDir
	up.Stdout, up.Stderr = os.Stdout, os.Stderr
	if err := up.Run(); err != nil {
		t.Fatalf("docker compose up: %v", err)
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	base := time.Now()
	for {
		if ctx.Err() != nil {
			t.Fatalf("deadline: stack did not become ready: %v", ctx.Err())
		}
		h, err := getHeightFromLatest(httpClient, "http://127.0.0.1:9100/block/latest")
		if err == nil && h > 0 {
			t.Logf("I1: height-sync /block/latest height=%d (after %s)", h, time.Since(base).Round(time.Second))
			break
		}
		if time.Since(base) > 4*time.Minute {
			if err != nil {
				t.Fatalf("I1: /block/latest: %v", err)
			}
			t.Fatalf("I1: expected height > 0, got %d", h)
		}
		time.Sleep(3 * time.Second)
	}

	// --- §8.7 (wiring, not dashboard contents) — poll until services answer ---
	dead2 := time.Now().Add(2 * time.Minute)
	for time.Now().Before(dead2) {
		if err := checkSection87(httpClient, t); err == nil {
			break
		} else {
			t.Logf("8.7 waiting: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
	if err := checkSection87(httpClient, t); err != nil {
		t.Fatalf("8.7: %v", err)
	}

	// --- §8.2 I2 — per-host cached heights (from VM; Alloy scrapes devshardd /metrics) ---
	i2HeightsConverge(httpClient, t)

	// --- §8.2 I9 — 20 consecutive fresh headers vs pinned verifier (config.yaml) ---
	i9MultiValidatorStreamVsAuditor(filepath.Join(testenvDir, "config.yaml"), "http://127.0.0.1:9100", httpClient, t)
}

func getHeightFromLatest(c *http.Client, u string) (int64, error) {
	// blockoracle wire uses default JSON field names: Height → "Height" in Go 1+ with no json tag
	// (capital H). The server may emit "Height" not "height" — support both.
	resp, err := c.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	var a struct {
		Height *int64 `json:"height"`
	}
	_ = json.Unmarshal(body, &a)
	if a.Height != nil && *a.Height > 0 {
		return *a.Height, nil
	}
	var b struct {
		Height *int64 `json:"Height"`
	}
	_ = json.Unmarshal(body, &b)
	if b.Height != nil && *b.Height > 0 {
		return *b.Height, nil
	}
	return 0, fmt.Errorf("no height in body: %s", string(body))
}

func checkSection87(c *http.Client, t *testing.T) error {
	// 1) VictoriaMetrics / Prometheus API — at least 3 time series in vector result
	q := url.QueryEscape(`up{job!=""}`)
	vmURL := "http://127.0.0.1:8428/api/v1/query?query=" + q
	resp, err := c.Get(vmURL)
	if err != nil {
		return fmt.Errorf("vm query: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vm status %d: %s", resp.StatusCode, string(b))
	}
	var vm struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct{} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &vm); err != nil {
		return fmt.Errorf("vm json: %w", err)
	}
	if vm.Status != "success" || len(vm.Data.Result) < 3 {
		return fmt.Errorf("vm: want ≥3 up series, got status=%q n=%d", vm.Status, len(vm.Data.Result))
	}
	t.Logf("8.7: victoria query ok (n=%d)", len(vm.Data.Result))

	// 2) Grafana health
	resp, err = c.Get("http://127.0.0.1:3000/api/health")
	if err != nil {
		return fmt.Errorf("grafana health: %w", err)
	}
	defer resp.Body.Close()
	gh, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("grafana /api/health %d: %s", resp.StatusCode, string(gh))
	}
	var grafanaHealth struct {
		Database string `json:"database"`
	}
	if err := json.Unmarshal(gh, &grafanaHealth); err != nil {
		return fmt.Errorf("grafana /api/health json: %w", err)
	}
	if grafanaHealth.Database != "ok" {
		return fmt.Errorf("grafana /api/health: want database=ok, got %q", grafanaHealth.Database)
	}
	t.Logf("8.7: grafana /api/health ok")

	// 3) Loki ready
	resp, err = c.Get("http://127.0.0.1:3100/ready")
	if err != nil {
		return fmt.Errorf("loki /ready: %w", err)
	}
	defer resp.Body.Close()
	lb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("loki /ready %d: %s", resp.StatusCode, string(lb))
	}
	if !strings.Contains(string(lb), "ready") {
		return fmt.Errorf("loki /ready body: %s", string(lb))
	}
	t.Logf("8.7: loki /ready ok")

	// 4) provisioned overview dashboard
	resp, err = c.Get("http://127.0.0.1:3000/api/dashboards/uid/devshard-overview")
	if err != nil {
		return fmt.Errorf("grafana dashboard: %w", err)
	}
	defer resp.Body.Close()
	db, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("grafana dashboard %d: %s", resp.StatusCode, string(db))
	}
	s := string(db)
	if !strings.Contains(s, "Chain") || !strings.Contains(s, "Gossip") || !strings.Contains(s, "Resource") {
		return fmt.Errorf("dashboard: expected Chain+Gossip+Resource in JSON")
	}
	t.Logf("8.7: grafana devshard-overview row titles ok")
	return nil
}
