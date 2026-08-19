package heightsync

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"devshard/signing"
	"devshard/types"
)

// DomainRepair is the signing domain for both legs of the repair probe.
// This is the one exception to §15's request-unsigned asymmetry: there is
// no courier user on this path.
const DomainRepair = "heightsync.repair.v1"

// Repair outcomes. The responder never emits UNREACHABLE — that is synthesized
// by the requester on timeout / unsigned / unverifiable response.
const (
	RepairOutcomeHeight      = "HEIGHT"
	RepairOutcomeUnreachable = "UNREACHABLE"
)

var (
	ErrRepairEmpty   = errors.New("heightsync: empty repair probe")
	ErrRepairNoSig   = errors.New("heightsync: missing repair signature")
	ErrRepairVerify  = errors.New("heightsync: repair signature verification failed")
	ErrRepairOutcome = errors.New("heightsync: unknown repair outcome")
)

// RepairRequest is the JSON body of POST .../heightsync/repair.
type RepairRequest struct {
	TurnSeq           uint64 `json:"turn_seq"`
	RefNonce          uint64 `json:"ref_nonce"`
	RequesterSlot     uint32 `json:"requester_slot"`
	ObservedHeight    uint64 `json:"observed_height"`
	ObservedBlockHash []byte `json:"observed_block_hash"`
	RequesterSig      []byte `json:"requester_sig"`
}

// RepairResponse is the JSON body of a successful repair probe. Outcome is
// HEIGHT on the wire; UNREACHABLE is requester-local.
type RepairResponse struct {
	Outcome           string              `json:"outcome"`
	ObservedHeight    uint64              `json:"observed_height"`
	ObservedBlockHash []byte              `json:"observed_block_hash"`
	SyncState         types.SyncState     `json:"sync_state,omitempty"`
	Ack               *types.MsgHeightAck `json:"ack,omitempty"`
	ResponderSig      []byte              `json:"responder_sig"`
}

// RepairProbeFn is the unicast send. Host injects Server.RepairProbe.
type RepairProbeFn func(ctx context.Context, targetSlot uint32, req *RepairRequest) (*RepairResponse, error)

// CanonicalRepairRequestBytes is DomainRepair || proto-wire fields 1–5.
func CanonicalRepairRequestBytes(r *RepairRequest) ([]byte, error) {
	if r == nil {
		return nil, ErrRepairEmpty
	}
	var body []byte
	body = appendVarintField(body, 1, r.TurnSeq)
	body = appendVarintField(body, 2, r.RefNonce)
	body = appendVarintField(body, 3, uint64(r.RequesterSlot))
	body = appendVarintField(body, 4, r.ObservedHeight)
	body = appendBytesField(body, 5, r.ObservedBlockHash)
	return append([]byte(DomainRepair), body...), nil
}

// CanonicalRepairResponseBytes is DomainRepair || proto-wire fields 1–5.
// Optional ack is proto.Marshal of MsgHeightAck (including host_sig).
func CanonicalRepairResponseBytes(r *RepairResponse) ([]byte, error) {
	if r == nil {
		return nil, ErrRepairEmpty
	}
	var body []byte
	body = appendBytesField(body, 1, []byte(r.Outcome))
	body = appendVarintField(body, 2, r.ObservedHeight)
	body = appendBytesField(body, 3, r.ObservedBlockHash)
	body = appendVarintField(body, 4, uint64(r.SyncState))
	if r.Ack != nil {
		ackBytes, err := proto.Marshal(r.Ack)
		if err != nil {
			return nil, fmt.Errorf("marshal repair ack: %w", err)
		}
		body = appendBytesField(body, 5, ackBytes)
	}
	return append([]byte(DomainRepair), body...), nil
}

// SignRepairRequest sets requester_sig over fields 1–5.
func SignRepairRequest(signer signing.Signer, r *RepairRequest) error {
	if signer == nil {
		return errors.New("heightsync: nil signer")
	}
	blob, err := CanonicalRepairRequestBytes(r)
	if err != nil {
		return err
	}
	sig, err := signer.Sign(blob)
	if err != nil {
		return fmt.Errorf("sign repair request: %w", err)
	}
	r.RequesterSig = sig
	return nil
}

// SignRepairResponse sets responder_sig over the response content.
func SignRepairResponse(signer signing.Signer, r *RepairResponse) error {
	if signer == nil {
		return errors.New("heightsync: nil signer")
	}
	blob, err := CanonicalRepairResponseBytes(r)
	if err != nil {
		return err
	}
	sig, err := signer.Sign(blob)
	if err != nil {
		return fmt.Errorf("sign repair response: %w", err)
	}
	r.ResponderSig = sig
	return nil
}

// VerifyRepairRequest checks requester_sig against slotKey.
func VerifyRepairRequest(verifier signing.Verifier, r *RepairRequest, slotKey string) error {
	if verifier == nil {
		return errors.New("heightsync: nil verifier")
	}
	if r == nil {
		return ErrRepairEmpty
	}
	if len(r.RequesterSig) == 0 {
		return ErrRepairNoSig
	}
	blob, err := CanonicalRepairRequestBytes(r)
	if err != nil {
		return err
	}
	return verifyRepairSig(verifier, blob, r.RequesterSig, slotKey)
}

// VerifyRepairResponse checks responder_sig against slotKey.
func VerifyRepairResponse(verifier signing.Verifier, r *RepairResponse, slotKey string) error {
	if verifier == nil {
		return errors.New("heightsync: nil verifier")
	}
	if r == nil {
		return ErrRepairEmpty
	}
	if len(r.ResponderSig) == 0 {
		return ErrRepairNoSig
	}
	if r.Outcome != "" && r.Outcome != RepairOutcomeHeight {
		return fmt.Errorf("%w: %s", ErrRepairOutcome, r.Outcome)
	}
	blob, err := CanonicalRepairResponseBytes(r)
	if err != nil {
		return err
	}
	return verifyRepairSig(verifier, blob, r.ResponderSig, slotKey)
}

func verifyRepairSig(verifier signing.Verifier, blob, sig []byte, slotKey string) error {
	recovered, err := verifier.RecoverAddress(blob, sig)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRepairVerify, err)
	}
	if recovered != slotKey {
		return fmt.Errorf("%w: signer %q != slot key %q", ErrRepairVerify, recovered, slotKey)
	}
	return nil
}

func appendVarintField(b []byte, num protowire.Number, v uint64) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func appendBytesField(b []byte, num protowire.Number, v []byte) []byte {
	if len(v) == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}
