package mesh

import (
	"strings"

	"trainshard/internal/domain/shared/vo"
)

const identityPayloadVersion = "trainshard-mesh-v0"

func VerifyIdentity(identity Identity, signer vo.Address) error {
	if identity.Member.Node.IsZero() || identity.Member.Address == "" || identity.Member.PublicKey == "" {
		return ErrIncompleteMember
	}
	if signer != vo.Address(identity.Member.Node.Participant) {
		return ErrForeignIdentity
	}
	return nil
}

func IdentityPayload(shardID vo.ShardID, member Member) []byte {
	return []byte(strings.Join([]string{
		identityPayloadVersion,
		shardID.String(),
		string(member.Node.Participant),
		string(member.Node.NodeID),
		member.Address,
		member.PublicKey,
	}, "\n"))
}
