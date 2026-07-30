package devshardobs

import "testing"

func TestCompareVersionNames(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v2", "v10", -1},
		{"v10", "v2", 1},
		{"v0.2.9", "v0.2.11", -1},
		{"v0.2.11", "v0.2.9", 1},
		{"dev", "v2", -1},
		{"v2", "dev", 1},
	}
	for _, tt := range tests {
		got := CompareVersionNames(tt.a, tt.b)
		if sign(got) != tt.want {
			t.Errorf("CompareVersionNames(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestPrimaryVersion(t *testing.T) {
	if got := PrimaryVersion([]string{"v2", "v10", "v1"}); got != "v10" {
		t.Fatalf("PrimaryVersion = %q, want v10", got)
	}
	if got := PrimaryVersion([]string{"v0.2.9", "v0.2.11"}); got != "v0.2.11" {
		t.Fatalf("PrimaryVersion = %q, want v0.2.11", got)
	}
}

func TestSortVersions(t *testing.T) {
	got := SortVersions([]string{"v10", "v2", "v1"})
	want := []string{"v1", "v2", "v10"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SortVersions = %v, want %v", got, want)
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
