package types

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Domain tags prefixed to proto-encoded signature preimages. Protobuf does not
// encode the message type, so two messages with the same field numbers and
// wire types marshal to identical bytes; a signature over one would verify as
// the other. Height-sync messages keep their tags in the heightsync package.
//
// Changing a tag is a protocol break for that message family.
const (
	DomainMsgValidation     = "devshard.validation.v1"
	DomainMsgValidationVote = "devshard.validationvote.v1"
	DomainTimeoutVote       = "devshard.timeoutvote.v1"
	DomainErrorMissVote     = "devshard.errormissvote.v1"
)

// signedPreimageDomain is the prefix CanonicalSignedBytes adds. Empty means the
// preimage is the deterministic proto encoding alone (the message's field
// layout must then be unique among other empty-domain signed types).
var signedPreimageDomain = map[protoreflect.FullName]string{
	"devshard.v1.MsgValidation":        DomainMsgValidation,
	"devshard.v1.MsgValidationVote":    DomainMsgValidationVote,
	"devshard.v1.TimeoutVoteContent":   DomainTimeoutVote,
	"devshard.v1.ErrorMissVoteContent": DomainErrorMissVote,
}

// SignedPreimageDomain is the domain tag CanonicalSignedBytes prefixes, or "".
func SignedPreimageDomain(msg proto.Message) string {
	if msg == nil {
		return ""
	}
	return signedPreimageDomain[msg.ProtoReflect().Descriptor().FullName()]
}

// CanonicalSignedBytes is the secp256k1 preimage for a host- or user-signed
// proto message: optional domain tag || deterministic proto encoding.
// Callers must zero signature fields before calling.
func CanonicalSignedBytes(msg proto.Message) ([]byte, error) {
	if msg == nil {
		return nil, fmt.Errorf("nil signed proto")
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return nil, err
	}
	domain := SignedPreimageDomain(msg)
	if domain == "" {
		return body, nil
	}
	out := make([]byte, 0, len(domain)+len(body))
	out = append(out, domain...)
	out = append(out, body...)
	return out, nil
}
