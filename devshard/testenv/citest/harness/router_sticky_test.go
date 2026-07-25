package harness

import "testing"

func TestOtherStickyUpstream(t *testing.T) {
	if got := OtherStickyUpstream("a:8080", "a:8080", "b:8080"); got != "b:8080" {
		t.Fatalf("got %q want b:8080", got)
	}
	if got := OtherStickyUpstream("b:8080", "a:8080", "b:8080"); got != "a:8080" {
		t.Fatalf("got %q want a:8080", got)
	}
	if got := OtherStickyUpstream("c:8080", "a:8080", "b:8080"); got != "" {
		t.Fatalf("got %q want empty", got)
	}
}
