package app

import (
	"context"
	"net/http"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/metrics"
	"devshard/cmd/gateway/nonces"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
	"devshard/transport"
	"devshard/user"
)

// ShutdownGracePeriod is the drain budget Shutdown gives the shutdown steps, as a signal's shutdown does.
const ShutdownGracePeriod = shutdownGracePeriod

// Sources is what a harness hands Compose in place of a dialled chain: no height oracle and no governance feed.
type Sources struct {
	Serving   registry.SessionFactory
	ReadOnly  registry.SessionFactory
	Reader    chain.Reader
	Transport chain.Transport
	PublicAPI *http.Client
	Hosts     HostDirectory
}

// Composed is a gateway composed as run composes it but not started, for a harness that boots it step by step. See README.md.
type Composed struct {
	gateway *gateway
	values  env.Values
	started bootState
}

// BootStep is one step of the boot contract under its docs/operations.md name.
type BootStep struct {
	Name  string
	Start func() error
}

// Compose composes the gateway over sources instead of a dialled chain.
func Compose(ctx context.Context, values env.Values, storageDir string, gatewayStore *store.Store, sources Sources) (*Composed, error) {
	dialled := func(config.Chain, config.HeightSync, string, *transport.PeerRPCAdoption, user.StepTimer) (chainSources, error) {
		return chainSources{
			Serving:   sources.Serving,
			ReadOnly:  sources.ReadOnly,
			Reader:    sources.Reader,
			Transport: sources.Transport,
			PublicAPI: sources.PublicAPI,
			Hosts:     sources.Hosts,
		}, nil
	}
	composed, err := compose(ctx, values, storageDir, gatewayStore, dialled)
	if err != nil {
		return nil, err
	}
	return &Composed{gateway: composed, values: values}, nil
}

// BootSteps lists the boot contract in order for the caller to run, skip or replace one step at a time.
func (composed *Composed) BootSteps(ctx, backgroundCtx context.Context) []BootStep {
	steps := composed.gateway.bootOrder(ctx, backgroundCtx, composed.gateway.config.Load(), &composed.started)
	exported := make([]BootStep, 0, len(steps))
	for _, step := range steps {
		exported = append(exported, BootStep{Name: step.name, Start: step.start})
	}
	return exported
}

// Shutdown runs the shutdown contract, then waits for the devshard-write republish the boot started.
func (composed *Composed) Shutdown() error {
	err := composed.gateway.shutdown(shutdownGracePeriod)
	if composed.started.republished != nil {
		<-composed.started.republished
	}
	return err
}

// Handler is the HTTP handler the listener would serve.
func (composed *Composed) Handler() http.Handler { return composed.gateway.server.Handler }

// Config is the configuration holder an operator override swaps.
func (composed *Composed) Config() *config.Holder { return composed.gateway.config }

// Store is the control-plane store.
func (composed *Composed) Store() *store.Store { return composed.gateway.store }

// Observer is the chain phase observer.
func (composed *Composed) Observer() *chain.PhaseObserver { return composed.gateway.observer }

// Escrows is the registry of published escrows.
func (composed *Composed) Escrows() *registry.Registry { return composed.gateway.escrows }

// Manager is the escrow lifecycle manager.
func (composed *Composed) Manager() *escrow.Manager { return composed.gateway.manager }

// Races is the race engine.
func (composed *Composed) Races() *engine.Engine { return composed.gateway.races }

// Nonces is the nonce ledger's recorder, nil when nonce accounting is off.
func (composed *Composed) Nonces() *nonces.Recorder { return composed.gateway.nonces }

// Journal is the journal every producer narrates through.
func (composed *Composed) Journal() *journal.Journal { return composed.gateway.events }

// Telemetry is the Prometheus registry's owner.
func (composed *Composed) Telemetry() *metrics.Metrics { return composed.gateway.telemetry }

// Deactivate takes an escrow out of service as the admin API's deactivate does.
func (composed *Composed) Deactivate(ctx context.Context, escrowID string) error {
	return composed.operator().Deactivate(ctx, escrowID)
}

// Settle settles an escrow as the admin API's settle does.
func (composed *Composed) Settle(ctx context.Context, escrowID string, force bool) (chain.SettleEscrowResult, error) {
	return composed.operator().Settle(ctx, escrowID, force)
}

func (composed *Composed) operator() *operations {
	running := composed.gateway
	return &operations{values: composed.values, config: running.config, store: running.store, escrows: running.escrows, manager: running.manager, nonces: running.nonces}
}
