package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"trainshard/internal/application/hostd/node"
	hostdrun "trainshard/internal/application/hostd/run"
	"trainshard/internal/application/hostd/session"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
	chainfake "trainshard/internal/infrastructure/adapters/chain/fake"
	clockadapter "trainshard/internal/infrastructure/adapters/clock"
	nodemanagerfake "trainshard/internal/infrastructure/adapters/nodemanager/fake"
	"trainshard/internal/infrastructure/adapters/signing/hmac"
	"trainshard/internal/infrastructure/repositories/localstate"
	"trainshard/internal/utils/httpx"
	"trainshard/internal/utils/logger"
	"trainshard/internal/utils/signedhttp"
)

var version = "dev"

const shutdownGrace = 15 * time.Second

func main() {
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve() error {
	if len(os.Args) > 1 && slices.Contains([]string{"--version", "-version"}, os.Args[1]) {
		fmt.Println(version)
		return nil
	}

	cfg, err := load()
	if err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.logLevel, cfg.logFormat)
	clock := clockadapter.System{}

	chain, err := loadChain(cfg, clock)
	if err != nil {
		return err
	}
	state, err := localstate.New(cfg.stateDir)
	if err != nil {
		return err
	}

	parts, err := machinery(cfg, clock, log)
	if err != nil {
		return err
	}
	nodeManager := nodemanagerfake.New(log)
	signer := hmac.New(cfg.secret, vo.Address(cfg.participant))

	if cfg.machine != "memory" {
		log.Warn("the chain, the node manager and the signer are stand-ins: this daemon reserves nothing, releases nothing, stops no inference, and anyone holding the shared secret can sign as any actor")
	}

	runs := hostdrun.New(hostdrun.Config{
		Participant: cfg.participant,
		Nodes:       cfg.nodes,
		Limits:      cfg.limits,
		Interval:    cfg.reconcileInterval,
		Patience:    cfg.prepareDeadline,
	}, hostdrun.Deps{
		Chain:        chain,
		Reservations: chain,
		Submitter:    chain,
		Watcher:      chain,
		Runs:         state.Runs(),
		Requests:     state.Requests(clock, cfg.requestTTL),
		Store:        state.Mesh(),
		Network:      parts.network,
		Machine: run.Machine{
			Images:     parts.images,
			Containers: parts.containers,
			Volumes:    parts.volumes,
			GPU:        parts.gpu,
			Mesh:       mesh.Runtime{Network: parts.network, Store: state.Mesh(), Attestor: signer},
			Egress:     parts.egress,
			Control:    nodeManager,
			Runs:       state.Runs(),
			Clock:      clock,
			StopGrace:  cfg.stopGrace,
		},
		Clock: clock,
		Log:   log,
	})

	nodes := node.New(node.Config{
		Nodes:            cfg.nodes,
		Version:          version,
		SupportedVersion: cfg.supportedVersion,
		MinFreeDiskBytes: cfg.minFreeDiskBytes,
		OptInTTL:         cfg.optInTTL,
		RefreshInterval:  cfg.refreshInterval,
	}, node.Deps{
		Probe:     parts.probe,
		GPU:       parts.gpu,
		Chain:     chain,
		Submitter: chain,
		Log:       log,
	})

	sessions := session.New(session.Config{Participant: cfg.participant, Window: cfg.signatureWindow}, session.Deps{
		Chain:    chain,
		Streams:  parts.streams,
		Volumes:  parts.volumes,
		Sessions: state.Sessions(),
		Clock:    clock,
	})

	mux := http.NewServeMux()
	guard, logged := signedhttp.New(signer, clock, cfg.signatureWindow).Wrap, httpx.Log(log, clock)
	boundary := func(next http.Handler) http.Handler { return logged(guard(next)) }
	runs.Mount(mux, boundary)
	nodes.Mount(mux, boundary)
	sessions.Mount(mux, boundary)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); runs.Run(ctx) }()
	go func() { defer workers.Done(); nodes.Run(ctx) }()

	server := &http.Server{Addr: cfg.listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Info("trainshardd listening", "participant", cfg.participant, "version", version,
		"listen", cfg.listen, "admin", cfg.admin, "nodes", len(cfg.nodes), "machine", cfg.machine)

	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()

	var admin *http.Server
	if cfg.admin != "" {
		adminMux := http.NewServeMux()
		runs.MountAdmin(adminMux)
		admin = &http.Server{Addr: cfg.admin, Handler: adminMux, ReadHeaderTimeout: 10 * time.Second}
		go func() { served <- admin.ListenAndServe() }()
	}

	select {
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}

	shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	err = server.Shutdown(shutdown)
	if admin != nil {
		err = errors.Join(err, admin.Shutdown(shutdown))
	}
	workers.Wait()
	log.Info("trainshardd stopped")
	return err
}

func loadChain(cfg config, clock clockadapter.System) (*chainfake.Chain, error) {
	if cfg.chainSeed == "" {
		return chainfake.New(clock), nil
	}
	return chainfake.Load(cfg.chainSeed, clock)
}
