package user

import (
	"bytes"
	"testing"
)

// Test flow:
//  1. Hold two signatures for a nonce in a session, then read them through SignaturesAt.
//  2. Overwrite the returned bytes.
//  3. Assert the session's own signatures are untouched and both slots were returned: the caller holds a copy, not the live map.
func TestSignaturesAtReturnsACopyOfTheHeldSignatures(t *testing.T) {
	session := &Session{signatures: map[uint64]map[uint32][]byte{9: {0: []byte("sig-0"), 2: []byte("sig-2")}}}

	copied := session.SignaturesAt(9)
	copied[0][0] = 'X'
	delete(copied, 2)

	if len(session.signatures[9]) != 2 || !bytes.Equal(session.signatures[9][0], []byte("sig-0")) {
		t.Fatalf("session signatures = %q, want them untouched by the caller's edits", session.signatures[9])
	}
	if again := session.SignaturesAt(9); len(again) != 2 || !bytes.Equal(again[2], []byte("sig-2")) {
		t.Fatalf("SignaturesAt(9) = %q, want both held slots", again)
	}
}
