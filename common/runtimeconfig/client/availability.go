package client

// AvailabilitySink receives devshard_requests_enabled updates from the client loop.
type AvailabilitySink interface {
	Record(enabled bool, timestamp int64, epochID uint64)
}

// AvailabilityView is the process-local availability snapshot derived from the
// latest runtime-config apply.
type AvailabilityView struct {
	Enabled bool
	Time    int64
	EpochID uint64
}
