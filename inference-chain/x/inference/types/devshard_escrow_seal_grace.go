package types

// DevshardInferenceSealGraceNoncesForCreate returns the seal-grace nonce gate
// snapshotted onto a DevshardEscrow at create. Governance may set an absolute
// default; zero falls back to group-size-derived canonical default.
func DevshardInferenceSealGraceNoncesForCreate(ep *DevshardEscrowParams, slotCount uint32) uint32 {
	if ep == nil {
		return DefaultDevshardInferenceSealGraceNonces(slotCount)
	}
	if ep.DefaultInferenceSealGraceNonces > 0 {
		return ep.DefaultInferenceSealGraceNonces
	}
	return DefaultDevshardInferenceSealGraceNonces(slotCount)
}

// DevshardInferenceSealGraceSecondsForCreate returns the wall-clock seal grace
// snapshotted onto a DevshardEscrow at create.
func DevshardInferenceSealGraceSecondsForCreate(ep *DevshardEscrowParams) uint32 {
	if ep == nil {
		return DefaultDevshardInferenceSealGraceSeconds
	}
	if ep.DefaultInferenceSealGraceSeconds > 0 {
		return ep.DefaultInferenceSealGraceSeconds
	}
	return DefaultDevshardInferenceSealGraceSeconds
}

// DevshardAutoSealEveryNNoncesForCreate returns the auto-seal interval
// snapshotted onto a DevshardEscrow at create.
func DevshardAutoSealEveryNNoncesForCreate(ep *DevshardEscrowParams) uint32 {
	if ep == nil {
		return DefaultDevshardAutoSealEveryNNonces
	}
	if ep.DefaultAutoSealEveryNNonces > 0 {
		return ep.DefaultAutoSealEveryNNonces
	}
	return DefaultDevshardAutoSealEveryNNonces
}
