//go:build testenvci

package harness

import "testing"

func TestParseDevsharddHeightAtLatestNonceFromMetricsText(t *testing.T) {
	text := `# HELP devshardd_height_at_latest_nonce x
# TYPE devshardd_height_at_latest_nonce gauge
devshardd_height_at_latest_nonce 107
`
	v, err := parseDevsharddHeightAtLatestNonceFromMetricsText(text)
	if err != nil {
		t.Fatal(err)
	}
	if v != 107 {
		t.Fatalf("got %v want 107", v)
	}
}

func TestParseDevsharddHeightAtLatestNonceFromMetricsText_Labeled(t *testing.T) {
	text := `devshardd_height_at_latest_nonce{instance="x"} 42`
	v, err := parseDevsharddHeightAtLatestNonceFromMetricsText(text)
	if err != nil {
		t.Fatal(err)
	}
	if v != 42 {
		t.Fatalf("got %v want 42", v)
	}
}
