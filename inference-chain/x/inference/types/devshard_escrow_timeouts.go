package types

// DevshardRefusalTimeoutForCreate snapshots the configured refusal deadline
// onto a newly created escrow. Zero-valued legacy params use the canonical
// default so every newly created escrow has an explicit timeout contract.
func DevshardRefusalTimeoutForCreate(ep *DevshardEscrowParams) int64 {
	if ep == nil || ep.RefusalTimeout <= 0 {
		return DefaultDevshardRefusalTimeout
	}
	return ep.RefusalTimeout
}

// DevshardExecutionTimeoutForCreate snapshots the configured execution
// deadline onto a newly created escrow. Zero-valued legacy params use the
// canonical default.
func DevshardExecutionTimeoutForCreate(ep *DevshardEscrowParams) int64 {
	if ep == nil || ep.ExecutionTimeout <= 0 {
		return DefaultDevshardExecutionTimeout
	}
	return ep.ExecutionTimeout
}
