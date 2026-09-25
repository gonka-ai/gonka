package storage

// PendingFinishStore keeps an executor's own signed MsgFinishInference
// (proto bytes) until a diff sequences it.
//
// The host mempool lives only in process memory. A Finish the escrow creator
// has not sequenced yet exists nowhere else, so without this store a restart
// of the executor (binary swap, crash, HA failover) leaves it with an empty
// mempool: the gateway can no longer collect the late Finish, peers asked to
// vote on an EXECUTION timeout see no Finish and accept, and the executor is
// timed out (no payment, Missed++) for an inference it served.
//
// Rows belong to the session's epoch and are pruned with it. Readers must
// ignore rows whose inference is no longer pending or started.
type PendingFinishStore interface {
	PutPendingFinish(escrowID string, inferenceID uint64, finishProto []byte) error
	PendingFinishes(escrowID string) (map[uint64][]byte, error)
}
