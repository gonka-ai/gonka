package pocstream

import "testing"

func TestIsVersionStreamCapable(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"", false},
		{"garbage", false},
		{"3.0.13", false},
		{"v3.0.13", false},
		{"3.0.13-post6", false},
		{MinStreamCapableVersion, true},
		{"v" + MinStreamCapableVersion, true},
		{MinStreamCapableVersion + "-post1", true},
		{"3.0.15", true},
		{"3.1.0", true},
		{"4.0.0", true},
		{"3.0", false},
		{"3.1", true},
	}
	for _, tc := range cases {
		if got := IsVersionStreamCapable(tc.version); got != tc.want {
			t.Errorf("IsVersionStreamCapable(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}
