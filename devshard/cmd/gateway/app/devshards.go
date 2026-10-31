package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	commonchain "common/chain"
	"common/vocabulary"
	"golang.org/x/sync/errgroup"

	"devshard/bridge"
	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/heights"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/nonces"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
	"devshard/logging"
	"devshard/runtimeparams"
	"devshard/transport"
	"devshard/user"
)

// chainBackedSessions owns the chain connection because it is the only provider that needs one.
func chainBackedSessions(records devshardLookup, storageDir string) sessionSources {
	return func(endpoints config.Chain, heightSettings config.HeightSync, routePrefix string, adoption *transport.PeerRPCAdoption, steps user.StepTimer) (chainSources, error) {
		// This client carries the CometBFT RPC query fallback. See ../README.md, "Escrow sessions and the chain connection".
		chainClient, err := commonchain.NewWithQueryFallback(endpoints.GRPCEndpoint, endpoints.RPCEndpoint)
		if err != nil {
			return chainSources{}, fmt.Errorf("dialing chain grpc %s: %w", endpoints.GRPCEndpoint, err)
		}
		grpcChain := chain.NewGRPCChain(chainClient, endpoints.ChainID)
		governanceSettings := runtimeparams.SettingsFromEnv()
		follower, err := heights.NewOracle(heightSettings, heights.OracleSources{
			NodeManagerAddr: governanceSettings.NodeManagerAddr,
			CometRPC:        endpoints.RPCEndpoint,
			Chain:           chainClient,
		})
		if err != nil {
			return chainSources{}, err
		}
		governance, err := openRuntimeParams(chainClient, governanceSettings)
		if err != nil {
			return chainSources{}, errors.Join(err, follower.Close())
		}
		escrowBridge := bridge.NewGRPCBridge(chainClient)
		return chainSources{
			Serving:    servingSessions(records, storageDir, escrowBridge, heightSettings, follower, governance, routePrefix, adoption, steps),
			ReadOnly:   readOnlySessions(records, storageDir),
			Reader:     grpcChain,
			Transport:  grpcChain,
			Heights:    follower,
			Governance: governance,

			ModelSnapshots: grpcChain,
			Hosts:          escrowBridge,
		}, nil
	}
}

func resolveStorageDir(explicit *string) (string, error) {
	if explicit != nil {
		return *explicit, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir for storage: %w", err)
	}
	return filepath.Join(homeDir, ".cache", "gonka-gateway"), nil
}

// publishEscrows brings the registry to what the store calls active, builders at a time. See operations.md, "Boot".
func (g *gateway) publishEscrows(ctx context.Context) (int, error) {
	// Taken before the list, so an escrow parked after the list read its row active is not reopened from it.
	listing := g.escrows.Listing()
	defer listing.Close()
	records, err := g.store.ListDevshards(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing devshards: %w", err)
	}
	add := func(ctx context.Context, record store.DevshardRecord) error { return addServing(ctx, listing, record) }
	return publishEscrows(ctx, records, g.builders, add, g.escrows.Retire, unservableDeactivation(ctx, g.store), g.events.EscrowUnservable, g.events.EscrowUnopened)
}

type devshardDeactivator interface {
	MarkDevshardGoneFromChain(ctx context.Context, escrowID string) error
	SetDevshardActive(ctx context.Context, escrowID string, active bool) error
	DeleteDevshard(ctx context.Context, escrowID string) error
}

// unservableDeactivation marks a row the chain lacks gone from chain and drops one it holds settled, as settlement would, so neither stays in the unsettled count; any other unservable row is only deactivated.
func unservableDeactivation(ctx context.Context, records devshardDeactivator) func(escrowID string, cause error) error {
	return func(escrowID string, cause error) error {
		switch {
		case errors.Is(cause, bridge.ErrEscrowNotFound):
			return records.MarkDevshardGoneFromChain(ctx, escrowID)
		case errors.Is(cause, bridge.ErrEscrowSettled):
			return records.DeleteDevshard(ctx, escrowID)
		}
		return records.SetDevshardActive(ctx, escrowID, false)
	}
}

type escrowAdder interface {
	Add(ctx context.Context, escrowID, model string) error
	AddReserve(ctx context.Context, escrowID, model string) error
}

// addServing publishes an active row, a reserve as one, so every path that routes a row reads its role.
func addServing(ctx context.Context, escrows escrowAdder, record store.DevshardRecord) error {
	if record.RotationRole == escrow.RoleReserve {
		return escrows.AddReserve(ctx, record.EscrowID, record.Model)
	}
	return escrows.Add(ctx, record.EscrowID, record.Model)
}

func publishEscrows(
	ctx context.Context,
	records []store.DevshardRecord,
	builders int,
	add func(ctx context.Context, record store.DevshardRecord) error,
	retire func(escrowID string) error,
	deactivate func(escrowID string, cause error) error,
	unservable func(escrowID string, err error),
	unopened func(escrowID string, err error),
) (int, error) {
	var (
		active   []store.DevshardRecord
		problems []error
	)
	for _, record := range records {
		if record.Active {
			active = append(active, record)
			continue
		}
		if err := retire(record.EscrowID); err != nil {
			problems = append(problems, fmt.Errorf("retiring inactive escrow %s: %w", record.EscrowID, err))
		}
	}

	built := make([]error, len(active))
	var building errgroup.Group
	building.SetLimit(builders)
	for index, record := range active {
		building.Go(func() error {
			built[index] = add(ctx, record)
			return nil
		})
	}
	_ = building.Wait()

	unopenedCount := 0
	for index, err := range built {
		escrowID := active[index].EscrowID
		switch {
		case err == nil:
		case errors.Is(err, bridge.ErrEscrowNotFound), errors.Is(err, bridge.ErrEscrowSettled), errors.Is(err, env.ErrPrivateKeyMissing):
			unservable(escrowID, err)
			if deactivateErr := deactivate(escrowID, err); deactivateErr != nil {
				problems = append(problems, fmt.Errorf("deactivating escrow %s: %w", escrowID, deactivateErr))
			}
		default:
			unopened(escrowID, err)
			unopenedCount++
		}
	}
	return unopenedCount, errors.Join(problems...)
}

// republishOnDevshardWrites keeps routing in step with the rotation lifecycle, which knows no registry.
func (g *gateway) republishOnDevshardWrites(ctx context.Context, unopenedAtBoot int) <-chan struct{} {
	var reopen func() bool
	if g.hosts != nil {
		reopen = func() bool { return retireListedAbsentHosts(g.hosts, g.router, g.escrows) }
	}
	return republishEscrows(ctx, g.devshardWork, unopenedAtBoot, g.publishEscrows, reopen)
}

type absentHostMarks interface {
	AbsentHosts() map[string][]string
	ForgetAbsentHosts(escrowID string)
}

type liveEscrows interface {
	Routable(escrowID string) (scheduler.Escrow, bool)
	Candidates(model string) []scheduler.Escrow
	Snapshot() []registry.EscrowState
	IsBusy(escrowID string) bool
	Retire(escrowID string) error
}

// retireListedAbsentHosts retires each live escrow holding a dead slot whose participant the chain lists again, and reports whether it retired one, so the republish reopens it. See routing.md, "A slot the chain did not list at open".
func retireListedAbsentHosts(hosts HostDirectory, marks absentHostMarks, escrows liveEscrows) bool {
	listed := map[string]bool{}
	isListed := func(participant string) bool {
		known, probed := listed[participant]
		if !probed {
			_, err := hosts.GetHostInfo(participant)
			known = err == nil
			listed[participant] = known
		}
		return known
	}
	retired := false
	marked := marks.AbsentHosts()
	for escrowID, participants := range marked {
		candidate, live := escrows.Routable(escrowID)
		if !live {
			marks.ForgetAbsentHosts(escrowID)
			continue
		}
		if !slices.ContainsFunc(participants, isListed) || !modelServedThroughDrain(escrows, marked, candidate) {
			continue
		}
		logging.Info("reopening escrow: a participant absent at its open is listed again", logkey.Escrow, escrowID)
		if err := escrows.Retire(escrowID); err != nil {
			logging.Error("retiring escrow to reopen it", logkey.Escrow, escrowID, logkey.Error, err)
		}
		marks.ForgetAbsentHosts(escrowID)
		retired = true
	}
	return retired
}

// unopenedEscrowRetryDelay is the lifecycle tick, so an escrow that would not open is retried as often as rotation looks at it.
const unopenedEscrowRetryDelay = escrow.TickInterval

// absentHostProbeInterval bounds the chain lookups for dead slots to one per marked participant per lifecycle tick.
const absentHostProbeInterval = escrow.TickInterval

// modelServedThroughDrain reports whether retiring the escrow leaves its model served: it closes at once, another of the model's escrows with no absent mark routes, or another routes while none is out of routing.
func modelServedThroughDrain(escrows liveEscrows, marked map[string][]string, candidate scheduler.Escrow) bool {
	if !escrows.IsBusy(candidate.ID) {
		return true
	}
	others := slices.DeleteFunc(escrows.Candidates(candidate.Model), func(other scheduler.Escrow) bool { return other.ID == candidate.ID })
	if slices.ContainsFunc(others, func(other scheduler.Escrow) bool { return len(marked[other.ID]) == 0 }) {
		return true
	}
	for _, state := range escrows.Snapshot() {
		if state.Model == candidate.Model && !state.Accepting {
			return false
		}
	}
	return len(others) > 0
}

// republishEscrows republishes on every devshard write, on a timer while an active escrow is left unopened, and once reopen retires an escrow to reopen it. See operations.md, "Boot".
func republishEscrows(ctx context.Context, writes <-chan struct{}, unopenedAtBoot int, publish func(context.Context) (int, error), reopen func() bool) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		retry := time.NewTimer(unopenedEscrowRetryDelay)
		defer retry.Stop()
		if unopenedAtBoot == 0 {
			retry.Stop()
		}
		var probe <-chan time.Time
		if reopen != nil {
			probes := time.NewTicker(absentHostProbeInterval)
			defer probes.Stop()
			probe = probes.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-writes:
			case <-retry.C:
			case <-probe:
				if !reopen() {
					continue
				}
			}
			unopened, err := publish(ctx)
			if err != nil && ctx.Err() == nil {
				logging.Error("republishing escrows", logkey.Error, err)
			}
			if unopened > 0 {
				retry.Reset(unopenedEscrowRetryDelay)
			} else {
				retry.Stop()
			}
		}
	}()
	return done
}

func notify(work chan struct{}) {
	select {
	case work <- struct{}{}:
	default:
	}
}

// devshardWrites reports changed rows, so a replacement escrow routes without waiting for a poll.
type devshardWrites struct {
	*store.Store
	changed func()
}

func (w devshardWrites) UpsertDevshard(ctx context.Context, record store.DevshardRecord) error {
	return w.report(w.Store.UpsertDevshard(ctx, record))
}

func (w devshardWrites) SetDevshardActive(ctx context.Context, escrowID string, active bool) error {
	return w.report(w.Store.SetDevshardActive(ctx, escrowID, active))
}

func (w devshardWrites) MarkDevshardGoneFromChain(ctx context.Context, escrowID string) error {
	return w.report(w.Store.MarkDevshardGoneFromChain(ctx, escrowID))
}

func (w devshardWrites) DeleteDevshard(ctx context.Context, escrowID string) error {
	return w.report(w.Store.DeleteDevshard(ctx, escrowID))
}

// ParkForSettlement reports the park, so a pass that listed the row active before it is followed by one that does not.
func (w devshardWrites) ParkForSettlement(ctx context.Context, escrowID string) error {
	return w.report(w.Store.ParkForSettlement(ctx, escrowID))
}

func (w devshardWrites) ParkForSettlementIfActive(ctx context.Context, escrowID string) (bool, error) {
	parked, err := w.Store.ParkForSettlementIfActive(ctx, escrowID)
	if parked {
		w.changed()
	}
	return parked, err
}

func (w devshardWrites) report(err error) error {
	if err == nil {
		w.changed()
	}
	return err
}

// depletionNotice breaks the registry/manager cycle for both rotation notices: the manager settles through the registry.
type depletionNotice struct{ manager *escrow.Manager }

func (d *depletionNotice) OnBalanceExhausted(escrowID string, reason scheduler.ExhaustionReason) {
	if d.manager != nil {
		d.manager.OnBalanceExhausted(escrowID, reason)
	}
}

func (d *depletionNotice) OnReserveTaken(escrowID string) {
	if d.manager != nil {
		d.manager.OnReserveTaken(escrowID)
	}
}

type escrowEpochLookup interface {
	GetEscrow(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error)
}

type devshardLister interface {
	ListDevshards(ctx context.Context) ([]store.DevshardRecord, error)
}

// creationEpochOf resolves the epoch the chain stamped on an escrow: the stored chain epoch, else the chain's answer, else the row's label. See ../nonces/README.md, "The judgements it does make".
func creationEpochOf(chainClient escrowEpochLookup, records devshardLister) nonces.CreationEpochFunc {
	return func(ctx context.Context, escrowID string) (uint64, bool) {
		var row store.DevshardRecord
		stored := false
		if devshards, err := records.ListDevshards(ctx); err == nil {
			for _, record := range devshards {
				if record.EscrowID == escrowID {
					row, stored = record, true
					break
				}
			}
		}
		if stored && row.ChainEpoch > 0 {
			return row.ChainEpoch, true
		}
		if info, found, err := chainClient.GetEscrow(ctx, escrowID); err == nil && found && info.EpochIndex > 0 {
			return info.EpochIndex, true
		}
		if stored && row.RotationEpoch > 0 {
			return uint64(row.RotationEpoch), true
		}
		return 0, false
	}
}

// sessionSources is a parameter so the transport an escrow is served over is chosen once, at compose.
type sessionSources func(endpoints config.Chain, heightSettings config.HeightSync, routePrefix string, adoption *transport.PeerRPCAdoption, steps user.StepTimer) (chainSources, error)

// chainSources is what one dial yields; Reader and Transport are interfaces so a test dials nothing.
type chainSources struct {
	Serving        registry.SessionFactory
	ReadOnly       registry.SessionFactory
	Reader         chain.Reader
	Transport      chain.Transport
	Heights        *heights.Oracle
	Governance     *runtimeParams
	PublicAPI      *http.Client
	ModelSnapshots vocabulary.EpochGroupDataQuery
	Hosts          HostDirectory
}

// HostDirectory is the chain's participant lookup, the one a session open asks.
type HostDirectory interface {
	GetHostInfo(address string) (*bridge.HostInfo, error)
}

type seedDevshard struct {
	EscrowID      string `json:"id"`
	PrivateKeyEnv string `json:"private_key_env"`
	Model         string `json:"model"`
	RoutePrefix   string `json:"route_prefix"`
}
type devshardRegistry interface {
	ListDevshards(ctx context.Context) ([]store.DevshardRecord, error)
	UpsertDevshard(ctx context.Context, record store.DevshardRecord) error
}

// seedDevshards leaves a known devshard alone, so a restart cannot resurrect a deactivated one.
func seedDevshards(ctx context.Context, records devshardRegistry, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var seeds []seedDevshard
	if err := json.Unmarshal([]byte(raw), &seeds); err != nil {
		return fmt.Errorf("parsing seed devshards: %w", err)
	}
	known, err := records.ListDevshards(ctx)
	if err != nil {
		return fmt.Errorf("listing devshards: %w", err)
	}
	registered := make(map[string]bool, len(known))
	for _, record := range known {
		registered[record.EscrowID] = true
	}
	for _, seed := range seeds {
		switch {
		case !isChainEscrowID(seed.EscrowID):
			return fmt.Errorf("seed devshard: %w: %q", api.ErrInvalidEscrowID, seed.EscrowID)
		case strings.TrimSpace(seed.Model) == "":
			return fmt.Errorf("seed devshard %s: model is required", seed.EscrowID)
		case strings.TrimSpace(seed.PrivateKeyEnv) == "":
			return fmt.Errorf("seed devshard %s: private_key_env is required", seed.EscrowID)
		case registered[seed.EscrowID]:
			continue
		}
		record := store.DevshardRecord{
			EscrowID:      seed.EscrowID,
			PrivateKeyEnv: seed.PrivateKeyEnv,
			Model:         seed.Model,
			Active:        true,
			RoutePrefix:   strings.TrimSpace(seed.RoutePrefix),
		}
		if err := records.UpsertDevshard(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

type devshardLookup interface {
	ListDevshards(ctx context.Context) ([]store.DevshardRecord, error)
}

func findDevshard(ctx context.Context, records devshardLookup, escrowID string) (store.DevshardRecord, error) {
	known, err := records.ListDevshards(ctx)
	if err != nil {
		return store.DevshardRecord{}, fmt.Errorf("listing devshards: %w", err)
	}
	for _, record := range known {
		if record.EscrowID == escrowID {
			return record, nil
		}
	}
	return store.DevshardRecord{}, fmt.Errorf("devshard %s: %w", escrowID, escrow.ErrUnknownEscrow)
}

// sessionInputs resolves what both session factories need before they diverge on how they reach hosts.
func sessionInputs(ctx context.Context, records devshardLookup, storageDir, escrowID string) (store.DevshardRecord, string, string, error) {
	record, err := findDevshard(ctx, records, escrowID)
	if err != nil {
		return store.DevshardRecord{}, "", "", err
	}
	keyHex, err := env.PrivateKey(record.PrivateKeyEnv)
	if err != nil {
		return store.DevshardRecord{}, "", "", err
	}
	storagePath, err := escrowStorage(storageDir, escrowID)
	if err != nil {
		return store.DevshardRecord{}, "", "", err
	}
	return record, keyHex, storagePath, nil
}

// The bridge is one object for the process. See ../README.md, "Escrow sessions and the chain connection".
func servingSessions(records devshardLookup, storageDir string, escrowBridge bridge.MainnetBridge, heightSettings config.HeightSync, follower *heights.Oracle, governance *runtimeParams, routePrefix string, adoption *transport.PeerRPCAdoption, steps user.StepTimer) registry.SessionFactory {
	return func(ctx context.Context, escrowID string) (registry.EscrowSession, error) {
		record, keyHex, storagePath, err := sessionInputs(ctx, records, storageDir, escrowID)
		if err != nil {
			return nil, err
		}
		// A boot seed writes back the row settlement dropped, so the chain decides. See ../docs/operations.md, "Boot".
		onChain, err := escrowBridge.GetEscrow(escrowID)
		switch {
		case err != nil:
			return nil, fmt.Errorf("get escrow: %w", err)
		case onChain.Settled:
			return nil, fmt.Errorf("escrow %s: %w", escrowID, bridge.ErrEscrowSettled)
		}
		sessionTimeouts := env.LoadE2E().SessionTimeouts
		session, machine, err := user.NewHTTPSession(user.HTTPSessionConfig{
			OpenAbsentHostsAsDeadSlots: true,
			PrivateKeyHex:              keyHex,
			EscrowID:                   escrowID,
			Bridge:                     escrowBridge,
			Escrow:                     onChain,
			StoragePath:                storagePath,
			RoutePrefix:                escrowRoutePrefix(record, routePrefix),
			RefusalTimeoutSeconds:      sessionTimeouts.RefusalTimeoutSeconds,
			ExecutionTimeoutSeconds:    sessionTimeouts.ExecutionTimeoutSeconds,
			RequireHeightSeed:          heightSettings.Enabled && heightSettings.RequireSeed,
			DisableHeightSyncHeartbeat: !heightSettings.Heartbeat,
			ExtraClientConfig:          peerRPCClientConfig(heights.BuildCourier(heightSettings, follower), adoption),
			Heartbeat:                  governance.Heartbeat(),
			QuietSettleDelay:           time.Duration(heightSettings.QuietSettleDelayMS) * time.Millisecond,
			StepTimer:                  steps,
		})
		if err != nil {
			return nil, err
		}
		heights.StartCadence(session, heightSettings)
		return registry.NewSessionHandle(session, machine), nil
	}
}

func escrowRoutePrefix(record store.DevshardRecord, gatewayPrefix string) string {
	if record.RoutePrefix != "" {
		return record.RoutePrefix
	}
	logging.Warn("escrow route prefix unpinned, following the gateway",
		logkey.Escrow, record.EscrowID, logkey.RoutePrefix, gatewayPrefix)
	return gatewayPrefix
}

func readOnlySessions(records devshardLookup, storageDir string) registry.SessionFactory {
	return func(ctx context.Context, escrowID string) (registry.EscrowSession, error) {
		_, keyHex, storagePath, err := sessionInputs(ctx, records, storageDir, escrowID)
		if err != nil {
			return nil, err
		}
		session, machine, err := user.NewLocalSession(user.LocalSessionConfig{
			PrivateKeyHex: keyHex,
			EscrowID:      escrowID,
			StoragePath:   storagePath,
		})
		if err != nil {
			return nil, err
		}
		return registry.NewSessionHandle(session, machine), nil
	}
}

const (
	storageDirectoryMode = 0o755
	storageFileMode      = 0o600
)

func escrowStorage(storageDir, escrowID string) (string, error) {
	storagePath := api.DevshardStoragePath(storageDir, escrowID)
	if err := os.MkdirAll(storagePath, storageDirectoryMode); err != nil {
		return "", fmt.Errorf("creating storage for escrow %s: %w", escrowID, err)
	}
	return storagePath, nil
}

// copySessionStorage copies regular files only: a directory below a flat SQLite set is not the escrow's.
func copySessionStorage(sourceDir, targetDir string) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("reading session storage %s: %w", sourceDir, err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(sourceDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(targetDir, entry.Name()), contents, storageFileMode); err != nil {
			return fmt.Errorf("writing %s: %w", entry.Name(), err)
		}
	}
	return nil
}

const chatCompressMinBytes = 4 << 10

func peerRPCClientConfig(clientConfig *transport.ClientConfig, adoption *transport.PeerRPCAdoption) *transport.ClientConfig {
	if clientConfig == nil {
		clientConfig = &transport.ClientConfig{}
	}
	clientConfig.RPCAdoption = adoption
	clientConfig.ChatCompressMinBytes = chatCompressMinBytes
	return clientConfig
}
