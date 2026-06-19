package devshard

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/labstack/echo/v4"

	"decentralized-api/internal/validation"
	"decentralized-api/logging"
	"decentralized-api/payloadstorage"
	"decentralized-api/utils"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	inferenceTypes "github.com/productscience/inference/x/inference/types"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/host"
	"devshard/observability"
	devshardserver "devshard/server"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"
)

// HostManager manages per-escrow devshard sessions with lazy creation.
type HostManager struct {
	mu       sync.RWMutex
	sessions map[string]*transport.Server
	sf       singleflight.Group

	readyMu      sync.RWMutex
	initializing bool
	initErr      error

	store        storage.Storage
	signer       *signing.Secp256k1Signer
	verifier     signing.Verifier
	engine       devshardpkg.InferenceEngine
	validator    devshardpkg.ValidationEngine
	availability devshardpkg.AvailabilityProvider
	maxNonce     devshardpkg.MaxNonceProvider
	params       RuntimeParamsProvider
	boundVersion string
	bridge       bridge.MainnetBridge
	payloadStore payloadstorage.PayloadStorage
	pruneSink    host.PruneEventSink
	recorder     PayloadAuthClient

	statsMu           sync.Mutex
	statsShardsCache  *statsShardsResponse
	statsShardsCached time.Time
	statsDetailsCache map[string]statsShardDetailCache
}

const (
	recoverSessionsConcurrency = 8
	statsCacheTTL              = 60 * time.Second
)

type statsShardDetailCache struct {
	response *statsShardDetailResponse
	cached   time.Time
}

type statsShardsResponse struct {
	CurrentEpochID  uint64              `json:"current_epoch_id"`
	CachedAt        int64               `json:"cached_at"`
	CacheTTLSeconds int64               `json:"cache_ttl_seconds"`
	ActiveEscrows   []string            `json:"active_escrows"`
	Shards          []statsShardSummary `json:"shards"`
}

type statsShardSummary struct {
	EscrowID string `json:"escrow_id"`
	EpochID  uint64 `json:"epoch_id"`
}

type statsShardDetailResponse struct {
	EscrowID                    string                       `json:"escrow_id"`
	EpochID                     uint64                       `json:"epoch_id"`
	Nonce                       uint64                       `json:"nonce"`
	Version                     string                       `json:"version"` // versiond runtime bind (m.boundVersion)
	StateRootAndProtocolVersion string                       `json:"state_root_and_protocol_version"`
	CachedAt                    int64                        `json:"cached_at"`
	CacheTTLSeconds             int64                        `json:"cache_ttl_seconds"`
	HostStats                   map[uint32]statsHostStats    `json:"host_stats"`
	ValidationObservability     statsValidationObservability `json:"validation_observability"`
	Group                       []types.SlotAssignment       `json:"group"`
}

type statsHostStats struct {
	Missed               uint32 `json:"missed"`
	Invalid              uint32 `json:"invalid"`
	Cost                 uint64 `json:"cost"`
	RequiredValidations  uint32 `json:"required_validations"`
	CompletedValidations uint32 `json:"completed_validations"`
}

// statsValidationTotals aggregates per-slot observability rows; uint64 avoids wrap
// when summing many slots (per-slot counters remain uint32).
type statsValidationTotals struct {
	RequiredValidations  uint64 `json:"required_validations"`
	CompletedValidations uint64 `json:"completed_validations"`
}

// statsValidationObservability exposes validation counters persisted outside the
// state root (survives host restart; not used for settlement).
type statsValidationObservability struct {
	BySlot map[uint32]statsHostStats `json:"by_slot"`
	Totals statsValidationTotals     `json:"totals"`
}

func NewHostManager(
	store storage.Storage,
	signer *signing.Secp256k1Signer,
	engine devshardpkg.InferenceEngine,
	validator devshardpkg.ValidationEngine,
	boundVersion string,
	br bridge.MainnetBridge,
	payloadStore payloadstorage.PayloadStorage,
	recorder PayloadAuthClient,
) *HostManager {
	m := &HostManager{
		sessions:          make(map[string]*transport.Server),
		initializing:      true,
		store:             store,
		signer:            signer,
		verifier:          signing.NewSecp256k1Verifier(),
		engine:            engine,
		validator:         validator,
		boundVersion:      boundVersion,
		bridge:            br,
		payloadStore:      payloadStore,
		recorder:          recorder,
		statsDetailsCache: make(map[string]statsShardDetailCache),
	}
	// Wire the payload prune sink. When payloadStore is nil (tests, tools)
	// the sink is nil and hosts will not emit any prune events.
	if payloadStore != nil {
		m.pruneSink = newPayloadPruneSink(payloadStore, fallbackEpochFromStore(store))
	}
	return m
}

// Close drains the payload prune worker pool (when configured) and releases
// session storage resources.
func (m *HostManager) Close() error {
	if sink, ok := m.pruneSink.(*payloadPruneSink); ok {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), pruneShutdownTimeout)
		if err := sink.shutdown(shutdownCtx); err != nil {
			logging.Warn("payload prune sink shutdown timed out", inferenceTypes.PayloadStorage,
				"error", err)
		}
		cancel()
	}
	return m.store.Close()
}

func (m *HostManager) SetInitializing() {
	m.readyMu.Lock()
	defer m.readyMu.Unlock()
	m.initializing = true
	m.initErr = nil
}

func (m *HostManager) SetReady() {
	m.readyMu.Lock()
	defer m.readyMu.Unlock()
	m.initializing = false
	m.initErr = nil
}

func (m *HostManager) SetUnavailable(err error) {
	m.readyMu.Lock()
	defer m.readyMu.Unlock()
	m.initializing = true
	m.initErr = err
}

func (m *HostManager) SetAvailabilityProvider(p devshardpkg.AvailabilityProvider) {
	m.availability = p
}

// SetMaxNonceProvider enforces chain max_nonce on every host (with finalization reserve).
func (m *HostManager) SetMaxNonceProvider(p devshardpkg.MaxNonceProvider) {
	m.maxNonce = p
}

// SetRuntimeParamsProvider supplies the live long-poll-backed view of session
// governance params, read at HostManager.create to freeze bind-time fields.
// freeze ValidationRate / grace / VoteThreshold onto the bound SessionConfig.
// Until then the provider is captured but not consulted, so wiring this in
// dapi/devshardd is a no-op for behavior.
func (m *HostManager) SetRuntimeParamsProvider(p RuntimeParamsProvider) {
	m.params = p
}

// SessionServer resolves or creates the per-escrow transport server.
func (m *HostManager) SessionServer(escrowID string) (*transport.Server, error) {
	if err := m.readinessError(); err != nil {
		return nil, err
	}
	return m.getOrCreate(escrowID)
}

func (m *HostManager) readinessError() error {
	m.readyMu.RLock()
	defer m.readyMu.RUnlock()
	if !m.initializing {
		return nil
	}
	if m.initErr != nil {
		return fmt.Errorf("%w: %v", devshardserver.ErrInitializing, m.initErr)
	}
	return devshardserver.ErrInitializing
}

func (m *HostManager) getOrCreate(escrowID string) (*transport.Server, error) {
	if srv, ok := m.session(escrowID); ok {
		return srv, nil
	}

	v, err, _ := m.sf.Do(escrowID, func() (interface{}, error) {
		if srv, ok := m.session(escrowID); ok {
			return srv, nil
		}

		srv, err := m.recoverStoredSession(escrowID)
		if err == nil {
			return m.storeSessionIfAbsent(escrowID, srv), nil
		}
		if !errors.Is(err, storage.ErrSessionNotFound) {
			return nil, err
		}

		srv, err = m.create(escrowID)
		if err != nil {
			return nil, err
		}

		return m.storeSessionIfAbsent(escrowID, srv), nil
	})

	if err != nil {
		return nil, err
	}
	return v.(*transport.Server), nil
}

func (m *HostManager) session(escrowID string) (*transport.Server, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	srv, ok := m.sessions[escrowID]
	return srv, ok
}

func (m *HostManager) storeSessionIfAbsent(escrowID string, srv *transport.Server) *transport.Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.sessions[escrowID]; ok {
		return existing
	}
	m.sessions[escrowID] = srv
	return srv
}

func (m *HostManager) create(escrowID string) (*transport.Server, error) {
	escrow, err := m.bridge.GetEscrow(escrowID)
	if err != nil {
		return nil, fmt.Errorf("get escrow: %w", err)
	}

	group, err := bridge.BuildGroupFromEscrow(escrow)
	if err != nil {
		return nil, fmt.Errorf("build group: %w", err)
	}

	creatorAddr := escrow.CreatorAddress

	config := types.SessionConfigFromEscrow(len(group), types.EscrowSessionFields{
		TokenPrice:                escrow.TokenPrice,
		CreateDevshardFee:         escrow.CreateDevshardFee,
		FeePerNonce:               escrow.FeePerNonce,
		InferenceSealGraceNonces:  escrow.InferenceSealGraceNonces,
		InferenceSealGraceSeconds: escrow.InferenceSealGraceSeconds,
		AutoSealEveryNNonces:      escrow.AutoSealEveryNNonces,
	})
	if m.params != nil {
		live := m.params.SessionParams()
		config = types.ApplyChainSessionBindParams(config, len(group), types.LiveSessionBindParams{
			RefusalTimeout:      live.RefusalTimeout,
			ExecutionTimeout:    live.ExecutionTimeout,
			ValidationRate:      live.ValidationRate,
			VoteThresholdFactor: live.VoteThresholdFactor,
		})
	} else {
		config = types.NormalizeSessionConfig(config, len(group))
	}

	sm, err := state.NewStateMachine(escrowID, config, group, escrow.Amount, creatorAddr, m.verifier, m.store,
		state.WithWarmKeyResolver(m.bridge.VerifyWarmKey),
		state.WithVersion(types.EffectiveStateRootAndProtocolVersion),
	)
	if err != nil {
		return nil, fmt.Errorf("create state machine: %w", err)
	}

	h, err := host.NewHost(sm, m.signer, m.engine, escrowID, group, nil,
		m.hostOptions(escrow.EpochID)...,
	)
	if err != nil {
		return nil, fmt.Errorf("create host: %w", err)
	}

	if err := m.store.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		EpochID:        escrow.EpochID,
		Version:        m.boundVersion,
		CreatorAddr:    creatorAddr,
		Config:         config,
		Group:          group,
		InitialBalance: escrow.Amount,
	}); err != nil {
		return nil, fmt.Errorf("init storage session: %w", err)
	}

	srv, err := transport.NewServer(h, m.store, m.verifier, creatorAddr,
		transport.WithBridge(m.bridge),
	)
	if err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}

	return srv, nil
}

// RecoverSessions rebuilds in-memory sessions from the shared store.
// For each active session, it replays all diffs through a fresh StateMachine,
// injecting warm key deltas from the stored DiffRecords. Call this on startup
// after constructing the HostManager.
func (m *HostManager) RecoverSessions() error {
	startedAt := time.Now()
	active, err := m.store.ListActiveSessions()
	if err != nil {
		return fmt.Errorf("list active sessions: %w", err)
	}
	if len(active) == 0 {
		logging.Info("completed devshard session recovery", inferenceTypes.System,
			"session_count", 0, "worker_count", 0, "recovered_count", 0, "failed_count", 0,
			"duration", time.Since(startedAt))
		return nil
	}

	workers := recoverSessionsConcurrency
	if len(active) < workers {
		workers = len(active)
	}
	logging.Info("starting devshard session recovery", inferenceTypes.System,
		"session_count", len(active), "worker_count", workers)

	jobs := make(chan storage.ActiveSession)
	var wg sync.WaitGroup
	var recoveredCount atomic.Int64
	var failedCount atomic.Int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sess := range jobs {
				sessionStartedAt := time.Now()
				if _, err := m.recoverAndStoreSession(sess.EscrowID); err != nil {
					failedCount.Add(1)
					logging.Error("failed to recover devshard session", inferenceTypes.System,
						"escrow_id", sess.EscrowID, "epoch_id", sess.EpochID,
						"duration", time.Since(sessionStartedAt), "error", err)
					continue
				}
				recoveredCount.Add(1)
				logging.Info("recovered devshard session", inferenceTypes.System,
					"escrow_id", sess.EscrowID, "epoch_id", sess.EpochID,
					"duration", time.Since(sessionStartedAt))
			}
		}()
	}
	for _, sess := range active {
		jobs <- sess
	}
	close(jobs)
	wg.Wait()

	logging.Info("completed devshard session recovery", inferenceTypes.System,
		"session_count", len(active), "worker_count", workers,
		"recovered_count", recoveredCount.Load(),
		"failed_count", failedCount.Load(),
		"duration", time.Since(startedAt))

	return nil
}

func (m *HostManager) recoverAndStoreSession(escrowID string) (*transport.Server, error) {
	if srv, ok := m.session(escrowID); ok {
		return srv, nil
	}
	v, err, _ := m.sf.Do(escrowID, func() (interface{}, error) {
		if srv, ok := m.session(escrowID); ok {
			return srv, nil
		}
		srv, err := m.recoverStoredSession(escrowID)
		if err != nil {
			return nil, err
		}
		return m.storeSessionIfAbsent(escrowID, srv), nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*transport.Server), nil
}

// recoverStoredSession replays a single session from storage.
func (m *HostManager) recoverStoredSession(escrowID string) (*transport.Server, error) {
	meta, err := m.store.GetSessionMeta(escrowID)
	if err != nil {
		return nil, fmt.Errorf("get session meta: %w", err)
	}
	if meta.Version != "" && meta.Version != m.boundVersion {
		return nil, fmt.Errorf("%w: stored %s, host %s", storage.ErrSessionVersionConflict, meta.Version, m.boundVersion)
	}
	if meta.Version == "" {
		meta.Version = m.boundVersion
	}
	sm, err := state.NewStateMachine(
		escrowID, meta.Config, meta.Group, meta.InitialBalance,
		meta.CreatorAddr, m.verifier, m.store,
		state.WithWarmKeyResolver(m.bridge.VerifyWarmKey),
		state.WithVersion(types.EffectiveStateRootAndProtocolVersion),
	)
	if err != nil {
		return nil, fmt.Errorf("create state machine: %w", err)
	}

	replayFrom := uint64(1)
	if meta.LatestNonce > 0 {
		snapNonce, snapData, snapErr := m.store.LoadSnapshot(escrowID)
		if snapErr == nil && snapNonce > 0 && snapNonce <= meta.LatestNonce {
			snapState, committedEntries, sealedNonces, err := host.UnmarshalStateSnapshotWithCommitted(snapData)
			if err != nil {
				logging.Error("failed to decode devshard snapshot, replaying full history", inferenceTypes.System,
					"escrow_id", escrowID, "snapshot_nonce", snapNonce, "error", err)
			} else {
				sm.RestoreState(snapState)
				sm.RestoreCommittedEntries(committedEntries)
				sm.RestoreSealedNonces(sealedNonces)
				replayFrom = snapNonce + 1
				logging.Info("restored devshard snapshot", inferenceTypes.System,
					"escrow_id", escrowID, "snapshot_nonce", snapNonce, "latest_nonce", meta.LatestNonce)
			}
		} else if snapErr != nil && !errors.Is(snapErr, storage.ErrSnapshotNotFound) {
			logging.Error("failed to load devshard snapshot, replaying full history", inferenceTypes.System,
				"escrow_id", escrowID, "error", snapErr)
		}

		records, err := m.store.GetDiffs(escrowID, replayFrom, meta.LatestNonce)
		if err != nil {
			return nil, fmt.Errorf("get diffs: %w", err)
		}

		for _, rec := range records {
			sm.InjectWarmKeys(rec.WarmKeyDelta)
			root, applyErr := sm.ApplyLocal(rec.Nonce, rec.Txs)
			if applyErr != nil {
				return nil, fmt.Errorf("replay nonce %d: %w", rec.Nonce, applyErr)
			}
			if len(rec.StateHash) > 0 && len(root) > 0 {
				if !bytes.Equal(root, rec.StateHash) {
					return nil, fmt.Errorf("state root mismatch at nonce %d", rec.Nonce)
				}
			}
		}

		if replayFrom == 1 || uint64(len(records)) >= host.SnapshotInterval {
			if err := saveHostSnapshot(m.store, sm, escrowID, meta.LatestNonce); err != nil {
				logging.Error("failed to save devshard recovery snapshot", inferenceTypes.System,
					"escrow_id", escrowID, "nonce", meta.LatestNonce, "error", err)
			}
		}
	}
	if err := sm.RebuildSealedInferenceIndex(); err != nil {
		return nil, fmt.Errorf("rebuild sealed inference index: %w", err)
	}

	h, err := host.NewHost(sm, m.signer, m.engine, escrowID, meta.Group, nil,
		m.hostOptions(meta.EpochID)...,
	)
	if err != nil {
		return nil, fmt.Errorf("create host: %w", err)
	}

	srv, err := transport.NewServer(h, m.store, m.verifier, meta.CreatorAddr,
		transport.WithBridge(m.bridge),
	)
	if err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}

	return srv, nil
}

// hostOptions returns the common HostOption set used when constructing a
// host either for a fresh session or a recovered one. Keeps the option list
// in one place so future additions (prune sink, gossip, etc.) stay symmetric.
func (m *HostManager) hostOptions(epochID uint64) []host.HostOption {
	opts := []host.HostOption{
		host.WithValidator(m.validator),
		host.WithStorage(m.store),
		host.WithEpochID(epochID),
		host.WithAvailabilityProvider(m.availability),
	}
	if m.pruneSink != nil {
		opts = append(opts, host.WithPruneSink(m.pruneSink))
	}
	if m.maxNonce != nil {
		opts = append(opts, host.WithMaxNonceProvider(m.maxNonce))
	}
	return opts
}

func saveHostSnapshot(store storage.Storage, sm *state.StateMachine, escrowID string, nonce uint64) error {
	data, err := host.MarshalStateSnapshotWithCommitted(sm.ExportState(), sm.ExportCommittedEntries(), sm.ExportSealedNonces())
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	if err := store.SaveSnapshot(escrowID, nonce, data); err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	return nil
}

// Register mounts devshard session routes on the given echo group.
func (m *HostManager) Register(g *echo.Group) {
	g.GET("/stats/shards", m.handleStatsShards)
	g.GET("/stats/shards/:escrow_id", m.handleStatsShard)
	devshardserver.RegisterLazySessionRoutes(g, m, m)
}

func (m *HostManager) handleStatsShards(c echo.Context) error {
	resp, err := m.statsShards(time.Now())
	if err != nil {
		return statsHTTPError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (m *HostManager) handleStatsShard(c echo.Context) error {
	escrowID := c.Param("escrow_id")
	if escrowID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "escrow_id required")
	}
	resp, err := m.statsShardDetail(escrowID, time.Now())
	if err != nil {
		return statsHTTPError(err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (m *HostManager) statsShards(now time.Time) (*statsShardsResponse, error) {
	if err := m.readinessError(); err != nil {
		return nil, err
	}

	m.statsMu.Lock()
	if m.statsShardsCache != nil && now.Sub(m.statsShardsCached) < statsCacheTTL {
		resp := m.statsShardsCache
		m.statsMu.Unlock()
		return resp, nil
	}
	m.statsMu.Unlock()

	currentEpochID, active, err := m.currentEpochActiveSessions()
	if err != nil {
		return nil, err
	}

	resp := &statsShardsResponse{
		CurrentEpochID:  currentEpochID,
		CachedAt:        now.Unix(),
		CacheTTLSeconds: int64(statsCacheTTL / time.Second),
		ActiveEscrows:   make([]string, 0, len(active)),
		Shards:          make([]statsShardSummary, 0, len(active)),
	}
	for _, sess := range active {
		resp.ActiveEscrows = append(resp.ActiveEscrows, sess.EscrowID)
		resp.Shards = append(resp.Shards, statsShardSummary{
			EscrowID: sess.EscrowID,
			EpochID:  sess.EpochID,
		})
	}

	m.statsMu.Lock()
	m.statsShardsCache = resp
	m.statsShardsCached = now
	m.statsMu.Unlock()
	return resp, nil
}

func (m *HostManager) statsShardDetail(escrowID string, now time.Time) (*statsShardDetailResponse, error) {
	if err := m.readinessError(); err != nil {
		return nil, err
	}

	m.statsMu.Lock()
	if cached, ok := m.statsDetailsCache[escrowID]; ok && now.Sub(cached.cached) < statsCacheTTL {
		resp := cached.response
		m.statsMu.Unlock()
		return resp, nil
	}
	m.statsMu.Unlock()

	sess, err := m.currentEpochActiveSession(escrowID)
	if err != nil {
		return nil, err
	}
	srv, err := m.SessionServer(escrowID)
	if err != nil {
		return nil, err
	}

	st := srv.Host().SnapshotState()

	resp := &statsShardDetailResponse{
		EscrowID:                    escrowID,
		EpochID:                     sess.EpochID,
		Nonce:                       st.LatestNonce,
		Version:                     m.boundVersion,
		StateRootAndProtocolVersion: st.StateRootAndProtocolVersion,
		CachedAt:                    now.Unix(),
		CacheTTLSeconds:             int64(statsCacheTTL / time.Second),
		HostStats:                   statsHostStatsFromState(st.HostStats),
		ValidationObservability:     validationObservabilityFromStore(m.store, escrowID),
		Group:                       append([]types.SlotAssignment(nil), st.Group...),
	}

	m.statsMu.Lock()
	m.statsDetailsCache[escrowID] = statsShardDetailCache{response: resp, cached: now}
	m.statsMu.Unlock()
	return resp, nil
}

func (m *HostManager) currentEpochActiveSession(escrowID string) (storage.ActiveSession, error) {
	_, active, err := m.currentEpochActiveSessions()
	if err != nil {
		return storage.ActiveSession{}, err
	}
	for _, sess := range active {
		if sess.EscrowID == escrowID {
			return sess, nil
		}
	}
	return storage.ActiveSession{}, storage.ErrSessionNotFound
}

func (m *HostManager) currentEpochActiveSessions() (uint64, []storage.ActiveSession, error) {
	active, err := m.store.ListActiveSessions()
	if err != nil {
		return 0, nil, fmt.Errorf("list active sessions: %w", err)
	}

	currentEpochID := currentEpochIDFromStore(m.store)
	if currentEpochID == 0 {
		for _, sess := range active {
			if sess.EpochID > currentEpochID {
				currentEpochID = sess.EpochID
			}
		}
	}

	filtered := make([]storage.ActiveSession, 0, len(active))
	for _, sess := range active {
		if sess.EpochID == currentEpochID {
			filtered = append(filtered, sess)
		}
	}
	slices.SortFunc(filtered, func(a, b storage.ActiveSession) int {
		return cmp.Compare(a.EpochID, b.EpochID)
	})
	return currentEpochID, filtered, nil
}

func statsHostStatsFromState(src map[uint32]*types.HostStats) map[uint32]statsHostStats {
	dst := make(map[uint32]statsHostStats, len(src))
	for slotID, stats := range src {
		if stats == nil {
			dst[slotID] = statsHostStats{}
			continue
		}
		dst[slotID] = statsHostStats{
			Missed:               stats.Missed,
			Invalid:              stats.Invalid,
			Cost:                 stats.Cost,
			RequiredValidations:  stats.RequiredValidations,
			CompletedValidations: stats.CompletedValidations,
		}
	}
	return dst
}

func validationObservabilityFromStore(store storage.Storage, escrowID string) statsValidationObservability {
	out := statsValidationObservability{
		BySlot: make(map[uint32]statsHostStats),
	}
	if store == nil {
		return out
	}
	rows, err := store.GetValidationObservability(escrowID)
	if err != nil {
		logging.Warn("validation observability read failed", inferenceTypes.System,
			"escrow_id", escrowID,
			"error", err,
		)
		return out
	}
	for _, row := range rows {
		out.BySlot[row.SlotID] = statsHostStats{
			RequiredValidations:  row.RequiredValidations,
			CompletedValidations: row.CompletedValidations,
		}
		out.Totals.RequiredValidations += uint64(row.RequiredValidations)
		out.Totals.CompletedValidations += uint64(row.CompletedValidations)
	}
	return out
}

func statsHTTPError(err error) error {
	if errors.Is(err, devshardserver.ErrInitializing) {
		return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	if errors.Is(err, storage.ErrSessionNotFound) {
		return echo.NewHTTPError(http.StatusNotFound, "shard not found")
	}
	if errors.Is(err, storage.ErrSessionVersionConflict) || errors.Is(err, storage.ErrSessionEpochConflict) {
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
	return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
}

// HandlePayloads serves payloads to validators for devshard validation.
// Authenticates that the requester is a member of the session group (or a warm key
// for a group member), then returns signed payloads.
func (m *HostManager) HandlePayloads(c echo.Context, srv *transport.Server) error {
	escrowID := srv.Host().EscrowID()
	ctx := c.Request().Context()
	inferenceID := c.QueryParam("inference_id")
	validatorAddress := c.Request().Header.Get(utils.XValidatorAddressHeader)

	emit := func(level observability.Level, msg string, status observability.MetricStatus, reason observability.Reason, err error, fields ...any) {
		base := []any{"inference_id", inferenceID, "validator_address", validatorAddress}
		observability.LogPayloadRequest(ctx, level, escrowID, status, reason, msg, err, append(base, fields...)...)
	}

	if inferenceID == "" {
		emit(observability.LevelWarn, "payload request failed", observability.MetricStatusError, observability.ReasonMissingInferenceID, nil)
		return echo.NewHTTPError(http.StatusBadRequest, "inference_id required")
	}

	epochID, authReason, authErr := m.authenticatePayloadRequest(c, srv.Host().Group())
	if authErr != nil {
		emit(observability.LevelWarn, "payload request auth failed", observability.MetricStatusError, authReason, authErr)
		return authErr
	}

	// Retrieve payloads with adjacent epoch fallback.
	promptPayload, responsePayload, servedEpoch, err := m.retrievePayloadsWithAdjacentEpochs(ctx, escrowID, inferenceID, epochID)
	if err != nil {
		if errors.Is(err, payloadstorage.ErrNotFound) {
			emit(observability.LevelWarn, "payload request failed", observability.MetricStatusError, observability.ReasonPayloadNotFound, nil, "requested_epoch", epochID)
			return echo.NewHTTPError(http.StatusNotFound, "payload not found")
		}
		emit(observability.LevelWarn, "payload request failed", observability.MetricStatusError, observability.ReasonPayloadRetrieveErr, err, "requested_epoch", epochID)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Sign response using same scheme as public endpoint
	executorSignature, err := m.signPayloadResponse(inferenceID, promptPayload, responsePayload)
	if err != nil {
		emit(observability.LevelWarn, "payload request failed", observability.MetricStatusError, observability.ReasonPayloadResponseSignErr, err,
			"requested_epoch", epochID,
			"served_epoch", servedEpoch)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to sign response")
	}

	if err := c.JSON(http.StatusOK, validation.PayloadResponse{
		InferenceId:       inferenceID,
		PromptPayload:     promptPayload,
		ResponsePayload:   responsePayload,
		ExecutorSignature: executorSignature,
	}); err != nil {
		emit(observability.LevelWarn, "payload request failed", observability.MetricStatusError, observability.ReasonPayloadWriteErr, err,
			"requested_epoch", epochID,
			"served_epoch", servedEpoch)
		return err
	}
	emit(observability.LevelInfo, "payload served", observability.MetricStatusOK, observability.ReasonOK, nil,
		"requested_epoch", epochID,
		"served_epoch", servedEpoch)
	return nil
}

// authenticatePayloadRequest validates headers, timestamp, group membership,
// and signature for a payload retrieval request. Returns the parsed epochID,
// the observability reason for the failure (or ReasonOK), and the *echo.HTTPError
// suitable to return directly to the client.
func (m *HostManager) authenticatePayloadRequest(c echo.Context, group []types.SlotAssignment) (uint64, observability.Reason, error) {
	validatorAddress := c.Request().Header.Get(utils.XValidatorAddressHeader)
	timestampStr := c.Request().Header.Get(utils.XTimestampHeader)
	epochIDStr := c.Request().Header.Get(utils.XEpochIdHeader)
	signature := c.Request().Header.Get(utils.AuthorizationHeader)
	inferenceID := c.QueryParam("inference_id")

	if validatorAddress == "" {
		return 0, observability.ReasonMissingValidatorHeader, echo.NewHTTPError(http.StatusBadRequest, "X-Validator-Address header required")
	}
	if timestampStr == "" {
		return 0, observability.ReasonMissingTimestampHeader, echo.NewHTTPError(http.StatusBadRequest, "X-Timestamp header required")
	}
	if epochIDStr == "" {
		return 0, observability.ReasonMissingEpochHeader, echo.NewHTTPError(http.StatusBadRequest, "X-Epoch-Id header required")
	}
	if signature == "" {
		return 0, observability.ReasonMissingSignatureHeader, echo.NewHTTPError(http.StatusUnauthorized, "Authorization header required")
	}

	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return 0, observability.ReasonInvalidTimestamp, echo.NewHTTPError(http.StatusBadRequest, "invalid timestamp format")
	}

	epochID, err := strconv.ParseUint(epochIDStr, 10, 64)
	if err != nil {
		return 0, observability.ReasonInvalidEpoch, echo.NewHTTPError(http.StatusBadRequest, "invalid epoch_id format")
	}

	// Validate timestamp within 60s window
	now := time.Now().UnixNano()
	maxAge := int64(60 * time.Second)
	maxFuture := int64(10 * time.Second)
	requestAge := now - timestamp
	if requestAge > maxAge {
		return 0, observability.ReasonTimestampTooOld, echo.NewHTTPError(http.StatusBadRequest, "request timestamp too old")
	}
	if requestAge < -maxFuture {
		return 0, observability.ReasonTimestampInFuture, echo.NewHTTPError(http.StatusBadRequest, "request timestamp in the future")
	}

	granterAddress, err := m.findGranterInGroup(validatorAddress, group)
	if err != nil {
		return 0, observability.ReasonNotGroupMember, echo.NewHTTPError(http.StatusUnauthorized, "not a group member")
	}

	// Collect requester's pubkeys for signature verification
	pubkeys, err := m.getValidatorPubKeys(c.Request().Context(), validatorAddress, granterAddress)
	if err != nil {
		return 0, observability.ReasonPubkeyResolutionErr, echo.NewHTTPError(http.StatusUnauthorized, "failed to resolve validator pubkeys")
	}

	// Verify signature
	components := calculations.SignatureComponents{
		Payload:         inferenceID,
		EpochId:         epochID,
		Timestamp:       timestamp,
		TransferAddress: validatorAddress,
		ExecutorAddress: "",
	}
	if err := calculations.ValidateSignatureWithGrantees(components, calculations.Developer, pubkeys, signature); err != nil {
		return 0, observability.ReasonInvalidSignature, echo.NewHTTPError(http.StatusUnauthorized, "invalid signature")
	}

	return epochID, observability.ReasonOK, nil
}

// findGranterInGroup returns the group member address that the validator
// represents. If validatorAddress is a direct group member, returns it.
// Otherwise checks if validatorAddress is a warm key for any group member.
func (m *HostManager) findGranterInGroup(validatorAddress string, group []types.SlotAssignment) (string, error) {
	// Direct membership check
	for _, slot := range group {
		if slot.ValidatorAddress == validatorAddress {
			return validatorAddress, nil
		}
	}

	// Warm key check: see if validatorAddress is authorized by any group member
	for _, slot := range group {
		isWarm, err := m.bridge.VerifyWarmKey(validatorAddress, slot.ValidatorAddress)
		if err != nil {
			continue
		}
		if isWarm {
			return slot.ValidatorAddress, nil
		}
	}

	return "", fmt.Errorf("address %s is not a group member or warm key", validatorAddress)
}

// getValidatorPubKeys collects all pubkeys (cold + warm) that can sign on
// behalf of the validator. granterAddress is the group member address that
// the validator represents (may be the same as validatorAddress for direct members).
func (m *HostManager) getValidatorPubKeys(ctx context.Context, validatorAddress, granterAddress string) ([]string, error) {
	var pubkeys []string
	queryClient := m.recorder.NewInferenceQueryClient()

	// Account pubkey (secp256k1) -- the key used for signing payload requests
	participant, err := queryClient.AccountByAddress(ctx, &inferenceTypes.QueryAccountByAddressRequest{
		Address: granterAddress,
	})
	if err == nil && participant.Pubkey != "" {
		pubkeys = append(pubkeys, participant.Pubkey)
	}

	// Warm keys via grantees query
	grantees, err := queryClient.GranteesByMessageType(ctx, &inferenceTypes.QueryGranteesByMessageTypeRequest{
		GranterAddress: granterAddress,
		MessageTypeUrl: "/inference.inference.MsgStartInference",
	})
	if err == nil {
		for _, g := range grantees.Grantees {
			pubkeys = append(pubkeys, g.PubKey)
		}
	}

	if len(pubkeys) == 0 {
		return nil, fmt.Errorf("no pubkeys found for %s (granter %s)", validatorAddress, granterAddress)
	}

	return pubkeys, nil
}

// retrievePayloadsWithAdjacentEpochs tries to retrieve payloads from storage,
// checking adjacent epochs if not found under the primary epochId.
func (m *HostManager) retrievePayloadsWithAdjacentEpochs(ctx context.Context, escrowID string, inferenceID string, epochID uint64) ([]byte, []byte, uint64, error) {
	parsedID, err := strconv.ParseUint(inferenceID, 10, 64)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("invalid inference_id %q: %w", inferenceID, err)
	}
	storageKey := devshardserver.PayloadKey(escrowID, parsedID)

	seen := map[uint64]struct{}{}
	var epochs []uint64
	addEpoch := func(epoch uint64) {
		if _, ok := seen[epoch]; ok {
			return
		}
		seen[epoch] = struct{}{}
		epochs = append(epochs, epoch)
	}
	addEpochWithAdjacent := func(epoch uint64) {
		addEpoch(epoch)
		if epoch > 0 {
			addEpoch(epoch - 1)
		}
		addEpoch(epoch + 1)
	}

	addEpochWithAdjacent(epochID)

	// TODO: remove after epoch-0 devshardd migration window closes.
	// Older devshardd versions requested payloads under epoch 0. When they
	// coexist with fixed binaries, validators can still send epoch 0 while the
	// executor stored the immutable, hash-verified payload under the escrow epoch.
	if meta, err := m.store.GetSessionMeta(escrowID); err == nil {
		addEpochWithAdjacent(meta.EpochID)
	}
	if m.bridge != nil {
		if info, err := m.bridge.GetEscrow(escrowID); err == nil && info != nil {
			addEpochWithAdjacent(info.EpochID)
		}
	}
	if epochID == 0 {
		if current := currentEpochIDFromStore(m.store); current > 0 {
			addEpoch(current)
			addEpoch(current - 1)
		}
	}
	addEpoch(0)

	for _, candidateEpoch := range epochs {
		prompt, response, err := m.payloadStore.Retrieve(ctx, storageKey, candidateEpoch)
		if err == nil {
			if candidateEpoch != epochID {
				logging.Info("served devshard payload from fallback epoch", inferenceTypes.System,
					"escrow_id", escrowID,
					"inference_id", inferenceID,
					"requested_epoch", epochID,
					"served_epoch", candidateEpoch)
			}
			return prompt, response, candidateEpoch, nil
		}
		if !errors.Is(err, payloadstorage.ErrNotFound) {
			return nil, nil, 0, err
		}
	}

	return nil, nil, 0, payloadstorage.ErrNotFound
}

func currentEpochIDFromStore(store storage.Storage) uint64 {
	type currentEpochProvider interface {
		CurrentEpochID() uint64
	}
	if p, ok := store.(currentEpochProvider); ok {
		return p.CurrentEpochID()
	}
	return 0
}

// signPayloadResponse signs the payload response using the same scheme as the public endpoint.
func (m *HostManager) signPayloadResponse(inferenceID string, promptPayload, responsePayload []byte) (string, error) {
	promptHash := utils.GenerateSHA256HashBytes(promptPayload)
	responseHash := utils.GenerateSHA256HashBytes(responsePayload)
	payload := inferenceID + promptHash + responseHash

	components := calculations.SignatureComponents{
		Payload:         payload,
		Timestamp:       0,
		TransferAddress: m.recorder.GetAccountAddress(),
		ExecutorAddress: "",
	}

	signerAddressStr := m.recorder.GetSignerAddress()
	signerAddress, err := sdk.AccAddressFromBech32(signerAddressStr)
	if err != nil {
		return "", err
	}
	accountSigner := &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: m.recorder.GetKeyring(),
	}

	return calculations.Sign(accountSigner, components, calculations.Developer)
}
