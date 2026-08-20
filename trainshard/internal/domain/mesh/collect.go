package mesh

import (
	"context"

	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/syncx"
)

type answer struct {
	identities []Identity
	err        error
}

// Collect gathers one signed member per reserved node and names those that have not reported yet.
// A silent host only leaves its nodes missing, unless no host answers at all; an identity that does
// not verify is a refusal and fails the whole mesh
func Collect(
	ctx context.Context,
	hosts Hosts,
	verifier ports.Verifier,
	shardID vo.ShardID,
	participants []vo.Participant,
	reserved []vo.NodeRef,
) (members []Member, missing []vo.NodeRef, err error) {
	answers := syncx.Fan(participants, func(participant vo.Participant) answer {
		identities, err := hosts.Identities(ctx, shardID, participant)
		return answer{identities: identities, err: err}
	})

	collected := make(map[vo.NodeRef]Member, len(reserved))
	answered := 0
	var silent error

	for _, reply := range answers {
		if reply.err != nil {
			silent = reply.err
			continue
		}
		answered++

		for _, identity := range reply.identities {
			signer, err := verifier.Recover(IdentityPayload(shardID, identity.Member), identity.Signature)
			if err != nil {
				return nil, nil, ErrForeignIdentity
			}
			if err := VerifyIdentity(identity, signer); err != nil {
				return nil, nil, err
			}
			collected[identity.Member.Node] = identity.Member
		}
	}
	if answered == 0 && silent != nil {
		return nil, nil, silent
	}

	members = make([]Member, 0, len(reserved))
	missing = make([]vo.NodeRef, 0)
	for _, node := range reserved {
		member, found := collected[node]
		if !found {
			missing = append(missing, node)
			continue
		}
		members = append(members, member)
	}
	return members, missing, nil
}
