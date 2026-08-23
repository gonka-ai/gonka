package types

// DevshardValidationRateForCreate returns the validation_rate snapshotted onto a
// DevshardEscrow at create. Governance zero falls back to the compiled default.
func DevshardValidationRateForCreate(ep *DevshardEscrowParams) uint32 {
	if ep == nil || ep.ValidationRate == 0 {
		return DefaultDevshardValidationRate
	}
	return ep.ValidationRate
}
