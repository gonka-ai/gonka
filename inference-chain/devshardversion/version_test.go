package devshardversion

import "testing"

func TestUsesAssignedCount(t *testing.T) {
	for _, version := range []string{"v7", "7", "V7.1", "v7r2", " v8 ", "12.0.1"} {
		if !UsesAssignedCount(version) {
			t.Errorf("%q should use assigned counts", version)
		}
	}
	for _, version := range []string{"", "dev", "mainnet-canary", "v6", "v4.1r5", "v0.2.18-devshard-v7", "v", "999999999999999999999"} {
		if UsesAssignedCount(version) {
			t.Errorf("%q should retain legacy accounting", version)
		}
	}
}
