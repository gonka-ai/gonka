//go:build testenvci

package citest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"devshard/blockoracle"
	"devshard/blockoracle/verifier"
	"devshard/testenv/config"
)

// --- §8.2 I2 — height convergence (VictoriaMetrics gauges from Alloy scrapes) ---

func i2HeightsConverge(c *http.Client, t *testing.T) {
	t.Helper()
	q := url.QueryEscape("devshardd_height_at_latest_nonce")
	vmURL := "http://127.0.0.1:8428/api/v1/query?query=" + q
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		vals, err := prometheusInstantVectorValues(c, vmURL)
		if err != nil {
			t.Logf("I2 wait: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if len(vals) < 4 {
			t.Logf("I2: want 4 devshardd series, got %d; waiting for scrape…", len(vals))
			time.Sleep(3 * time.Second)
			continue
		}
		hi, lo := maxMin(vals)
		if hi-lo > 1 {
			t.Fatalf("I2: max(H_i)−min(H_i) = %d−%d = %d, want ≤ 1 (steady state)", hi, lo, hi-lo)
		}
		t.Logf("I2: height spread ok (min=%d max=%d, n=%d series)", lo, hi, len(vals))
		return
	}
	t.Fatal("I2: deadline: could not get 4 devshardd_height_at_latest_nonce series in time")
}

func prometheusInstantVectorValues(c *http.Client, vmURL string) ([]float64, error) {
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

func maxMin(vals []float64) (hi, lo int64) {
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

// --- §8.2 I9 — stream from height-sync vs pinned verifier (subset of spec) ---

func i9MultiValidatorStreamVsAuditor(cfgPath, heightSyncBase string, c *http.Client, t *testing.T) {
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
	vf := verifier.New(vs)

	var lastOK int64
	partialSigsSeen := false
	verified := 0
	const wantDistinct = 20
	deadline := time.Now().Add(2 * time.Minute)
	for verified < wantDistinct {
		if time.Now().After(deadline) {
			t.Fatalf("I9: want %d verified headers, got %d; partial 8–9-sig block seen=%v", wantDistinct, verified, partialSigsSeen)
		}
		h, err := fetchBlockLatestHeader(c, heightSyncBase+"/block/latest")
		if err != nil {
			t.Logf("I9: fetch: %v", err)
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
		t.Logf("I9: verified height=%d sigs=%d (%d/%d)", h.Height, len(h.Commit.Signatures), verified, wantDistinct)
		time.Sleep(200 * time.Millisecond)
	}
	if !partialSigsSeen {
		t.Logf("I9: no 8–9 signature block observed (10-validator runs may be all-full on some ticks); not failing")
	}
}

func fetchBlockLatestHeader(c *http.Client, u string) (*blockoracle.Header, error) {
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
		return nil, fmt.Errorf("non-json body: %q", firstRunes(string(body), 64))
	}
	var h blockoracle.Header
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
