//go:build testenvci

package container

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
)

const victoriaMetricsURL = "http://127.0.0.1:8428"

// PromInstantScalar runs an instant PromQL query and returns the first scalar float (or 0 if empty).
func PromInstantScalar(t *testing.T, c *http.Client, expr string) float64 {
	t.Helper()
	q := url.QueryEscape(expr)
	u := victoriaMetricsURL + "/api/v1/query?query=" + q
	resp, err := c.Get(u)
	if err != nil {
		t.Fatalf("prometheus query: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("prometheus read: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prometheus http %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("prometheus json: %v", err)
	}
	if out.Status != "success" || len(out.Data.Result) == 0 {
		return 0
	}
	v := out.Data.Result[0].Value
	if len(v) < 2 {
		return 0
	}
	switch s := v[1].(type) {
	case string:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatalf("prometheus parse scalar %q: %v", s, err)
		}
		return f
	case float64:
		return s
	default:
		f, err := strconv.ParseFloat(fmt.Sprint(s), 64)
		if err != nil {
			t.Fatalf("prometheus parse scalar %#v: %v", s, err)
		}
		return f
	}
}
