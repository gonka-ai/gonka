package transport

import "devshard/transport/rpcpb/rpcpbconnect"

// Per-RPC weights against the peer (MessagesPerMin) budget.
// Effective max if a method is the only traffic is floor(budget / weight).
const (
	RPCWeightGetSignatures = 1
	RPCWeightGossipNonce   = 1
	RPCWeightGossipTxs     = 2
	RPCWeightSeed          = 2
	RPCWeightRepair        = 2
	RPCWeightVerify        = 2
	RPCWeightChallenge     = 2
	RPCWeightChat          = 10
	RPCWeightGetMempool    = 6
	RPCWeightGetPayload    = 6
	RPCWeightGetDiffs      = 60
)

// RPCProcedureWeight is the peer-bucket cost of an authenticated procedure.
// Watch is 0 (stream cap). Attach is 0 (process floor in the child;
// per-IP bounds live on versiond / Phase 6 proxy). Unknown authenticated
// RPCs cost 1.
func RPCProcedureWeight(procedure string) int {
	switch procedure {
	case rpcpbconnect.SessionServiceChatProcedure:
		return RPCWeightChat
	case rpcpbconnect.SessionServiceSeedHeightSyncProcedure:
		return RPCWeightSeed
	case rpcpbconnect.SessionServiceRepairHeightSyncProcedure:
		return RPCWeightRepair
	case rpcpbconnect.SessionServiceVerifyTimeoutProcedure,
		rpcpbconnect.SessionServiceVerifyErrorMissProcedure:
		return RPCWeightVerify
	case rpcpbconnect.SessionServiceChallengeReceiptProcedure:
		return RPCWeightChallenge
	case rpcpbconnect.GossipServiceTxsProcedure:
		return RPCWeightGossipTxs
	case rpcpbconnect.SessionServiceGetSignaturesProcedure,
		rpcpbconnect.GossipServiceNonceProcedure:
		return RPCWeightGetSignatures
	case rpcpbconnect.SessionServiceGetMempoolProcedure:
		return RPCWeightGetMempool
	case rpcpbconnect.PayloadServiceGetPayloadProcedure:
		return RPCWeightGetPayload
	case rpcpbconnect.SessionServiceGetDiffsProcedure:
		return RPCWeightGetDiffs
	case rpcpbconnect.PeerAuthServiceWatchProcedure,
		rpcpbconnect.PeerAuthServiceAttachProcedure:
		return 0
	default:
		return 1
	}
}
