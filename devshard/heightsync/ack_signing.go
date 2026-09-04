package heightsync

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"devshard/signing"
	"devshard/types"
)

// DomainHeightAck is the canonical signing domain for MsgHeightAck fields 1–7.
const DomainHeightAck = "heightsync.ack.v1"

var (
	ErrAckEmpty  = errors.New("heightsync: empty height ack")
	ErrAckNoSig  = errors.New("heightsync: missing host_sig")
	ErrAckVerify = errors.New("heightsync: ack signature verification failed")
)

// CanonicalAckBytes returns DomainHeightAck || proto.Marshal(fields 1..7).
func CanonicalAckBytes(ack *types.MsgHeightAck) ([]byte, error) {
	if ack == nil {
		return nil, ErrAckEmpty
	}
	content, ok := proto.Clone(ack).(*types.MsgHeightAck)
	if !ok || content == nil {
		return nil, ErrAckEmpty
	}
	content.HostSig = nil
	body, err := proto.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal height ack: %w", err)
	}
	return append([]byte(DomainHeightAck), body...), nil
}

// SignAck sets field 8 over fields 1–7.
func SignAck(signer signing.Signer, ack *types.MsgHeightAck) error {
	if signer == nil {
		return errors.New("heightsync: nil signer")
	}
	blob, err := CanonicalAckBytes(ack)
	if err != nil {
		return err
	}
	sig, err := signer.Sign(blob)
	if err != nil {
		return fmt.Errorf("sign height ack: %w", err)
	}
	ack.HostSig = sig
	return nil
}

// VerifyAck checks host_sig against slotKey (the registered host address).
func VerifyAck(verifier signing.Verifier, ack *types.MsgHeightAck, slotKey string) error {
	if verifier == nil {
		return errors.New("heightsync: nil verifier")
	}
	if ack == nil {
		return ErrAckEmpty
	}
	if len(ack.GetHostSig()) == 0 {
		return ErrAckNoSig
	}
	blob, err := CanonicalAckBytes(ack)
	if err != nil {
		return err
	}
	recovered, err := verifier.RecoverAddress(blob, ack.GetHostSig())
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAckVerify, err)
	}
	if recovered != slotKey {
		return fmt.Errorf("%w: signer %q != slot key %q", ErrAckVerify, recovered, slotKey)
	}
	return nil
}
