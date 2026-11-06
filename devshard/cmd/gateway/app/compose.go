// Package app composes the gateway's layers into one process and runs it. See README.md.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"common/httpguard"
	"common/observability"

	devshardpkg "devshard"
	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/heights"
	"devshard/cmd/gateway/hostping"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/metrics"
	"devshard/cmd/gateway/nonces"
	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
	"devshard/cmd/gateway/warmup"
	"devshard/logging"
	"devshard/signing"
	"devshard/transport"
)

// Version is the build's version; package main copies its stamped Version here before Main runs.
var Version = "dev"

const (
	shutdownGracePeriod = 10 * time.Second
	chainRequestTimeout = 10 * time.Second

	contentionMutexProfileFraction = 5
	contentionBlockProfileRate     = time.Millisecond
)

// Main runs the gateway until SIGINT or SIGTERM and exits the process with status 1 when it fails.
func Main() {
	observability.InstallLogger(env.LogFormat())
	applyDialGuard()
	warnIgnoredLegacyStore()
	if err := serve(); err != nil {
		logging.Error("gateway exited", logkey.Error, err)
		os.Exit(1)
	}
}

// applyContentionProfiles samples lock contention and blocking for /debug/pprof when the operator asks. See ../docs/operations.md.
func applyContentionProfiles(enabled *bool) {
	if enabled == nil || !*enabled {
		return
	}
	runtime.SetMutexProfileFraction(contentionMutexProfileFraction)
	runtime.SetBlockProfileRate(int(contentionBlockProfileRate.Nanoseconds()))
	logging.Info("lock contention and blocking are sampled for /debug/pprof",
		logkey.Subsystem, "gateway", logkey.Used, "DEVSHARD_ENABLE_CONTENTION_PROFILES")
}

func warnOpenModels(configured config.Limits) {
	if open := configured.OpenModels(); len(open) > 0 {
		logging.Warn("models are open: any caller reaches them without a key and spends the escrows serving them",
			logkey.Subsystem, "gateway", logkey.OpenModels, open)
	}
}

func warnIgnoredLegacyStore() {
	if ignored := env.LegacyPostgresVariablesSet(); len(ignored) > 0 {
		logging.Warn("legacy Postgres settings are ignored: the gateway keeps its own store and runs as one process per escrow set",
			logkey.Subsystem, "gateway", logkey.IgnoredVariables, ignored)
	}
}

// applyDialGuard arms the dial-time SSRF guard before anything can dial. See ../docs/operations.md.
func applyDialGuard() {
	allowed := env.AllowPrivateAddresses()
	httpguard.SetAllowPrivate(allowed)
	if allowed {
		logging.Warn("dials to private addresses are allowed: the SSRF guard is off",
			logkey.Subsystem, "gateway", logkey.Used, env.AllowPrivateAddressesVariable)
	}
}

// serve owns the signal context so releasing it survives a panic; os.Exit skips a defer left in main.
func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopTracing := startTracing(ctx)
	defer stopTracing()
	return run(ctx)
}

func run(ctx context.Context) error {
	values, err := env.Load()
	if err != nil {
		return err
	}
	applyContentionProfiles(values.ContentionProfiles)
	storageDir, err := resolveStorageDir(values.StorageDir)
	if err != nil {
		return err
	}
	values.StorageDir = &storageDir

	gatewayStore, err := store.Open(storageDir)
	if err != nil {
		return err
	}
	gateway, err := compose(ctx, values, storageDir, gatewayStore, chainBackedSessions(gatewayStore, storageDir))
	if err != nil {
		return errors.Join(err, gatewayStore.Close())
	}
	return gateway.serve(ctx)
}

type gateway struct {
	config       *config.Holder
	store        *store.Store
	observer     *chain.PhaseObserver
	escrows      *registry.Registry
	manager      *escrow.Manager
	router       *scheduler.Scheduler
	races        *engine.Engine
	finishes     *finishNotice
	limiter      *limits.GatewayLimiter
	participants *limits.ParticipantLimiter
	telemetry    *metrics.Metrics
	server       *http.Server
	endRequests  context.CancelFunc
	publicAPI    *http.Client
	nonces       *nonces.Recorder
	events       *journal.Journal
	warmup       *warmup.Prober
	hostPings    *hostping.Pinger
	heights      *heights.Oracle
	governance   *runtimeParams
	hosts        HostDirectory

	builders     int
	devshardWork chan struct{}
	bootSteps    func(g *gateway, ctx, backgroundCtx context.Context, settings *config.Config, started *bootState) []bootStep
}

func compose(ctx context.Context, values env.Values, storageDir string, gatewayStore *store.Store, openSessions sessionSources) (*gateway, error) {
	overrides, err := gatewayStore.LoadOverrides(ctx)
	if err != nil {
		return nil, err
	}
	configuration, err := config.Build(values, overrides)
	if err != nil {
		return nil, err
	}
	warnOpenModels(configuration.Limits)
	configHolder := config.NewHolder(configuration)
	clock := time.Now

	boot := newBootBudget(int(configuration.Server.MaxConcurrentRuntimeBuilds))
	routePrefix, _, err := devshardpkg.ResolveRoutePrefix(devshardpkg.VersionedRoutePrefix(Version))
	if err != nil {
		return nil, fmt.Errorf("resolving host route prefix from version %q: %w", Version, err)
	}

	telemetry := metrics.New()
	adoption := transport.NewPeerRPCAdoption(metrics.NewPeerRPCRecorder(telemetry))
	steps := metrics.NewStepRecorder(telemetry)
	sources, err := openSessions(configuration.Chain, configuration.HeightSync, routePrefix, adoption, steps.SessionStep)
	if err != nil {
		return nil, err
	}
	publicAPI := boot.client
	if sources.PublicAPI != nil {
		publicAPI = sources.PublicAPI
	}
	observer, err := chain.NewPhaseObserver(chain.ObserverConfig{
		PublicAPIBaseURL:     configuration.Chain.PublicAPIBaseURL,
		Chain:                sources.Reader,
		VersionsPollInterval: time.Duration(configuration.Chain.VersionsPollIntervalSeconds) * time.Second,
		HTTPClient:           publicAPI,
		Now:                  clock,
	})
	if err != nil {
		return nil, err
	}

	recorder := nonces.Open(configuration.NonceAccounting, storageDir, observer, clock)
	events := journal.New(journalSettings(recorder))
	built := false
	defer func() {
		if !built {
			_ = events.Close()
		}
	}()
	txClient, err := chain.NewTxClient(chain.Config{
		Transport:    sources.Transport,
		FeeDenom:     configuration.Tx.FeeDenom,
		FeeAmount:    uint64(configuration.Tx.FeeAmount),
		GasLimit:     uint64(configuration.Tx.GasLimit),
		PollInterval: time.Duration(configuration.Tx.PollIntervalMS) * time.Millisecond,
		PollTimeout:  time.Duration(configuration.Tx.PollTimeoutMS) * time.Millisecond,
		Now:          clock,
		Narrator:     events,
	})
	if err != nil {
		return nil, err
	}
	recorder.SetCreationEpoch(creationEpochOf(txClient, gatewayStore))

	participants := limits.NewParticipantLimiter(limits.ParticipantConfigFromConfig(configuration), clock)
	participants.SetNarrator(events)
	capacity := limits.NewCapacity(participants.Available)
	observer.Subscribe(capacity.Update)
	observer.Subscribe(func(snapshot chain.PhaseSnapshot) {
		participants.ObserveModels(contextWindowsOf(snapshot))
		participants.ObserveWeights(participantWeightsOf(snapshot))
	})
	observer.SetNarrator(events)
	observer.Subscribe((&phaseNarrator{events: events}).observe)
	gatewayLimiter := limits.NewGatewayLimiter(limits.GatewayConfigFromLimits(configuration.Limits))
	buffers := api.NewBufferBudget(configuration.Limits.MaxBufferedResponseBytes)
	configHolder.Subscribe(func(next *config.Config) {
		gatewayLimiter.Reconfigure(limits.GatewayConfigFromLimits(next.Limits))
		participants.Reconfigure(limits.ParticipantConfigFromConfig(next))
		buffers.Retune(next.Limits.MaxBufferedResponseBytes)
		warnOpenModels(next.Limits)
	})
	hosts := perf.NewTracker(configHolder, clock)
	hosts.SetNarrator(events)
	recorder.SetCapability(hostCapabilities(hosts, participants, capacity))

	devshardWork := make(chan struct{}, 1)
	depletion := &depletionNotice{}
	finishes := &finishNotice{}
	escrows, router, prober, err := newRouting(routingDeps{
		Sessions:     sources.Serving,
		ReadOnly:     sources.ReadOnly,
		Capacity:     capacity,
		Participants: participants,
		Hosts:        hosts,
		Snapshots:    observer,
		Config:       configHolder,
		Depletion:    depletion,
		Dispatches:   metrics.NewDispatchRecorder(telemetry),
		Steps:        steps,
		Served:       metrics.NewServedBindingRecorder(telemetry),
		Finishes:     finishes,
		PeerRPC:      adoption,
		Ledger:       recorder,
		Journal:      events,
		Now:          clock,
	})
	if err != nil {
		return nil, err
	}
	retention := newSessionRetention(storageDir, configuration.Server.SessionRetentionEpochs, gatewayStore, escrows)
	observer.Subscribe(func(snapshot chain.PhaseSnapshot) { retention.observe(ctx, snapshot) })
	observer.Subscribe(func(snapshot chain.PhaseSnapshot) { escrows.ShareChainMaxNonce(snapshot.MaxNonce) })
	hostStaleness := func() time.Duration {
		return time.Duration(configHolder.Load().Perf.HostStalenessSeconds) * time.Second
	}
	raceRecorder := metrics.NewRaceRecorder(telemetry, clock, hostStaleness)
	exhaustion := escrowExhaustion{escrows: escrows, router: router}
	manager, err := escrow.NewManager(escrow.Deps{
		Tx:          txClient,
		Store:       devshardWrites{Store: gatewayStore, changed: func() { notify(devshardWork) }},
		Snapshots:   observer,
		Settlement:  escrows,
		Timeouts:    escrows,
		Exhaustion:  exhaustion,
		Funds:       escrowFunds{escrows: escrows},
		ChainFacts:  txClient,
		Sweeps:      raceRecorder,
		Narrator:    events,
		Signer:      environmentSigner{},
		Config:      configHolder,
		Now:         clock,
		RoutePrefix: routePrefix,
	})
	if err != nil {
		return nil, err
	}
	depletion.manager = manager

	ledger, err := gatewayStore.NewLedger(store.Retention{
		MaxAge:  time.Duration(configuration.Accounting.RetentionHours) * time.Hour,
		MaxRows: configuration.Accounting.RetentionMaxRows,
	}, clock)
	if err != nil {
		return nil, err
	}

	suspicious, err := newSuspiciousHosts(ctx, gatewayStore)
	if err != nil {
		return nil, err
	}
	modelFilterSettings, err := newModelFilters(ctx, gatewayStore, clock)
	if err != nil {
		return nil, err
	}

	sessions := api.NewSessions(escrows)
	raceObserver := nonceAccountedRaces{recorder: raceRecorder, events: events}
	prober.Settle(sessions.Poster, probeVotes{recorder: raceRecorder, events: events}, events)
	e2e := env.LoadE2E()
	races, err := engine.NewEngine(engine.Deps{
		Picker:     router,
		Targets:    sessions,
		Windows:    participants,
		Perf:       hosts,
		Snapshots:  observer,
		Config:     configHolder,
		Metrics:    raceObserver,
		Ledger:     api.NewRaceLedger(ledger),
		Lifecycle:  manager,
		Journal:    events,
		Suspicious: suspicious.Suspicious,
		Timeouts:   sessions.Poster,
		Now:        clock,
		E2E:        engine.E2EOverrides{HardTimeout: e2e.StreamingHardTimeout},
	})
	if err != nil {
		return nil, err
	}
	finishes.attach(races)
	// One wrapper for both readers, so the gauge reports the scale admission actually applies.
	modelCapacities := modelCapacity{capacity: capacity, snapshots: observer, config: configHolder, participants: participants}
	telemetry.Register(
		metrics.NewLimitsCollector(metrics.LimitsSources{
			Limiter:       gatewayLimiter,
			Capacity:      modelCapacities,
			Participants:  participants,
			Models:        escrows.Models,
			BufferedBytes: buffers.Held,
			OwedVotes:     races.OwedTimeoutVotes,
		}),
		metrics.NewPerfCollector(hosts),
		metrics.NewRegistryCollector(metrics.RegistrySources{
			Escrows:            escrows,
			EscrowWeight:       capacity.EscrowWeight,
			Available:          participants.Available,
			DrainCloseFailures: escrows.DrainCloseFailures,
		}),
		metrics.NewChainCollector(observer, clock),
		metrics.NewTransportCollector(transport.DefaultHostConnectionTracker()),
		metrics.NewAccountingCollector(ledger),
		metrics.NewJournalCollector(journalCounts(events)),
		metrics.NewNonceLedgerCollector(recorder.UnknownEscrowFacts),
		metrics.NewFundingCollector(fundingModels(manager), deadlineCounts(manager)),
	)

	server, err := api.New(api.Deps{
		Config:     configHolder,
		Escrows:    escrows,
		Inference:  races,
		Limiter:    gatewayLimiter,
		Capacity:   modelCapacities,
		Snapshots:  observer,
		Control:    gatewayStore,
		Accounting: ledger,
		Operations: &operations{
			values:       values,
			config:       configHolder,
			store:        gatewayStore,
			escrows:      escrows,
			manager:      manager,
			participants: participants,
			storageDir:   storageDir,
			nonces:       recorder,
		},
		Suspicious:   suspicious,
		ModelFilters: modelFilterSettings,
		HostStates:   hosts,
		HostWindows:  participants,
		Telemetry:    telemetry,
		Buffers:      buffers,
		Rejections:   metrics.NewLimitRecorder(telemetry),
		CacheResults: metrics.NewCacheResultRecorder(telemetry),
		Journal:      events,
		StorageDir:   storageDir,
		Version:      Version,
		Now:          clock,

		StopTokenVocabulary:     stopTokenVocabulary(sources.ModelSnapshots, observer),
		PromptTokens:            promptTokens(sources.ModelSnapshots, observer),
		StatsTokenizerFallbacks: metrics.NewStatsTokenizerRecorder(telemetry),
	})
	if err != nil {
		return nil, err
	}
	// Registered after the server, which owns both sinks. See ../README.md, "Wiring order, and the knots in it".
	telemetry.Register(metrics.NewCaptureCollector(server))
	telemetry.Register(metrics.NewCacheCollector(server))
	httpServer, endRequests := server.HTTPServer(fmt.Sprintf(":%d", configuration.Server.Port))

	built = true
	return &gateway{
		config:       configHolder,
		store:        gatewayStore,
		observer:     observer,
		escrows:      escrows,
		manager:      manager,
		router:       router,
		races:        races,
		finishes:     finishes,
		limiter:      gatewayLimiter,
		participants: participants,
		telemetry:    telemetry,
		server:       httpServer,
		endRequests:  endRequests,
		publicAPI:    publicAPI,
		nonces:       recorder,
		events:       events,
		warmup:       prober,
		hostPings:    hostping.New(configuration.HostPing, escrows, metrics.NewHostPingRecorder(telemetry)),
		heights:      sources.Heights,
		governance:   sources.Governance,
		hosts:        sources.Hosts,
		builders:     boot.builders,
		devshardWork: devshardWork,
		bootSteps:    (*gateway).bootOrder,
	}, nil
}

func hostCapabilities(hosts *perf.Tracker, participants *limits.ParticipantLimiter, capacity *limits.Capacity) accounting.CapabilityFunc {
	return func(participant, model string) accounting.HostCapability {
		contextLimit, versionRefusals, toolRefusals, contextRefusals := hosts.Capability(participant, model)
		capability := accounting.HostCapability{
			ProtocolVersionUnsupported: versionRefusals > 0,
			ToolChoiceUnsupported:      toolRefusals > 0,
			ContextLimit:               contextLimit,
			VersionRefusals:            versionRefusals,
			ToolRefusals:               toolRefusals,
			ContextRefusals:            contextRefusals,
			OutputOverruns:             hosts.OutputOverruns(participant, model),
		}
		if window, tracked := participants.WindowFor(participant, model); tracked {
			capability.InputWindowTokens = uint64(max(window.InputWindowTokens, 0))
			capability.OutputWindowTokens = uint64(max(window.OutputWindowTokens, 0))
			capability.InflightInputTokens = uint64(max(window.InflightInputTokens, 0))
			capability.InflightOutputTokens = uint64(max(window.InflightOutputTokens, 0))
			capability.WindowCutoff = string(window.Cutoff)
			capability.WindowWeight = uint64(max(capacity.ParticipantWeight(participant, model), 0))
		}
		return capability
	}
}

type environmentSigner struct{}

func (environmentSigner) SignerFor(privateKeyEnv string) (*signing.Secp256k1Signer, error) {
	keyHex, err := env.PrivateKey(privateKeyEnv)
	if err != nil {
		return nil, err
	}
	return signing.SignerFromHex(keyHex)
}
