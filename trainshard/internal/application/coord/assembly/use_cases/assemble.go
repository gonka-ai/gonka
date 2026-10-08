package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/timex"
)

type AssembleUseCase struct {
	lifecycle shard.ChainLifecycle
	window    shard.AssemblyWindow
	poll      time.Duration
}

func NewAssembleUseCase(lifecycle shard.ChainLifecycle, window shard.AssemblyWindow, poll time.Duration) *AssembleUseCase {
	return &AssembleUseCase{lifecycle: lifecycle, window: window, poll: poll}
}

func (uc *AssembleUseCase) Execute(ctx context.Context, cmd AssembleCommand) (vo.ShardID, error) {
	var told vo.Height
	for {
		// 1. Wait out PoC and confirmation PoC, telling each new height it waits for
		now, opens, err := uc.window.AssemblyOpensAt(ctx)
		if err != nil {
			return 0, err
		}
		if now < opens {
			if opens != told && cmd.Waiting != nil {
				cmd.Waiting(now, opens)
				told = opens
			}
			if err := timex.Sleep(ctx, uc.poll); err != nil {
				return 0, fmt.Errorf("waiting for PoC to end at height %d: %w", opens, err)
			}
			continue
		}

		// 2. Ask the chain to assemble; a confirmation PoC that started first sends it back to waiting
		shardID, err := uc.lifecycle.Assemble(ctx, cmd.Proposal)
		if !errors.Is(err, shard.ErrAssemblyClosed) {
			return shardID, err
		}
		if err := timex.Sleep(ctx, uc.poll); err != nil {
			return 0, err
		}
	}
}
