package proxy

import "testing"

func TestCompareVersionNames(t *testing.T) {
	tests := []struct {
		a, b string
		want int // sign of compareVersionNames(a,b)
	}{
		{"v2", "v10", -1},
		{"v10", "v2", 1},
		{"v0.2.9", "v0.2.11", -1},
		{"v0.2.11", "v0.2.9", 1},
		{"v1", "v2", -1},
		{"v2", "v2", 0},
		{"2", "v10", -1},
		{"v0.2.11", "v0.2.11", 0},
		{"dev", "v2", -1}, // non-numeric sorts below numeric
		{"v2", "dev", 1},
		{"alpha", "beta", -1},
		{"v0.2", "v0.2.0", -1}, // fewer segments sorts older when prefix equal
		{"v0.2.0", "v0.2", 1},
	}
	for _, tt := range tests {
		got := compareVersionNames(tt.a, tt.b)
		if sign(got) != tt.want {
			t.Errorf("compareVersionNames(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestPrimaryVersion(t *testing.T) {
	if got := primaryVersion([]string{"v2", "v10", "v1"}); got != "v10" {
		t.Fatalf("primaryVersion(v1,v2,v10) = %q, want v10", got)
	}
	if got := primaryVersion([]string{"v0.2.9", "v0.2.11", "v0.2.10"}); got != "v0.2.11" {
		t.Fatalf("primaryVersion(semver-ish) = %q, want v0.2.11", got)
	}
	if got := primaryVersion([]string{"v2", "dev"}); got != "v2" {
		t.Fatalf("primaryVersion(v2,dev) = %q, want v2", got)
	}
}

func TestSortedVersions_NumericOrder(t *testing.T) {
	got := sortedVersions(RouteTable{
		"v10": NewTarget("a"),
		"v2":  NewTarget("b"),
		"v1":  NewTarget("c"),
	})
	want := []string{"v1", "v2", "v10"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedVersions = %v, want %v", got, want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}
