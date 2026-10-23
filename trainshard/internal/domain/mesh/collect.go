package mesh

import (
	"context"
	"errors"
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

// Collect names a node missing when its host is silent, the first identity the host offers for it
// does not verify or carries no 32-byte public key, or it offers a public key another node offers
// too. It fails only when no host answers or the chain cannot say whether a signer speaks for a
// host. A host speaks for the nodes the chain places on it and no other
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
	keys := make(map[vo.NodeRef][32]byte, len(reserved))
	answered := 0
	var silent error

	for _, reply := range answers {
		if reply.err != nil {
			silent = reply.err
			continue
		}
		answered++

		checked := make(map[vo.NodeRef]bool, len(reply.host.Nodes))
		for _, identity := range reply.identities {
			node := identity.Member.Node
			if checked[node] || !slices.Contains(reply.host.Nodes, node) {
				continue
			}
			checked[node] = true
			key, ok := keyBytes(identity.Member.PublicKey)
			if !ok {
				continue
			}
			signer, err := verifier.Recover(IdentityPayload(shardID, identity.Member), identity.Signature)
			if err != nil {
				continue
			}
			if err := VerifyIdentity(ctx, identity, signer, delegation); err != nil {
				if errors.Is(err, ErrDelegationUnknown) {
					return nil, nil, err
				}
				continue
			}
			collected[node], keys[node] = identity.Member, key
		}
	}
	if answered == 0 && silent != nil {
		return nil, nil, silent
	}

	offered := make(map[[32]byte]int, len(collected))
	for node := range collected {
		if slices.Contains(reserved, node) {
			offered[keys[node]]++
		}
	}

	members = make([]Member, 0, len(reserved))
	missing = make([]vo.NodeRef, 0)
	for _, node := range reserved {
		member, found := collected[node]
		if !found || offered[keys[node]] > 1 {
			missing = append(missing, node)
			continue
		}
		members = append(members, member)
	}
	return members, missing, nil
}
