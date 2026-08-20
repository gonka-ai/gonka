package cli

import (
	"trainshard/internal/domain/shared/vo"
)

func toShardID(args []string) (vo.ShardID, error) {
	return vo.ParseShardID(args[0])
}
