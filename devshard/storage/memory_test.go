package storage

import "testing"

func TestMemory_CreateSession_GetSessionMeta(t *testing.T) {
	runCreateSessionGetSessionMeta(t, NewMemory())
}

func TestMemory_CreateSession_Idempotent(t *testing.T) {
	runCreateSessionIdempotent(t, NewMemory())
}

func TestMemory_CreateSession_ConflictingEpoch(t *testing.T) {
	runCreateSessionConflictingEpoch(t, NewMemory())
}

func TestMemory_CreateSession_ConflictingVersion(t *testing.T) {
	runCreateSessionConflictingVersion(t, NewMemory())
}

func TestMemory_CreateSession_EmptyVersionRejected(t *testing.T) {
	runCreateSessionEmptyVersionRejected(t, NewMemory())
}

func TestMemory_AppendDiff_GetDiffs(t *testing.T) {
	runAppendDiffGetDiffs(t, NewMemory())
}

func TestMemory_GetSignatures(t *testing.T) {
	runGetSignatures(t, NewMemory())
}

func TestMemory_MarkFinalized_LastFinalized(t *testing.T) {
	runMarkFinalizedLastFinalized(t, NewMemory())
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
	runPruneEpochRemovesOnlyTarget(t, NewMemory())
}

func TestMemory_PruneEpoch_Idempotent(t *testing.T) {
	runPruneEpochIdempotent(t, NewMemory())
}

func TestMemory_PruneEpoch_WriteAfter(t *testing.T) {
	runPruneEpochWriteAfter(t, NewMemory())
}

func TestMemory_DuplicateNonce_IdenticalReplayOK(t *testing.T) {
	// Identical same-nonce replay is idempotent (HA); see AppendDiff docs.
	runAppendDiffIdempotentReplay(t, NewMemory())
}
