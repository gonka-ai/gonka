package storage

import "testing"

func TestMemory_CreateSession_GetSessionMeta(t *testing.T) {
	runCreateSession_GetSessionMeta(t, NewMemory())
}

func TestMemory_CreateSession_Idempotent(t *testing.T) {
	runCreateSession_Idempotent(t, NewMemory())
}

func TestMemory_AppendDiff_GetDiffs(t *testing.T) {
	runAppendDiff_GetDiffs(t, NewMemory())
}

func TestMemory_GetSignatures(t *testing.T) {
	runGetSignatures(t, NewMemory())
}

func TestMemory_MarkFinalized_LastFinalized(t *testing.T) {
	runMarkFinalized_LastFinalized(t, NewMemory())
}

func TestMemory_AddSignature(t *testing.T) {
	runAddSignature(t, NewMemory())
}

func TestMemory_WarmKeyDelta(t *testing.T) {
	runWarmKeyDelta(t, NewMemory())
}

func TestMemory_MarkSettled(t *testing.T) {
	runMarkSettled(t, NewMemory())
}

func TestMemory_ListActiveSessions(t *testing.T) {
	runListActiveSessions(t, NewMemory())
}

func TestMemory_DuplicateNonce(t *testing.T) {
	store := NewMemory()
	err := store.CreateSession(defaultParams())
	if err != nil {
		t.Fatal(err)
	}

	err = store.AppendDiff("escrow-1", makeDiffRecord(1))
	if err != nil {
		t.Fatal(err)
	}

	err = store.AppendDiff("escrow-1", makeDiffRecord(1))
	if err == nil {
		t.Fatal("expected error on duplicate nonce")
	}
}
