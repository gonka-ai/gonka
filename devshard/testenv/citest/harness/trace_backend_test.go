package harness

import "testing"

func TestNormalizeTraceID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"832f3e0ba491680da88f15f9497f379", "0832f3e0ba491680da88f15f9497f379"},
		{"0832f3e0ba491680da88f15f9497f379", "0832f3e0ba491680da88f15f9497f379"},
		{"0x832f3e0ba491680da88f15f9497f379", "0832f3e0ba491680da88f15f9497f379"},
		{"ABCD", "0000000000000000000000000000abcd"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeTraceID(tc.in); got != tc.want {
			t.Fatalf("NormalizeTraceID(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
