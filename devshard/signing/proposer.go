package signing

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"devshard/types"
)

// Domain strings are bound into every proposer preimage so two proto messages
// with the same field numbers cannot share a signature. A trailing NUL stops
// one domain from being a prefix of another.
const (
	DomainFinishInference = "devshard.MsgFinishInference.v1"
	DomainValidation      = "devshard.MsgValidation.v1"
	DomainValidationVote  = "devshard.MsgValidationVote.v1"
	DomainRevealSeed      = "devshard.MsgRevealSeed.v1"
)

var proposerMarshal = proto.MarshalOptions{Deterministic: true}

// CanonicalProposerBytes is the preimage for proposer_sig: domain || 0x00 ||
// deterministic proto of msg with proposer_sig cleared.
func CanonicalProposerBytes(msg proto.Message) ([]byte, error) {
	if msg == nil {
		return nil, fmt.Errorf("nil proposer message")
	}
	domain, ok := proposerDomain(msg)
	if !ok {
		return nil, fmt.Errorf("unknown proposer message %T", msg)
	}
	cloned := proto.Clone(msg)
	zeroProposerSig(cloned)
	body, err := proposerMarshal.Marshal(cloned)
	if err != nil {
		return nil, fmt.Errorf("marshal proposer message: %w", err)
	}
	out := make([]byte, 0, len(domain)+1+len(body))
	out = append(out, domain...)
	out = append(out, 0)
	return append(out, body...), nil
}

func proposerDomain(msg proto.Message) (string, bool) {
	switch msg.(type) {
	case *types.MsgFinishInference:
		return DomainFinishInference, true
	case *types.MsgValidation:
		return DomainValidation, true
	case *types.MsgValidationVote:
		return DomainValidationVote, true
	case *types.MsgRevealSeed:
		return DomainRevealSeed, true
	default:
		return "", false
	}
}

func zeroProposerSig(msg proto.Message) {
	switch m := msg.(type) {
	case *types.MsgFinishInference:
		m.ProposerSig = nil
	case *types.MsgValidation:
		m.ProposerSig = nil
	case *types.MsgValidationVote:
		m.ProposerSig = nil
	case *types.MsgRevealSeed:
		m.ProposerSig = nil
	}
}
