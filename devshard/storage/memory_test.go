package storage

import "testing"

func TestMemory_CreateSession_GetSessionMeta(t *testing.T) {
	runCreateSession_GetSessionMeta(t, NewMemory())
}

func TestMemory_CreateSession_Idempotent(t *testing.T) {
	runCreateSession_Idempotent(t, NewMemory())
}

func TestMemory_CreateSession_ConflictingEpoch(t *testing.T) {
	runCreateSession_ConflictingEpoch(t, NewMemory())
}

func TestMemory_CreateSession_ConflictingVersion(t *testing.T) {
	runCreateSession_ConflictingVersion(t, NewMemory())
}

func TestMemory_CreateSession_EmptyVersionRejected(t *testing.T) {
	runCreateSession_EmptyVersionRejected(t, NewMemory())
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

func TestMemory_SaveLoadSnapshot(t *testing.T) {
	runSaveLoadSnapshot(t, NewMemory())
}

func TestMemory_SealedInferenceLifecycle(t *testing.T) {
	runSealedInferenceLifecycle(t, NewMemory())
}

func TestMemory_SealedInferenceBatchInsert(t *testing.T) {
	runSealedInferenceBatchInsert(t, NewMemory())
}

func TestMemory_SealedInferenceBulkInsert(t *testing.T) {
	runSealedInferenceBulkInsert(t, NewMemory())
}

func TestMemory_ValidationObsBatchDrain(t *testing.T) {
	runValidationObsBatchDrain(t, NewMemory())
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

func TestMemory_PruneEpoch_RemovesOnlyTarget(t *testing.T) {
	runPruneEpoch_RemovesOnlyTarget(t, NewMemory())
}

func TestMemory_PruneEpoch_Idempotent(t *testing.T) {
	runPruneEpoch_Idempotent(t, NewMemory())
}

func TestMemory_PruneEpoch_WriteAfter(t *testing.T) {
	runPruneEpoch_WriteAfter(t, NewMemory())
}

func TestMemory_DuplicateNonce_IdenticalReplayOK(t *testing.T) {
	// Identical same-nonce replay is idempotent (HA); see AppendDiff docs.
	runAppendDiff_IdempotentReplay(t, NewMemory())
}
