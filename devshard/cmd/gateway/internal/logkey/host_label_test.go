package logkey

import "testing"

// Test flow:
//  1. Call ShortHost with a full-length address.
//  2. Assert it returns the address's last eight characters.
//  3. Call ShortHost with a short string and assert it is returned unchanged.
//  4. Call ShortHost with an empty string and assert it returns empty.
func TestAHostIsNamedByItsTail(t *testing.T) {
	if got := ShortHost("gonka1gvpv7vhk5gyxhmf9u8sc8pw5j8fr6lzalyrmkx"); got != "zalyrmkx" {
		t.Fatalf("ShortHost() = %q, want the last eight characters", got)
	}
	if got := ShortHost("short"); got != "short" {
		t.Fatalf("ShortHost() = %q, want a short address left alone", got)
	}
	if got := ShortHost(""); got != "" {
		t.Fatalf("ShortHost() = %q, want nothing for an unknown host", got)
	}
}
