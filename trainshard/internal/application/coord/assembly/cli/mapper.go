package cli

import (
	"fmt"
	"strconv"
	"strings"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func toShardID(args []string) (vo.ShardID, error) {
	return vo.ParseShardID(args[0])
}

func toProposalID(args []string) (uint64, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(args[0]), 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("proposal_id %q: %w", args[0], shared.ErrValidation)
	}
	return id, nil
}
