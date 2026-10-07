package transport

import (
	"devshard/heightsync"
	"devshard/transport/rpcpb"
	"devshard/types"
)

// DiffToProto is the Connect wire form of a domain Diff. Same field mapping
// as DiffToJSON (Nonce, marshaled DiffContent Txs, UserSig, PostStateRoot)
// without an intermediate DiffJSON.
func DiffToProto(d types.Diff) (*rpcpb.Diff, error) {
	txsBytes, err := marshalDiffWireTxs(d)
	if err != nil {
		return nil, err
	}
	return &rpcpb.Diff{
		Nonce:         d.Nonce,
		Txs:           txsBytes,
		UserSig:       d.UserSig,
		PostStateRoot: d.PostStateRoot,
	}, nil
}

// DiffJSONToProto is the Connect wire form of DiffJSON.
func DiffJSONToProto(d DiffJSON) *rpcpb.Diff {
	return &rpcpb.Diff{
		Nonce:         d.Nonce,
		Txs:           d.Txs,
		UserSig:       d.UserSig,
		PostStateRoot: d.PostStateRoot,
	}
}

// DiffJSONFromProto reconstructs DiffJSON. A nil proto is a zero value.
func DiffJSONFromProto(d *rpcpb.Diff) DiffJSON {
	if d == nil {
		return DiffJSON{}
	}
	return DiffJSON{
		Nonce:         d.GetNonce(),
		Txs:           d.GetTxs(),
		UserSig:       d.GetUserSig(),
		PostStateRoot: d.GetPostStateRoot(),
	}
}

func diffsJSONToProto(in []DiffJSON) []*rpcpb.Diff {
	if len(in) == 0 {
		return nil
	}
	out := make([]*rpcpb.Diff, len(in))
	for i, d := range in {
		out[i] = DiffJSONToProto(d)
	}
	return out
}

func diffsJSONFromProto(in []*rpcpb.Diff) []DiffJSON {
	if len(in) == 0 {
		return nil
	}
	out := make([]DiffJSON, len(in))
	for i, d := range in {
		out[i] = DiffJSONFromProto(d)
	}
	return out
}

// PayloadJSONToProto is the Connect wire form of PayloadJSON.
func PayloadJSONToProto(p *PayloadJSON) *rpcpb.Payload {
	if p == nil {
		return nil
	}
	return &rpcpb.Payload{
		Prompt:      p.Prompt,
		Model:       p.Model,
		InputLength: p.InputLength,
		MaxTokens:   p.MaxTokens,
		StartedAt:   p.StartedAt,
	}
}

// PayloadJSONFromProto reconstructs PayloadJSON. A nil proto is nil.
func PayloadJSONFromProto(p *rpcpb.Payload) *PayloadJSON {
	if p == nil {
		return nil
	}
	return &PayloadJSON{
		Prompt:      p.GetPrompt(),
		Model:       p.GetModel(),
		InputLength: p.GetInputLength(),
		MaxTokens:   p.GetMaxTokens(),
		StartedAt:   p.GetStartedAt(),
	}
}

// HeightSyncSectionToProto is the Connect wire form of a height-sync section.
func HeightSyncSectionToProto(s *heightsync.HeightSyncSection) *rpcpb.HeightSyncSection {
	if s == nil {
		return nil
	}
	return &rpcpb.HeightSyncSection{
		ChainId:                   s.ChainID,
		ProofType:                 s.ProofType,
		MainnetHeight:             s.MainnetHeight,
		MainnetBlockHashHex:       s.MainnetBlockHashHex,
		TimestampUnixMs:           s.TimestampUnixMs,
		Direction:                 s.Direction,
		OriginatorSenderId:        s.OriginatorSenderID,
		OriginatorTimestampUnixMs: s.OriginatorTimestampMs,
		SenderSignature:           s.SenderSignature,
		TipStaleAfterMs:           s.TipStaleAfterMs,
	}
}

// HeightSyncSectionFromProto reconstructs a height-sync section.
func HeightSyncSectionFromProto(s *rpcpb.HeightSyncSection) *heightsync.HeightSyncSection {
	if s == nil {
		return nil
	}
	return &heightsync.HeightSyncSection{
		ChainID:               s.GetChainId(),
		ProofType:             s.GetProofType(),
		MainnetHeight:         s.GetMainnetHeight(),
		MainnetBlockHashHex:   s.GetMainnetBlockHashHex(),
		TimestampUnixMs:       s.GetTimestampUnixMs(),
		Direction:             s.GetDirection(),
		OriginatorSenderID:    s.GetOriginatorSenderId(),
		OriginatorTimestampMs: s.GetOriginatorTimestampUnixMs(),
		SenderSignature:       s.GetSenderSignature(),
		TipStaleAfterMs:       s.GetTipStaleAfterMs(),
	}
}

// RepairRequestToProto is the Connect wire form of a repair probe.
func RepairRequestToProto(r *heightsync.RepairRequest) *rpcpb.RepairRequest {
	if r == nil {
		return nil
	}
	return &rpcpb.RepairRequest{
		TurnStart:         r.TurnStart,
		RefNonce:          r.RefNonce,
		RequesterSlot:     r.RequesterSlot,
		ObservedHeight:    r.ObservedHeight,
		ObservedBlockHash: r.ObservedBlockHash,
		RequesterSig:      r.RequesterSig,
	}
}

// RepairRequestFromProto reconstructs a repair probe.
func RepairRequestFromProto(r *rpcpb.RepairRequest) *heightsync.RepairRequest {
	if r == nil {
		return nil
	}
	return &heightsync.RepairRequest{
		TurnStart:         r.GetTurnStart(),
		RefNonce:          r.GetRefNonce(),
		RequesterSlot:     r.GetRequesterSlot(),
		ObservedHeight:    r.GetObservedHeight(),
		ObservedBlockHash: r.GetObservedBlockHash(),
		RequesterSig:      r.GetRequesterSig(),
	}
}

// RepairResponseToProto is the Connect wire form of a repair response.
func RepairResponseToProto(r *heightsync.RepairResponse) *rpcpb.RepairResponse {
	if r == nil {
		return nil
	}
	return &rpcpb.RepairResponse{
		Outcome:           r.Outcome,
		ObservedHeight:    r.ObservedHeight,
		ObservedBlockHash: r.ObservedBlockHash,
		SyncState:         r.SyncState,
		Ack:               r.Ack,
		ResponderSig:      r.ResponderSig,
	}
}

// RepairResponseFromProto reconstructs a repair response.
func RepairResponseFromProto(r *rpcpb.RepairResponse) *heightsync.RepairResponse {
	if r == nil {
		return nil
	}
	return &heightsync.RepairResponse{
		Outcome:           r.GetOutcome(),
		ObservedHeight:    r.GetObservedHeight(),
		ObservedBlockHash: r.GetObservedBlockHash(),
		SyncState:         r.GetSyncState(),
		Ack:               r.GetAck(),
		ResponderSig:      r.GetResponderSig(),
	}
}

// VerifyTimeoutRequestToProto is the Connect inner payload for VerifyTimeout.
func VerifyTimeoutRequestToProto(r VerifyTimeoutRequest) *rpcpb.VerifyTimeoutRequest {
	return &rpcpb.VerifyTimeoutRequest{
		InferenceId: r.InferenceID,
		Reason:      r.Reason,
		Payload:     PayloadJSONToProto(r.Payload),
		Diffs:       diffsJSONToProto(r.Diffs),
	}
}

// VerifyTimeoutRequestFromProto reconstructs the JSON-shaped request.
func VerifyTimeoutRequestFromProto(r *rpcpb.VerifyTimeoutRequest) VerifyTimeoutRequest {
	if r == nil {
		return VerifyTimeoutRequest{}
	}
	return VerifyTimeoutRequest{
		InferenceID: r.GetInferenceId(),
		Reason:      r.GetReason(),
		Payload:     PayloadJSONFromProto(r.GetPayload()),
		Diffs:       diffsJSONFromProto(r.GetDiffs()),
	}
}

// VerifyTimeoutResponseToProto is the Connect form of VerifyTimeoutResponse.
func VerifyTimeoutResponseToProto(r *VerifyTimeoutResponse) *rpcpb.VerifyTimeoutResponse {
	if r == nil {
		return nil
	}
	return &rpcpb.VerifyTimeoutResponse{
		Accept:      r.Accept,
		Signature:   r.Signature,
		VoterSlot:   r.VoterSlot,
		Mempool:     r.Mempool,
		RejectCause: r.RejectCause,
	}
}

// VerifyTimeoutResponseFromProto reconstructs VerifyTimeoutResponse.
func VerifyTimeoutResponseFromProto(r *rpcpb.VerifyTimeoutResponse) *VerifyTimeoutResponse {
	if r == nil {
		return nil
	}
	return &VerifyTimeoutResponse{
		Accept:      r.GetAccept(),
		Signature:   r.GetSignature(),
		VoterSlot:   r.GetVoterSlot(),
		Mempool:     r.GetMempool(),
		RejectCause: r.GetRejectCause(),
	}
}

// VerifyErrorMissRequestToProto is the Connect inner payload for VerifyErrorMiss.
func VerifyErrorMissRequestToProto(r VerifyErrorMissRequest) *rpcpb.VerifyErrorMissRequest {
	return &rpcpb.VerifyErrorMissRequest{
		InferenceId:     r.InferenceID,
		Diffs:           diffsJSONToProto(r.Diffs),
		FinishTx:        r.FinishTx,
		ResponsePayload: r.ResponsePayload,
	}
}

// VerifyErrorMissRequestFromProto reconstructs the JSON-shaped request.
func VerifyErrorMissRequestFromProto(r *rpcpb.VerifyErrorMissRequest) VerifyErrorMissRequest {
	if r == nil {
		return VerifyErrorMissRequest{}
	}
	return VerifyErrorMissRequest{
		InferenceID:     r.GetInferenceId(),
		Diffs:           diffsJSONFromProto(r.GetDiffs()),
		FinishTx:        r.GetFinishTx(),
		ResponsePayload: r.GetResponsePayload(),
	}
}

// VerifyErrorMissResponseToProto is the Connect form of VerifyErrorMissResponse.
func VerifyErrorMissResponseToProto(r *VerifyErrorMissResponse) *rpcpb.VerifyErrorMissResponse {
	if r == nil {
		return nil
	}
	return &rpcpb.VerifyErrorMissResponse{
		Accept:      r.Accept,
		Signature:   r.Signature,
		VoterSlot:   r.VoterSlot,
		Mempool:     r.Mempool,
		RejectCause: r.RejectCause,
	}
}

// VerifyErrorMissResponseFromProto reconstructs VerifyErrorMissResponse.
func VerifyErrorMissResponseFromProto(r *rpcpb.VerifyErrorMissResponse) *VerifyErrorMissResponse {
	if r == nil {
		return nil
	}
	return &VerifyErrorMissResponse{
		Accept:      r.GetAccept(),
		Signature:   r.GetSignature(),
		VoterSlot:   r.GetVoterSlot(),
		Mempool:     r.GetMempool(),
		RejectCause: r.GetRejectCause(),
	}
}

// ChallengeReceiptRequestToProto is the Connect inner payload for ChallengeReceipt.
func ChallengeReceiptRequestToProto(r ChallengeReceiptRequest) *rpcpb.ChallengeReceiptRequest {
	return &rpcpb.ChallengeReceiptRequest{
		InferenceId:     r.InferenceID,
		Payload:         PayloadJSONToProto(r.Payload),
		Diffs:           diffsJSONToProto(r.Diffs),
		ProtocolVersion: r.ProtocolVersion,
	}
}

// ChallengeReceiptRequestFromProto reconstructs the JSON-shaped request.
func ChallengeReceiptRequestFromProto(r *rpcpb.ChallengeReceiptRequest) ChallengeReceiptRequest {
	if r == nil {
		return ChallengeReceiptRequest{}
	}
	return ChallengeReceiptRequest{
		InferenceID:     r.GetInferenceId(),
		Payload:         PayloadJSONFromProto(r.GetPayload()),
		Diffs:           diffsJSONFromProto(r.GetDiffs()),
		ProtocolVersion: r.GetProtocolVersion(),
	}
}

// ChallengeReceiptResponseToProto is the Connect form of ChallengeReceiptResponse.
func ChallengeReceiptResponseToProto(r *ChallengeReceiptResponse) *rpcpb.ChallengeReceiptResponse {
	if r == nil {
		return nil
	}
	return &rpcpb.ChallengeReceiptResponse{
		Receipt: r.Receipt,
		Mempool: r.Mempool,
	}
}

// ChallengeReceiptResponseFromProto reconstructs ChallengeReceiptResponse.
func ChallengeReceiptResponseFromProto(r *rpcpb.ChallengeReceiptResponse) *ChallengeReceiptResponse {
	if r == nil {
		return nil
	}
	return &ChallengeReceiptResponse{
		Receipt: r.GetReceipt(),
		Mempool: r.GetMempool(),
	}
}

// GossipNonceRequestToProto is the Connect inner payload for GossipService.Nonce.
func GossipNonceRequestToProto(r GossipNonceRequest) *rpcpb.GossipNonceRequest {
	return &rpcpb.GossipNonceRequest{
		Nonce:     r.Nonce,
		StateHash: r.StateHash,
		StateSig:  r.StateSig,
		SlotId:    r.SlotID,
	}
}

// GossipNonceRequestFromProto reconstructs GossipNonceRequest.
func GossipNonceRequestFromProto(r *rpcpb.GossipNonceRequest) GossipNonceRequest {
	if r == nil {
		return GossipNonceRequest{}
	}
	return GossipNonceRequest{
		Nonce:     r.GetNonce(),
		StateHash: r.GetStateHash(),
		StateSig:  r.GetStateSig(),
		SlotID:    r.GetSlotId(),
	}
}
