package mockdapi

import (
	"context"
	"log/slog"

	"common/nodemanager/gen"
	"devshard/cmd/devshardd/events"
)

func (s *Service) runBlockSync(ctx context.Context) error {
	if s.cfg.ChainRPCAddr == "" {
		slog.Warn("mockdapi: ChainRPCAddr unset; skipping CometBFT block sync")
		return s.runChainPoll(ctx)
	}
	l := events.NewListener(s.cfg.ChainRPCAddr)
	l.OnNewBlock(func(ctx context.Context, e events.NewBlockEvent) {
		if err := s.syncRuntimeConfigAtBlock(ctx, e.BlockHeight); err != nil {
			slog.Warn("mockdapi: runtime config block sync failed", "height", e.BlockHeight, "err", err)
		}
		s.appendHostEventBlock(e)
	})
	return l.Start(ctx)
}

func (s *Service) appendHostEventBlock(e events.NewBlockEvent) {
	if s.hostEvents == nil || e.BlockHeight <= 0 || len(e.Header) == 0 {
		return
	}
	s.hostEvents.AppendBlock(&gen.BlockPayload{
		Height:           e.BlockHeight,
		Hash:             append([]byte(nil), e.BlockHash...),
		HeaderTimeUnixMs: e.Time.UnixMilli(),
		EpochIndex:       s.paramsSrc.CurrentEpochID(),
		Header:           e.Header,
	})
}

// RefreshRuntimeConfig pulls the latest params revision from mock-chain (after admin
// fault injection or on demand).
func (s *Service) RefreshRuntimeConfig(ctx context.Context) error {
	snap, err := s.runtimeFetcher.FetchSnapshot(ctx)
	if err != nil {
		return err
	}
	s.paramsSrc.ApplyRevision(snap)
	return nil
}

func (s *Service) syncRuntimeConfigAtBlock(ctx context.Context, blockHeight int64) error {
	snap, err := s.runtimeFetcher.FetchSnapshot(ctx)
	if err != nil {
		return err
	}
	snap.ParamsBlockHeight = 0
	s.paramsSrc.ApplyBlockIfChanged(blockHeight, snap.CurrentEpochID, snap)
	return nil
}
