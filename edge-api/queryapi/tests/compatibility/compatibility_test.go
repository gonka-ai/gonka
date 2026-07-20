package compatibility_test

// Compatibility test: calls the same endpoints on two running servers and
// compares HTTP status codes and top-level JSON keys.
//
// Run:
//
//	go test -v -tags compat -run TestCompat . \
//	    -endpoint1 http://localhost:18080 \
//	    -endpoint2 http://node1.gonka.ai:8000/api

import (
	. "edge-api/queryapi/gen"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var (
	endpoint1 = flag.String("endpoint1", "", "First server base URL (required)")
	endpoint2 = flag.String("endpoint2", "", "Second server base URL (required)")

	// Flags for params that cannot be derived from chain state.
	paramPubkey        = flag.String("param.pubkey", "", "Hex pubkey for /v1/debug/pubkey-to-addr/{pubkey}")
	paramRequestID     = flag.String("param.request_id", "", "Hex request ID for /v1/bls/signatures/{request_id}")
	paramExemptionID   = flag.String("param.exemption_id", "", "Exemption ID for /v1/restrictions/exemptions/{id}/usage/{account}")
	paramExemptionAcct = flag.String("param.exemption_account", "", "Account for /v1/restrictions/exemptions/{id}/usage/{account}")
)

// chainParams holds values derived from the chain at test startup.
type chainParams struct {
	blsEpoch Uint64
	pocEpoch Int64 // 0 = skip
	height   Int64 // 0 = skip
	address  string
}

// resolveChainParams queries endpoint1 to derive test parameters from live
// chain state so callers don't have to pass them manually.
func resolveChainParams(ctx context.Context, c *ClientWithResponses) chainParams {
	p := chainParams{blsEpoch: 1} // safe fallback

	if r, err := c.GetEpochWithResponse(ctx, "latest"); err == nil && r.JSON200 != nil {
		ep := r.JSON200
		idx := Uint64(ep.LatestEpoch.Index)
		if idx > 0 {
			p.blsEpoch = idx
		}
		p.height = ep.BlockHeight - 25
		p.pocEpoch = Int64(ep.LatestEpoch.PocStartBlockHeight)
	}

	if r, err := c.GetParticipantsWithResponse(ctx); err == nil && r.JSON200 != nil {
		for _, part := range r.JSON200.Participants {
			if part.Id != "" {
				p.address = part.Id
				break
			}
		}
	}

	return p
}

func TestVersion(t *testing.T) {
	if *endpoint1 == "" || *endpoint2 == "" {
		t.Skip("skipping: both -endpoint1 and -endpoint2 are required")
	}

	c1, err := NewClientWithResponses(*endpoint1)
	if err != nil {
		t.Fatalf("client1: %v", err)
	}
	c2, err := NewClientWithResponses(*endpoint2)
	if err != nil {
		t.Fatalf("client2: %v", err)
	}

	ctx := context.Background()

	runCompatibilityTest(t, c1, c2, "versions", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetVersionsWithResponse(ctx)
		return statusOf(r, err), bodyOf(r), err
	})
}

func TestCompatibility(t *testing.T) {
	if *endpoint1 == "" || *endpoint2 == "" {
		t.Skip("skipping: both -endpoint1 and -endpoint2 are required")
	}

	c1, err := NewClientWithResponses(*endpoint1)
	if err != nil {
		t.Fatalf("client1: %v", err)
	}
	c2, err := NewClientWithResponses(*endpoint2)
	if err != nil {
		t.Fatalf("client2: %v", err)
	}

	ctx := context.Background()

	p := resolveChainParams(ctx, c1)
	t.Logf("chain params: blsEpoch=%d pocEpoch=%d height=%d address=%s", p.blsEpoch, p.pocEpoch, p.height, p.address)

	runCompatibilityTest(t, c1, c2, "status", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetStatusWithResponse(ctx)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, "versions", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetVersionsWithResponse(ctx)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, "models", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetModelsWithResponse(ctx)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, "governance/models", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetGovernanceModelsWithResponse(ctx)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, "governance/models-legacy", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetGovernanceModelsLegacyWithResponse(ctx)
		return statusOf(r, err), bodyOf(r), err
	})
	// runCompatibilityTest(t, c1, c2, "epochs/latest", func(c *ClientWithResponses) (int, string, error) {
	// 	r, err := c.GetEpochWithResponse(ctx, "latest")
	// 	return statusOf(r, err), bodyOf(r), err
	// })
	runCompatibilityTest(t, c1, c2, "epochs/latest/participants", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetEpochParticipantsWithResponse(ctx, "latest")
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, "pricing", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetPricingWithResponse(ctx)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, "restrictions/status", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetRestrictionsStatusWithResponse(ctx)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, "restrictions/exemptions", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetRestrictionsExemptionsWithResponse(ctx)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, fmt.Sprintf("bls/epoch/%d", p.blsEpoch), func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetBLSEpochWithResponse(ctx, p.blsEpoch)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, fmt.Sprintf("bls/epochs/%d", p.blsEpoch), func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetBLSEpochsWithResponse(ctx, p.blsEpoch)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, "bridge/addresses?chain=ethereum", func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetBridgeAddressesWithResponse(ctx, &GetBridgeAddressesParams{Chain: "ethereum"})
		return statusOf(r, err), bodyOf(r), err
	})

	// participants: retry if block heights differ between the two endpoints.
	t.Run("participants", func(t *testing.T) {
		t.Parallel()
		const maxRetries = 5
		for range maxRetries {
			r1, err1 := c1.GetParticipantsWithResponse(ctx)
			r2, err2 := c2.GetParticipantsWithResponse(ctx)
			if err1 != nil || err2 != nil {
				t.Errorf("request error: endpoint1=%v endpoint2=%v", err1, err2)
				return
			}
			if r1.JSON200 != nil && r2.JSON200 != nil && r1.JSON200.BlockHeight != r2.JSON200.BlockHeight {
				continue
			}
			status1, body1 := statusOf(r1, nil), bodyOf(r1)
			status2, body2 := statusOf(r2, nil), bodyOf(r2)
			if status1 != status2 {
				t.Errorf("status mismatch: endpoint1=%d endpoint2=%d body1=%s body2=%s", status1, status2, body1, body2)
				return
			}
			if status1/100 != 2 {
				return
			}
			if body1 != body2 {
				t.Errorf("body mismatch:\n%s", jsonDiff(body1, body2))
			}
			return
		}
		t.Errorf("block heights did not converge after %d retries", maxRetries)
	})

	runCompatibilityTest(t, c1, c2, "participants/"+p.address, func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetParticipantWithResponse(ctx, p.address)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, fmt.Sprintf("poc-batches/%d", p.pocEpoch), func(c *ClientWithResponses) (int, string, error) {
		r, err := c.GetPoCBatchesWithResponse(ctx, p.pocEpoch)
		return statusOf(r, err), bodyOf(r), err
	})
	runCompatibilityTest(t, c1, c2, fmt.Sprintf("debug/verify/%d", p.height), func(c *ClientWithResponses) (int, string, error) {
		r, err := c.DebugVerifyBlockSignaturesWithResponse(ctx, p.height)
		return statusOf(r, err), bodyOf(r), err
	})
	if *paramPubkey != "" {
		pk := *paramPubkey
		runCompatibilityTest(t, c1, c2, "debug/pubkey-to-addr/"+pk, func(c *ClientWithResponses) (int, string, error) {
			r, err := c.DebugPubKeyToAddrWithResponse(ctx, pk)
			return statusOf(r, err), bodyOf(r), err
		})
	}
	if *paramRequestID != "" {
		rid := *paramRequestID
		runCompatibilityTest(t, c1, c2, "bls/signatures/"+rid, func(c *ClientWithResponses) (int, string, error) {
			r, err := c.GetBLSSignatureWithResponse(ctx, rid)
			return statusOf(r, err), bodyOf(r), err
		})
	}
	if *paramExemptionID != "" && *paramExemptionAcct != "" {
		id, acct := *paramExemptionID, *paramExemptionAcct
		runCompatibilityTest(t, c1, c2, "restrictions/exemptions/"+id+"/usage/"+acct, func(c *ClientWithResponses) (int, string, error) {
			r, err := c.GetRestrictionsExemptionUsageWithResponse(ctx, id, acct)
			return statusOf(r, err), bodyOf(r), err
		})
	}
}

func runCompatibilityTest(t *testing.T, c1, c2 *ClientWithResponses, name string, call func(*ClientWithResponses) (int, string, error)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		status1, body1, err1 := call(c1)
		status2, body2, err2 := call(c2)

		if err1 != nil || err2 != nil {
			t.Errorf("request error: endpoint1=%v endpoint2=%v", err1, err2)
			return
		}
		if status1 != status2 {
			t.Errorf("status mismatch: endpoint1=%d endpoint2=%d body1=%s body2=%s", status1, status2, body1, body2)
			return
		}
		if status1/100 != 2 {
			return // both errored the same way — compatible
		}
		if body1 != body2 {
			keys1 := jsonTopLevelKeys(body1)
			keys2 := jsonTopLevelKeys(body2)
			if reflect.DeepEqual(keys1, keys2) {
				t.Logf("bodies differ but top-level JSON keys match (%v); treating as compatible", keys1)
				return
			}
			t.Errorf("body mismatch:\n%s", jsonDiff(body1, body2))
		}
	})
}

func jsonTopLevelKeys(body string) []string {
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return nil
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// statusOf extracts the HTTP status code from any *XxxResponse via reflection.
func statusOf(r any, err error) int {
	if err != nil {
		return 0
	}
	v := reflect.ValueOf(r)
	if v.IsNil() {
		return 0
	}
	m := v.MethodByName("StatusCode")
	if !m.IsValid() {
		// fall back to HTTPResponse field
		f := v.Elem().FieldByName("HTTPResponse")
		if f.IsValid() && !f.IsNil() {
			return int(f.Elem().FieldByName("StatusCode").Int())
		}
		return 0
	}
	return int(m.Call(nil)[0].Int())
}

// jsonDiff pretty-prints both JSON strings and returns an LCS-based diff,
// so only genuinely changed lines appear as +/-.
func jsonDiff(a, b string) string {
	la := prettyLines(a)
	lb := prettyLines(b)

	// Build LCS table.
	m, n := len(la), len(lb)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if la[i-1] == lb[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack to produce edit script.
	var buf strings.Builder
	var walk func(i, j int)
	walk = func(i, j int) {
		if i == 0 && j == 0 {
			return
		}
		if i > 0 && j > 0 && la[i-1] == lb[j-1] {
			walk(i-1, j-1)
			// unchanged lines omitted
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			walk(i, j-1)
			buf.WriteString("+ " + lb[j-1] + "\n")
		} else {
			walk(i-1, j)
			buf.WriteString("- " + la[i-1] + "\n")
		}
	}
	walk(m, n)
	return buf.String()
}

func prettyLines(s string) []string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return strings.Split(s, "\n")
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return strings.Split(s, "\n")
	}
	return strings.Split(string(b), "\n")
}

// bodyOf returns the response body as a deterministic JSON string.
// It parses then re-marshals so that key order is always sorted,
// making comparisons stable regardless of server serialization order.
func bodyOf(r any) string {
	if r == nil {
		return ""
	}
	v := reflect.ValueOf(r)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	body := v.FieldByName("Body")
	if !body.IsValid() {
		return ""
	}
	raw := body.Bytes()
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return string(raw) // not JSON — return as-is
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return string(raw)
	}
	return string(out)
}
