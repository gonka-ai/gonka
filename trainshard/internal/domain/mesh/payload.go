package mesh

import (
	"context"
	"strings"

	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

const identityPayloadVersion = "trainshard-mesh-v0"

func VerifyIdentity(ctx context.Context, identity Identity, signer vo.Address, delegation ports.Delegation) error {
	if identity.Member.Node.IsZero() || identity.Member.Address == "" || identity.Member.PublicKey == "" {
		return ErrIncompleteMember
	}
	speaks, err := delegation.Speaks(ctx, identity.Member.Node.Participant, signer)
	if err != nil {
		return ErrDelegationUnknown
	}
	if !speaks {
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
