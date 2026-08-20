package mesh

import (
	"context"

	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/syncx"
)

func Probe(ctx context.Context, hosts Hosts, cfg Config) []Pair {
	answers := syncx.Fan(cfg.Refs(), func(node vo.NodeRef) []Pair {
		pairs, err := hosts.Probe(ctx, cfg, node)
		if err != nil {
			return brokenWithEveryone(node, cfg)
		}
		return pairs
	})

	seen := make(map[Pair]struct{})
	failed := make([]Pair, 0)
	for _, pairs := range answers {
		for _, pair := range pairs {
			if _, repeated := seen[pair]; repeated {
				continue
			}
			seen[pair] = struct{}{}
			failed = append(failed, pair)
		}
	}
	return failed
}

func brokenWithEveryone(node vo.NodeRef, cfg Config) []Pair {
	pairs := make([]Pair, 0, len(cfg.Peers))
	for _, peer := range cfg.PeersFor(node) {
		pairs = append(pairs, NewPair(node, peer.Node))
	}
	return pairs
}
