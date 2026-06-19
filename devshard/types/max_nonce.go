package types

// FinalizeNonceReserve is the number of nonces a session may consume after the
// last active-phase nonce before settlement: one MsgFinalizeRound diff plus
// groupSize rounds in user.Finalize phase A and one drain round (phase A+1),
// matching state-machine settlement at FinalizeNonce+groupSize.
func FinalizeNonceReserve(groupSize int) uint64 {
	if groupSize < 0 {
		groupSize = 0
	}
	return uint64(groupSize) + 1
}

// MaxActiveNonce is the highest diff nonce allowed while the session is still in
// the active phase, so that finalization and settlement can complete without
// exceeding chain max_nonce (see keeper.VerifyDevshardSettlement).
func MaxActiveNonce(maxNonce uint32, groupSize int) uint64 {
	if maxNonce == 0 {
		return ^uint64(0)
	}
	reserve := FinalizeNonceReserve(groupSize)
	if uint64(maxNonce) <= reserve {
		return 0
	}
	return uint64(maxNonce) - reserve
}

// DiffHasActiveCompletionWork reports whether a diff carries new completion-
// phase work that should be capped before finalization (start/timeout/validation).
// Finish, finalize, and mempool housekeeping txs are not capped here.
func DiffHasActiveCompletionWork(diff Diff) bool {
	for _, tx := range diff.Txs {
		if tx.GetStartInference() != nil ||
			tx.GetTimeoutInference() != nil ||
			tx.GetValidation() != nil ||
			tx.GetValidationVote() != nil {
			return true
		}
	}
	return false
}
