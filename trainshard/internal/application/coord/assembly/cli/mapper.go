package cli

import (
	"fmt"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func toShardID(args []string) (vo.ShardID, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("shard: %w", shared.ErrValidation)
	}
	return vo.ParseShardID(args[0])
}
