package usecases

import "trainshard/internal/domain/shared/vo"

type AssembleCommand struct {
	Proposal uint64
	// Waiting hears each new height the assemble waits for while PoC or confirmation PoC runs
	Waiting func(now, opens vo.Height)
}

type SettleCommand struct {
	Shard vo.ShardID
}

type KickCommand struct {
	Shard vo.ShardID
	Node  vo.NodeRef
}
