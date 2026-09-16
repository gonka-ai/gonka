package devshard

import (
	"errors"
	"testing"
)

func TestParseEscrowIDAcceptsCanonicalOnly(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want uint64
	}{
		{"1", 1},
		{"123", 123},
		{"18446744073709551615", 18446744073709551615},
	} {
		got, err := ParseEscrowID(tc.raw)
		if err != nil {
			t.Fatalf("ParseEscrowID(%q) returned error: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ParseEscrowID(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestParseEscrowIDRejectsNonCanonical(t *testing.T) {
	for _, raw := range []string{
		"",
		"0",
		"00",
		"0123",
		"00123",
		"000000000000000000000123",
		"+123",
		"-123",
		" 123",
		"123 ",
		"1_2",
		"0x7b",
		"123abc",
		"12.3",
		"18446744073709551616",
	} {
		if _, err := ParseEscrowID(raw); err == nil {
			t.Fatalf("ParseEscrowID(%q) accepted a non-canonical escrow id", raw)
		} else if !errors.Is(err, ErrInvalidEscrowID) {
			t.Fatalf("ParseEscrowID(%q) error = %v, want ErrInvalidEscrowID", raw, err)
		}
	}
}

func TestValidateEscrowIDMatchesParse(t *testing.T) {
	if err := ValidateEscrowID("42"); err != nil {
		t.Fatalf("ValidateEscrowID(\"42\") returned error: %v", err)
	}
	if err := ValidateEscrowID("042"); err == nil {
		t.Fatal("ValidateEscrowID(\"042\") accepted a leading-zero alias")
	}
}
