package store

import (
	"testing"
	"time"
)

// Test flow:
//  1. Table-driven: a serving row, a row waiting to settle, and a row with a settle transaction in flight.
//  2. Assert Parked is true for the last two only.
func TestARowIsParkedOnceSettlementOwnsIt(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		record DevshardRecord
		want   bool
	}{
		{"serving", DevshardRecord{Active: true}, false},
		{"waiting to settle", DevshardRecord{SettlementPending: true}, true},
		{"settle in flight", DevshardRecord{SettleTxHash: "TX"}, true},
	} {
		if got := testCase.record.Parked(); got != testCase.want {
			t.Errorf("%s: Parked() = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// Test flow:
//  1. Format a nanosecond timestamp and the zero time.
//  2. Parse each back and assert the instant survives and the zero time stays the empty string.
//  3. Assert two instants a nanosecond apart sort in time order as text.
func TestStoredTimestampsRoundTripAndSortAsText(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)
	parsed, err := ParseTime(FormatTime(at))
	if err != nil || !parsed.Equal(at) {
		t.Fatalf("ParseTime(FormatTime(at)) = %v, %v; want %v", parsed, err, at)
	}
	if FormatTime(time.Time{}) != "" {
		t.Fatalf("FormatTime(zero) = %q, want the empty string", FormatTime(time.Time{}))
	}
	if zero, err := ParseTime(""); err != nil || !zero.IsZero() {
		t.Fatalf("ParseTime(empty) = %v, %v; want the zero time", zero, err)
	}
	if FormatTime(at) >= FormatTime(at.Add(time.Nanosecond)) {
		t.Fatal("timestamps a nanosecond apart do not sort in time order as text")
	}
}

// Test flow:
//  1. Stamp a settle hash at a known instant with sub-second precision.
//  2. Assert the stamp parses back to that instant truncated to the second.
//  3. Assert an empty hash stamps as empty and an unparsable stamp reads as the zero time.
func TestTheSettleStampKeepsSecondsAndClearsWithTheHash(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 987654321, time.UTC)
	if got := ParseSettleStamp(SettleStamp("TX", at)); !got.Equal(at.Truncate(time.Second)) {
		t.Fatalf("ParseSettleStamp(SettleStamp()) = %v, want %v", got, at.Truncate(time.Second))
	}
	if SettleStamp("", at) != "" {
		t.Fatalf("SettleStamp(empty hash) = %q, want the empty string", SettleStamp("", at))
	}
	if got := ParseSettleStamp("not a time"); !got.IsZero() {
		t.Fatalf("ParseSettleStamp(garbage) = %v, want the zero time", got)
	}
}
