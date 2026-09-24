package types

// DevshardValidationRateForCreate returns the validation_rate snapshotted onto a
// DevshardEscrow at create. Governance zero falls back to the compiled default.
func DevshardValidationRateForCreate(ep *DevshardEscrowParams) uint32 {
	if ep == nil || ep.ValidationRate == 0 {
		return DefaultDevshardValidationRate
	}
	return ep.ValidationRate
}

// DevshardVoteThresholdFactorForCreate returns the vote_threshold_factor
// snapshotted onto a DevshardEscrow at create. Governance zero falls back to the
// compiled default. Devshard hosts derive VoteThreshold from the escrow row only,
// so a factor left off the row would make the governance value dead.
func DevshardVoteThresholdFactorForCreate(ep *DevshardEscrowParams) uint32 {
	if ep == nil || ep.VoteThresholdFactor == 0 {
		return DefaultDevshardVoteThresholdFactor
	}
	return ep.VoteThresholdFactor
}
