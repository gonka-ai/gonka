package app

import (
	"context"
	"fmt"
	"log/slog"

	commonchain "common/chain"
	"common/nodemanager"
	"common/nodemanager/gen"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/heightsync"
	"devshard/logging"
	"devshard/runtimeparams"
)

// runtimeParams is the chain's operational governance as the gateway reads it. See ../README.md, "Escrow sessions and the chain connection".
type runtimeParams struct {
	feed             *runtimeparams.Managed
	closeNodeManager func()
}

func openRuntimeParams(chainClient *commonchain.Client, settings runtimeparams.EnvSettings) (*runtimeParams, error) {
	setup := runtimeparams.SetupConfig{
		Chain:  runtimeparams.NewGRPCChainFetcher(chainClient),
		Logger: slog.Default(),
		Env:    settings,
	}
	nodeManager, closeNodeManager := nodeManagerFeed(settings)
	if nodeManager != nil {
		setup.GRPCClient = nodeManager
	}
	feed, err := runtimeparams.NewManaged(context.Background(), setup)
	if err != nil {
		if closeNodeManager != nil {
			closeNodeManager()
		}
		return nil, fmt.Errorf("runtime params: %w", err)
	}
	logging.Info("runtime params feed opened", logkey.Subsystem, "runtimeparams",
		logkey.ParamsSource, feed.Source, logkey.NodeManager, nodeManager != nil)
	return &runtimeParams{feed: feed, closeNodeManager: closeNodeManager}, nil
}

func (p *runtimeParams) Heartbeat() *heightsync.HeartbeatConfig {
	if p == nil || p.feed == nil {
		return nil
	}
	provider := p.feed.BindProvider()
	if provider == nil {
		return nil
	}
	schedule := provider.SessionParams().Heartbeat
	return &schedule
}

func (p *runtimeParams) Close() error {
	if p == nil {
		return nil
	}
	p.feed.Close()
	if p.closeNodeManager != nil {
		p.closeNodeManager()
	}
	return nil
}

func nodeManagerFeed(settings runtimeparams.EnvSettings) (gen.NodeManagerClient, func()) {
	if settings.Source == runtimeparams.SourceChain || settings.NodeManagerAddr == "" {
		return nil, nil
	}
	client, err := nodemanager.NewClient(settings.NodeManagerAddr)
	if err != nil {
		logging.Warn("runtime params fall back to the chain poll", logkey.Subsystem, "runtimeparams",
			logkey.Addr, settings.NodeManagerAddr, logkey.Error, err)
		return nil, nil
	}
	return client.NodeManagerClient(), func() { _ = client.Close() }
}
