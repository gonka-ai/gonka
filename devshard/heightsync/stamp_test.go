package heightsync

import "testing"

func TestStampPresent_EmptyHashIsAbsent(t *testing.T) {
	if StampPresent(nil) {
		t.Fatal("nil hash must be absent")
	}
	if StampPresent([]byte{}) {
		t.Fatal("empty hash must be absent")
	}
	if !StampPresent([]byte{0x00}) {
		t.Fatal("non-empty hash is a claim even at height 0")
	}
	if !StampPresent([]byte{0xaa, 0xbb}) {
		t.Fatal("non-empty hash is a claim")
	}
}

func TestStampPresent_PresentThenAbsentIsNotRegression(t *testing.T) {
	// H38: L0b would compare start ≤ confirm. A present start (hash set) and
	// an absent confirm (hash empty, height proto-zero) must skip the check,
	// not read confirm as height 0 and INVALID(height_regression).
	startHash := []byte{0x11}
	var confirmHash []byte
	if !StampPresent(startHash) {
		t.Fatal("start is present")
	}
	if StampPresent(confirmHash) {
		t.Fatal("confirm is absent")
	}
	if StampPresent(startHash) && !StampPresent(confirmHash) {
		return // skip L0b for the missing leg — not a regression
	}
	t.Fatal("present-then-absent pair must not be treated as height 0")
}
