package usecases

import "trainshard/internal/domain/shared/vo"

type AssembleCommand struct {
	Proposal uint64
}

type SettleCommand struct {
	Shard vo.ShardID
}

type KickCommand struct {
	Shard vo.ShardID
	Node  vo.NodeRef
}
