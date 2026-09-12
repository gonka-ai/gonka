package vo

type ReleaseReason string

const (
	ReleaseFailedPrepare ReleaseReason = "failed_prepare"
	ReleaseFailedRun     ReleaseReason = "failed_run"
	ReleaseUnreachable   ReleaseReason = "unreachable"
	ReleaseOperatorAbort ReleaseReason = "operator_abort"
)
