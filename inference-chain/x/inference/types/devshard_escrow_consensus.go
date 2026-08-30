package types

// DevshardValidationRateForCreate returns the validation_rate snapshotted onto a
// DevshardEscrow at create. Governance zero falls back to the compiled default.
func DevshardValidationRateForCreate(ep *DevshardEscrowParams) uint32 {
	if ep == nil || ep.ValidationRate == 0 {
		return DefaultDevshardValidationRate
	}
	return ep.ValidationRate
}

func DevshardLogprobsModeForCreate(vp *ValidationParams) string {
	if vp == nil {
		return DefaultLogprobsMode
	}
	switch vp.LogprobsMode {
	case LogprobsModeProcessed, LogprobsModeRaw:
		return vp.LogprobsMode
	default:
		return DefaultLogprobsMode
	}
}
