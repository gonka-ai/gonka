package main

import (
	"context"
	"fmt"

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
// does not emit OnEpochChange). A failure is returned so startup fails:
// phase left at 0 would store payloads under an epoch validators never ask.
func bootstrapPhase(ctx context.Context, query chain.InferenceClient, phase *chain.Phase) error {
	epochResp, err := query.GetCurrentEpoch(ctx, &chaintypes.QueryGetCurrentEpochRequest{})
	if err != nil {
		return fmt.Errorf("query current epoch: %w", err)
	}
	phase.SetEpoch(epochResp.Epoch)
	return nil
}

func newChainEventBridge(
	ctx context.Context,
	chainRPCURL string,
	chainClient *chain.Client,
	submitter chainbridge.Submitter,
) (*chainEventBridge, error) {
	phase := new(chain.Phase)
	if err := bootstrapPhase(ctx, chainClient.InferenceQueryClient(), phase); err != nil {
		return nil, err
	}
	eventListener := events.NewListener(chainRPCURL)
	br := chainbridge.NewChainBridge(chainClient, submitter)
	br.Subscribe(eventListener)
	return &chainEventBridge{
		listener: eventListener,
		bridge:   br,
		phase:    phase,
	}, nil
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
