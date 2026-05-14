//go:build testenvci

// Package harness holds shared testenv CI helpers (VictoriaMetrics, height-sync
// verifier checks) used by citest and container scenario packages.
package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"devshard/blockoracle"
	"devshard/blockoracle/verifier"
	"devshard/testenv/config"
)

func i9LineStart() {
	if StderrColorEnabled() {
		_, _ = fmt.Fprint(os.Stderr, "\033[1;36m[citest I9]\033[0m ")
		return
	}
	_, _ = fmt.Fprint(os.Stderr, "[citest I9] ")
}

func i9ProgressLoaded(cfgPath string, nVal, wantDistinct int, heightSyncBase string) {
	if StderrColorEnabled() {
		i9LineStart()
		_, _ = fmt.Fprintf(os.Stderr,
			"verifier from \033[2m%s\033[0m (\033[1m%d\033[0m validators); \033[32m%d\033[0m× GET \033[2m%s/block/latest\033[0m\n",
			cfgPath, nVal, wantDistinct, heightSyncBase)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[citest I9] verifier from %s (%d validators); %d× GET %s/block/latest\n",
		cfgPath, nVal, wantDistinct, heightSyncBase)
}

func i9ProgressVerified(height int64, sigs, verified, want int) {
	if !StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "[citest I9] verified height=%d sigs=%d (%d/%d)\n", height, sigs, verified, want)
		return
	}
	i9LineStart()
	_, _ = fmt.Fprintf(os.Stderr, "verified height=\033[1m%d\033[0m sigs=", height)
	if sigs < 10 && sigs >= 8 {
		_, _ = fmt.Fprintf(os.Stderr, "\033[33m%d\033[0m", sigs)
	} else {
		_, _ = fmt.Fprintf(os.Stderr, "%d", sigs)
	}
	_, _ = fmt.Fprintf(os.Stderr, " (\033[32m%d/%d\033[0m)\n", verified, want)
}

func i9ProgressFetchErr(err error) {
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[2m[I9 fetch retry] %v\033[0m\n", err)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[I9 fetch retry] %v\n", err)
}

func i9ProgressPartialNote() {
	if StderrColorEnabled() {
		_, _ = fmt.Fprintln(os.Stderr, "\033[33m[I9]\033[0m \033[2mno 8–9 signature block observed (not failing)\033[0m")
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "[I9] no 8–9 signature block observed (not failing)")
}

func parseDevsharddHeightAtLatestNonceFromMetricsText(text string) (float64, error) {
	const prefix = "devshardd_height_at_latest_nonce"
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		if name != prefix && !strings.HasPrefix(name, prefix+"{") {
			continue
		}
		var v float64
		if _, err := fmt.Sscan(fields[1], &v); err != nil {
			return 0, fmt.Errorf("parse %s value %q: %w", prefix, fields[1], err)
		}
		return v, nil
	}
	return 0, fmt.Errorf("%s sample not found", prefix)
}

// I2aDirectHostOracleHeights runs testenv.md §7.2 I2a — protocol view: in one tight loop
// (no delay between hosts), GET each devshardd-testenv /metrics on 127.0.0.1:public_metrics_port
// and parse devshardd_height_at_latest_nonce. Logs each host height; requires max(H)−min(H) ≤ 1.
func I2aDirectHostOracleHeights(cfgPath string, c *http.Client, t *testing.T) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("I2a: load config: %v", err)
	}
	if len(cfg.Hosts) == 0 {
		t.Fatal("I2a: no hosts in config")
	}
	fast := &http.Client{Timeout: 3 * time.Second}
	if c != nil && c.Transport != nil {
		fast.Transport = c.Transport
	}
	deadline := time.Now().Add(2 * time.Minute)
	lastNote := time.Now()
	for time.Now().Before(deadline) {
		type sample struct {
			id     string
			port   int
			height int64
		}
		var samples []sample
		var firstErr error
		for _, h := range cfg.Hosts {
			port := h.PublicMetricsPort
			if port <= 0 {
				t.Fatalf("I2a: host %q has public_metrics_port unset (re-run gencompose)", h.ID)
			}
			u := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
			resp, err := fast.Get(u)
			if err != nil {
				firstErr = fmt.Errorf("%s: %w", h.ID, err)
				break
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				firstErr = fmt.Errorf("%s: read body: %w", h.ID, err)
				break
			}
			if resp.StatusCode != http.StatusOK {
				firstErr = fmt.Errorf("%s: http %d", h.ID, resp.StatusCode)
				break
			}
			v, err := parseDevsharddHeightAtLatestNonceFromMetricsText(string(body))
			if err != nil {
				firstErr = fmt.Errorf("%s: %w", h.ID, err)
				break
			}
			samples = append(samples, sample{id: h.ID, port: port, height: int64(math.Round(v))})
		}
		if firstErr != nil {
			CitestPrintI2aWait("wait: %v", firstErr)
			if time.Since(lastNote) > 15*time.Second {
				CitestPrintI2a("per-host /metrics wait: %v", firstErr)
				lastNote = time.Now()
			}
			time.Sleep(2 * time.Second)
			continue
		}
		if len(samples) < len(cfg.Hosts) {
			CitestPrintI2aWait("incomplete pass (have %d hosts)", len(samples))
			time.Sleep(2 * time.Second)
			continue
		}
		heights := make([]float64, len(samples))
		for i := range samples {
			heights[i] = float64(samples[i].height)
		}
		hi, lo := MaxMin(heights)
		if hi-lo > 1 {
			for _, s := range samples {
				CitestPrintI2a("%s :%d height_at_latest_nonce=%d", s.id, s.port, s.height)
			}
			CitestPrintI2aWait("spread %d−%d=%d > 1; retrying…", hi, lo, hi-lo)
			time.Sleep(2 * time.Second)
			continue
		}
		for _, s := range samples {
			CitestPrintI2a("%s :%d height_at_latest_nonce=%d", s.id, s.port, s.height)
		}
		CitestPrintI2a("protocol spread ok (min=%d max=%d spread=%d, n=%d hosts)", lo, hi, hi-lo, len(samples))
		return
	}
	t.Fatal("I2a: deadline: could not observe converged per-host oracle heights")
}

// I2bVictoriaMetricsHostHeightSpread runs §7.2 I2b — observability view: instant PromQL on
// VictoriaMetrics for devshardd_height_at_latest_nonce (Alloy scrape path). Alloy → VM
// timing can widen spread vs I2a; allow max(H)−min(H) ≤ 3.
func I2bVictoriaMetricsHostHeightSpread(c *http.Client, t *testing.T) {
	t.Helper()
	CitestPrintI2b("querying VictoriaMetrics for devshardd_height_at_latest_nonce (see testenv.md §7.2 I2b)...")
	q := url.QueryEscape("devshardd_height_at_latest_nonce")
	vmURL := "http://127.0.0.1:8428/api/v1/query?query=" + q
	deadline := time.Now().Add(2 * time.Minute)
	lastNote := time.Now()
	for time.Now().Before(deadline) {
		vals, err := PrometheusInstantVectorValues(c, vmURL)
		if err != nil {
			CitestPrintI2bWait("wait: %v", err)
			if time.Since(lastNote) > 15*time.Second {
				CitestPrintI2b("VM query wait: %v", err)
				lastNote = time.Now()
			}
			time.Sleep(2 * time.Second)
			continue
		}
		if len(vals) < 4 {
			CitestPrintI2bColon("want 4 devshardd series, got %d; waiting for scrape…", len(vals))
			if time.Since(lastNote) > 15*time.Second {
				CitestPrintI2b("waiting for ≥4 VM series (have %d)...", len(vals))
				lastNote = time.Now()
			}
			time.Sleep(3 * time.Second)
			continue
		}
		hi, lo := MaxMin(vals)
		if hi-lo > 3 {
			t.Fatalf("I2b: max(H_i)−min(H_i) = %d−%d = %d, want ≤ 3 (VM scrape skew)", hi, lo, hi-lo)
		}
		CitestPrintI2bColon("VM height spread ok (min=%d max=%d spread=%d, n=%d series)", lo, hi, hi-lo, len(vals))
		return
	}
	t.Fatal("I2b: deadline: could not get 4 devshardd_height_at_latest_nonce series in time")
}

// PrometheusInstantVectorValues parses a Prometheus / VictoriaMetrics instant vector query result.
func PrometheusInstantVectorValues(c *http.Client, vmURL string) ([]float64, error) {
	resp, err := c.Get(vmURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out.Status != "success" || out.Data.ResultType != "vector" {
		return nil, fmt.Errorf("unexpected vm response status=%q type=%q", out.Status, out.Data.ResultType)
	}
	var vals []float64
	for _, p := range out.Data.Result {
		if len(p.Value) < 2 {
			continue
		}
		s, ok := p.Value[1].(string)
		if !ok {
			if f, ok2 := p.Value[1].(float64); ok2 {
				vals = append(vals, f)
			}
			continue
		}
		var v float64
		if _, err := fmt.Sscan(s, &v); err != nil {
			continue
		}
		vals = append(vals, v)
	}
	return vals, nil
}

// MaxMin returns max and min of a non-empty float slice (rounded to int64).
func MaxMin(vals []float64) (hi, lo int64) {
	if len(vals) == 0 {
		return 0, 0
	}
	h := vals[0]
	l := vals[0]
	for _, v := range vals[1:] {
		if v > h {
			h = v
		}
		if v < l {
			l = v
		}
	}
	return int64(math.Round(h)), int64(math.Round(l))
}

// MultiValidatorStreamVsAuditor runs testenv.md §7.2 I9 — stream from height-sync vs pinned verifier.
func MultiValidatorStreamVsAuditor(cfgPath, heightSyncBase string, c *http.Client, t *testing.T) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("I9: load config: %v", err)
	}
	hv, err := cfg.HeightSyncValidators()
	if err != nil {
		t.Fatalf("I9: validators: %v", err)
	}
	vers := make([]verifier.Validator, len(hv))
	for i, v := range hv {
		vers[i] = verifier.Validator{Address: v.Address, Power: v.Power}
	}
	vs, err := verifier.NewValidatorSet(cfg.Chain.ID, vers)
	if err != nil {
		t.Fatalf("I9: validator set: %v", err)
	}
	const wantDistinct = 20
	vf := verifier.New(vs)
	i9ProgressLoaded(cfgPath, len(vers), wantDistinct, heightSyncBase)

	var lastOK int64
	partialSigsSeen := false
	verified := 0
	deadline := time.Now().Add(2 * time.Minute)
	for verified < wantDistinct {
		if time.Now().After(deadline) {
			t.Fatalf("I9: want %d verified headers, got %d; partial 8–9-sig block seen=%v", wantDistinct, verified, partialSigsSeen)
		}
		h, err := FetchBlockLatestHeader(c, heightSyncBase+"/block/latest")
		if err != nil {
			i9ProgressFetchErr(err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if n := len(h.Commit.Signatures); n < 10 && n >= 8 {
			partialSigsSeen = true
		}
		if err := vf.Verify(h, lastOK); err != nil {
			if lastOK > 0 && errors.Is(err, verifier.ErrStale) {
				time.Sleep(150 * time.Millisecond)
				continue
			}
			t.Fatalf("I9: verify: %v", err)
		}
		if h.Height <= lastOK {
			t.Fatalf("I9: expected advancing height after verify, got %d last=%d", h.Height, lastOK)
		}
		lastOK = h.Height
		verified++
		i9ProgressVerified(h.Height, len(h.Commit.Signatures), verified, wantDistinct)
		time.Sleep(200 * time.Millisecond)
	}
	if !partialSigsSeen {
		i9ProgressPartialNote()
	}
}

// FetchBlockLatestHeader GETs /block/latest JSON into blockoracle.Header.
func FetchBlockLatestHeader(c *http.Client, u string) (*blockoracle.Header, error) {
	resp, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	if strings.HasPrefix(string(body), "<!") {
		return nil, fmt.Errorf("non-json body: %q", FirstRunes(string(body), 64))
	}
	var h blockoracle.Header
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// FirstRunes truncates s to at most n runes for error messages.
func FirstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
