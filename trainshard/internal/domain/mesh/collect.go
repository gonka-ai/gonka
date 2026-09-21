package mesh

import (
	"context"
	"slices"

	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/syncx"
)

type answer struct {
	host       vo.Host
	identities []Identity
	err        error
}

// Collect gathers one signed member per reserved node and names those that have not reported yet.
// A silent host only leaves its nodes missing, unless no host answers at all; an identity that does
// not verify is a refusal and fails the whole mesh. A host speaks for the nodes the chain places on
// it and no other: an identity it offers for another machine's node, or for one it no longer holds,
// is not taken
func Collect(
	ctx context.Context,
	hosts Hosts,
	verifier ports.Verifier,
	delegation ports.Delegation,
	shardID vo.ShardID,
	machines []vo.Host,
	reserved []vo.NodeRef,
) (members []Member, missing []vo.NodeRef, err error) {
	answers := syncx.Fan(machines, func(host vo.Host) answer {
		identities, err := hosts.Identities(ctx, shardID, host)
		return answer{host: host, identities: identities, err: err}
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
			if !slices.Contains(reply.host.Nodes, identity.Member.Node) {
				continue
			}
			signer, err := verifier.Recover(IdentityPayload(shardID, identity.Member), identity.Signature)
			if err != nil {
				return nil, nil, ErrForeignIdentity
			}
			if err := VerifyIdentity(ctx, identity, signer, delegation); err != nil {
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
