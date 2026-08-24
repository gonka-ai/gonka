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
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/adapters/chain"
	clockadapter "trainshard/internal/infrastructure/adapters/clock"
	"trainshard/internal/infrastructure/adapters/dapi"
	"trainshard/internal/infrastructure/adapters/signing/cosmos"
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

	signer, err := key(cfg)
	if err != nil {
		return err
	}
	if signer.Address() != vo.Address(cfg.participant) {
		return fmt.Errorf("the key signs as %s and this daemon speaks for %s: a node's mesh identity is only believed from its own participant", signer.Address(), cfg.participant)
	}

	outside, err := connect(cfg)
	if err != nil {
		return err
	}
	defer outside.close()

	state, err := localstate.New(cfg.stateDir)
	if err != nil {
		return err
	}

	parts, err := machinery(cfg, clock, log)
	if err != nil {
		return err
	}

	runs := hostdrun.New(hostdrun.Config{
		Participant: cfg.participant,
		Nodes:       cfg.nodes,
		Limits:      cfg.limits,
		Interval:    cfg.reconcileInterval,
		Patience:    cfg.prepareDeadline,
	}, hostdrun.Deps{
		Chain:        outside.chain,
		Reservations: outside.reservations,
		Submitter:    outside.submitter,
		Watcher:      outside.watcher,
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
			Control:    outside.control,
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
		Chain:     outside.chain,
		Submitter: outside.submitter,
		Clock:     clock,
		Log:       log,
	})

	sessions := session.New(session.Config{Participant: cfg.participant}, session.Deps{
		Chain:    outside.chain,
		Streams:  parts.streams,
		Sessions: state.Sessions(),
		Served:   state.Served(clock),
		Clock:    clock,
	})

	mux := http.NewServeMux()
	guard, logged := signedhttp.New(signer, clock, cfg.signatureWindow, vo.Address(cfg.participant)).Wrap, httpx.Log(log, clock)
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

type keys interface {
	Address() vo.Address
	Sign(payload []byte) []byte
	Attest(ctx context.Context, payload []byte) ([]byte, error)
	Recover(payload, signature []byte) (vo.Address, error)
}

func key(cfg config) (keys, error) {
	if cfg.privateKey != "" {
		return cosmos.FromHex(cfg.privateKey)
	}
	return cosmos.FromKeyring(cfg.keyringDir, cfg.keyringBackend, cfg.keyringPassword, cfg.keyName)
}

type outside struct {
	chain        shard.ChainReader
	watcher      shard.ChainWatcher
	submitter    shard.ChainSubmitter
	reservations run.Reservations
	control      run.NodeControl
	close        func() error
}

// reservations reads from the chain and writes through the dapi, because a release is a transaction
// and this machine holds no key
type reservations struct {
	*chain.Client
	dapi *dapi.Client
}

func (r reservations) Release(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error {
	return r.dapi.Release(ctx, shardID, node, reason)
}

func connect(cfg config) (outside, error) {
	client, err := chain.Dial(chain.Config{Address: cfg.chainGRPC, Poll: cfg.chainPoll})
	if err != nil {
		return outside{}, err
	}
	node := dapi.New(&http.Client{}, dapi.Config{
		Address:     cfg.dapiAddress,
		Participant: cfg.participant,
		Timeout:     cfg.dapiTimeout,
	})
	return outside{
		chain: client, watcher: client, submitter: node,
		reservations: reservations{Client: client, dapi: node},
		control:      node,
		close:        client.Close,
	}, nil
}
