package vo

import "fmt"

type ReleaseReason string

const (
	ReleaseFailedPrepare ReleaseReason = "failed_prepare"
	ReleaseFailedRun     ReleaseReason = "failed_run"
	ReleaseUnreachable   ReleaseReason = "unreachable"
	ReleaseOperatorAbort ReleaseReason = "operator_abort"
	ReleaseManualKick    ReleaseReason = "manual_kick"
)

func ReleaseRequestID(shardID ShardID, node NodeRef, reason ReleaseReason) string {
	return fmt.Sprintf("release/%s/%s/%s/%s", shardID, node.Participant, node.NodeID, reason)
}
