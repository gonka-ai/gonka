package main

import (
	"context"
	"log/slog"

	"common/chain"
	chainbridge "devshard/cmd/devshardd/bridge"
	"devshard/cmd/devshardd/events"

	chaintypes "github.com/productscience/inference/x/inference/types"
)

type chainEventBridge struct {
	listener *events.Listener
	bridge   *chainbridge.ChainBridge
	phase    *chain.Phase
}

// bootstrapPhase fetches the current epoch from the chain and seeds phase
// before runtime-config OnEpochChange starts firing (initial snapshot apply
// does not emit OnEpochChange).
func bootstrapPhase(ctx context.Context, chainClient *chain.Client, phase *chain.Phase) {
	epochResp, err := chainClient.InferenceQueryClient().GetCurrentEpoch(ctx, &chaintypes.QueryGetCurrentEpochRequest{})
	if err != nil {
		slog.Warn("phase: failed to bootstrap epoch, starting at 0", "error", err)
		return
	}
	phase.SetEpoch(epochResp.Epoch)
}

func newChainEventBridge(
	ctx context.Context,
	chainRPCURL string,
	chainClient *chain.Client,
	submitter chainbridge.Submitter,
) *chainEventBridge {
	phase := new(chain.Phase)
	bootstrapPhase(ctx, chainClient, phase)
	eventListener := events.NewListener(chainRPCURL)
	br := chainbridge.NewChainBridge(chainClient, submitter)
	br.Subscribe(eventListener)
	return &chainEventBridge{
		listener: eventListener,
		bridge:   br,
		phase:    phase,
	}
}

func (b *chainEventBridge) Bridge() *chainbridge.ChainBridge {
	return b.bridge
}

func (b *chainEventBridge) Phase() *chain.Phase {
	return b.phase
}

// OnNewBlock registers an additional new-block handler on the underlying listener.
// Must be called before Start. Used for height-sync headers, not epoch/prune.
func (b *chainEventBridge) OnNewBlock(h events.NewBlockHandler) {
	b.listener.OnNewBlock(h)
}

func (b *chainEventBridge) OnReady(h func(bool)) {
	b.listener.OnReady(h)
}

func (b *chainEventBridge) Start(ctx context.Context) error {
	if err := b.listener.Start(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}
