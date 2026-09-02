package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/labstack/echo/v4"

	"common/logging"
	"common/storage/payloads"
	"common/utils"
	validationpkg "common/validation"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	inferenceTypes "github.com/productscience/inference/x/inference/types"

	"common/chainoracle/blocks"
	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/heightsync"
	"devshard/host"
	"devshard/observability"
	"devshard/runtimeparams"
	devshardserver "devshard/server"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"
)

// HostManager manages per-escrow devshard sessions with lazy creation.
type HostManager struct {
	sessionsMutex      sync.RWMutex
	sessions           map[string]*transport.Server
	resolutionFailures map[string]resolutionFailure
	sf                 singleflight.Group

	store              storage.Storage
	signer             *signing.Secp256k1Signer
	verifier           signing.Verifier
	engine             devshardpkg.InferenceEngine
	validator          devshardpkg.ValidationEngine
	validationRecorder devshardpkg.ValidationCompletionRecorder
	boundVersion       string
	bridge             bridge.MainnetBridge
	payloadStore       PayloadStore
	recorder           PayloadAuthClient
	availability       devshardpkg.AvailabilityProvider
	maxNonce           devshardpkg.MaxNonceProvider
	params             runtimeparams.Provider
	maxBodySize        int64

	recoveryGate     recoveryGate
	recoveryComplete atomic.Bool

	// obsGate is the same value as store, kept typed so obs rebuilds can run
	// against the wrapped store while the live writes queue.
	obsGate     *storage.ObsRepairGate
	obsRepairWG sync.WaitGroup

	statsMu            sync.Mutex
	statsShardsCache   *statsShardsResponse
	statsShardsCached  time.Time
	statsDetailsCache  map[string]statsShardDetailCache
	statsNegativeCache map[string]statsNegativeCacheEntry

	binaryVersion string

	// Testenv-only payload withholding (DEVSHARD_TESTENV_PAYLOAD_*). Zero status is off.
	payloadFaultStatus int
	payloadFaultAddr   string

	// Height-sync scheduler (chain RPC or DEVSHARD_CHAINORACLE_URL). Nil when neither is available.
	chainOracle      blocks.BlockOracle
	heightSync       *heightsync.AnchorScheduler
	heightSyncCloser func()
	heightSyncTip    interface{ Observe(h *blocks.Header) }
}

const (
	recoverSessionsConcurrency = 8
	resolutionFailureTTL       = 30 * time.Second
	permanentFailureTTL        = 10 * time.Minute
	maxResolutionFailures      = 1024
	// resolutionFailureLowWater is the size the tombstone map is trimmed to
	// once it exceeds maxResolutionFailures. Trimming below the cap amortises
	// the eviction sort over many inserts; trimming exactly to the cap would
	// re-sort on every insert while the map sits at the bound, and that sort
	// runs under the sessionsMutex write lock that all lookups contend on.
	resolutionFailureLowWater = maxResolutionFailures * 3 / 4

	// recoveryEscrowCheckTimeout bounds the per-session chain query that
	// RecoverSessions makes before replaying a locally-active row.
	recoveryEscrowCheckTimeout = 5 * time.Second
	// recoveryEscrowCheckBudget bounds those queries in aggregate. Recovery is
	// synchronous in host startup and a host can hold many sessions, so a
	// per-call timeout alone would still let an unresponsive chain add
	// sessions*timeout to boot. Past the budget the remaining escrows skip the
	// check and recover, same as any other query failure.
	recoveryEscrowCheckBudget = 30 * time.Second
)

type resolutionFailure struct {
	err       error
	expiresAt time.Time
}

// recoveryGate lets sessions a live caller asked for jump ahead of the cold
// recovery backlog. Every escrow that reaches getOrCreate is remembered for the
// rest of the recovery window, which does two things: the queue hands those
// escrows out first, and a worker already recovering one of them keeps running
// instead of parking, since that work is exactly what the caller is waiting on.
// Only workers about to pick up a cold session park while a request is
// in flight, so a request waits at most for the cold sessions already running.
type recoveryGate struct {
	mu        sync.Mutex
	cond      *sync.Cond
	inFlight  int
	requested map[string]struct{}
	stopped   bool
}

// maxRequestedRecoveryEscrows bounds the demand set. Past the cap ordering
// degrades to list order rather than growing memory without limit.
const maxRequestedRecoveryEscrows = 4096

// condLocked lazily builds the cond so a zero-value HostManager still works.
func (g *recoveryGate) condLocked() *sync.Cond {
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
	}
	return g.cond
}

// begin marks an escrow as demanded and records an in-flight on-demand load.
// The broadcast lets a worker parked on this escrow notice it is now
// prioritized and resume instead of waiting for the request to finish.
func (g *recoveryGate) begin(escrowID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.requested == nil {
		g.requested = make(map[string]struct{})
	}
	if len(g.requested) < maxRequestedRecoveryEscrows {
		g.requested[escrowID] = struct{}{}
	}
	g.inFlight++
	g.condLocked().Broadcast()
}

// end releases one on-demand load. The escrow stays in the demand set so a
// later backlog pass still treats it as warm.
func (g *recoveryGate) end() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight > 0 {
		g.inFlight--
	}
	if g.inFlight == 0 {
		g.condLocked().Broadcast()
	}
}

// isRequested reports whether a live caller has asked for this escrow.
func (g *recoveryGate) isRequested(escrowID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.requested[escrowID]
	return ok
}

// waitTurn parks a background worker that is about to recover a cold session
// while on-demand loads are in flight. Workers assigned an already-demanded
// escrow are never parked: pausing them would only delay the caller.
func (g *recoveryGate) waitTurn(escrowID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for !g.stopped {
		if _, prioritized := g.requested[escrowID]; prioritized {
			return
		}
		if g.inFlight == 0 {
			return
		}
		g.condLocked().Wait()
	}
}

// stop releases parked workers so shutdown cannot block behind the gate.
func (g *recoveryGate) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopped = true
	g.condLocked().Broadcast()
}

// recoveryQueue hands backlog sessions to the recovery workers, preferring
// escrows a live caller has already asked for. Requests that arrive mid
// recovery reorder whatever is left, so demand keeps overtaking cold sessions
// instead of being fixed at startup.
type recoveryQueue struct {
	mu         sync.Mutex
	pending    []storage.ActiveSession
	prioritize func(escrowID string) bool
}

// next returns the highest-priority remaining session, or false when drained.
func (q *recoveryQueue) next() (storage.ActiveSession, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return storage.ActiveSession{}, false
	}
	idx := 0
	if q.prioritize != nil {
		for i, sess := range q.pending {
			if q.prioritize(sess.EscrowID) {
				idx = i
				break
			}
		}
	}
	sess := q.pending[idx]
	q.pending = append(q.pending[:idx], q.pending[idx+1:]...)
	return sess, true
}

func (q *recoveryQueue) remaining() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

func NewHostManager(
	store storage.Storage,
	signer *signing.Secp256k1Signer,
	engine devshardpkg.InferenceEngine,
	validator devshardpkg.ValidationEngine,
	validationRecorder devshardpkg.ValidationCompletionRecorder,
	boundVersion string,
	br bridge.MainnetBridge,
	ps PayloadStore,
	recorder PayloadAuthClient,
) *HostManager {
	faultStatus, faultAddr := payloadFaultFromEnv()
	if faultStatus > 0 {
		slog.Warn("devshardd: payload fault injection active; testenv build only",
			"http_status", faultStatus, "only_validator", faultAddr)
	}
	// Everything downstream (hosts, state machines, transport) writes through
	// the gate, which is a pass-through until a rebuild claims an escrow.
	gate := storage.NewObsRepairGate(store)
	return &HostManager{
		sessions:           make(map[string]*transport.Server),
		resolutionFailures: make(map[string]resolutionFailure),
		store:              gate,
		obsGate:            gate,
		signer:             signer,
		verifier:           signing.NewSecp256k1Verifier(),
		engine:             engine,
		validator:          validator,
		validationRecorder: validationRecorder,
		boundVersion:       boundVersion,
		bridge:             br,
		payloadStore:       ps,
		recorder:           recorder,
		statsDetailsCache:  make(map[string]statsShardDetailCache),
		statsNegativeCache: make(map[string]statsNegativeCacheEntry),
		payloadFaultStatus: faultStatus,
		payloadFaultAddr:   faultAddr,
		maxBodySize:        transport.DefaultMaxBodySize,
	}
}

// SetAvailabilityProvider gates completion requests on devshard_requests_enabled.
func (m *HostManager) SetAvailabilityProvider(p devshardpkg.AvailabilityProvider) {
	m.availability = p
}

// StorageReady reports whether the backing storage is ready to serve. When the
// store does not expose readiness (e.g. pure SQLite), it is considered ready.
func (m *HostManager) StorageReady() bool {
	if r, ok := m.store.(interface{ Ready() bool }); ok {
		return r.Ready()
	}
	return true
}

// StorageFatalErrors reports storage failures that require replacing this
// devshardd process rather than waiting for readiness recovery.
func (m *HostManager) StorageFatalErrors() <-chan error {
	if reporter, ok := m.store.(interface{ FatalErrors() <-chan error }); ok {
		return reporter.FatalErrors()
	}
	return nil
}

// StorageProof uses the same storage object as inference and session traffic.
// Deployment checks therefore cannot accidentally validate a separate pool.
func (m *HostManager) StorageProof(
	ctx context.Context,
	operation storage.ProofOperation,
	nonce string,
) (storage.StorageProof, error) {
	provider, ok := m.store.(storage.ProofProvider)
	if !ok {
		return storage.StorageProof{}, errors.New("postgres storage proof is unavailable")
	}
	return provider.StorageProof(ctx, operation, nonce)
}

// SetMaxNonceProvider enforces chain max_nonce on every host.
func (m *HostManager) SetMaxNonceProvider(p devshardpkg.MaxNonceProvider) {
	m.maxNonce = p
}

// SetParamsProvider overlays runtime height-sync scheduling knobs on new and
// recovered sessions. Evaluation knobs stay compiled inside HeartbeatConfigFromSnapshot.
func (m *HostManager) SetParamsProvider(p runtimeparams.Provider) {
	m.params = p
}

// SetBinaryVersion sets the link-time / log build id exposed on stats endpoints
// (same value as --print-binary-version / DEVSHARD_BINARY_LOG_VERSION).
func (m *HostManager) SetBinaryVersion(v string) {
	m.binaryVersion = strings.TrimSpace(v)
}

// CloseHosts cancels in-flight validation workers without closing storage.
// Shutdown must do this before closing the ML client or store so Validate
// aborts, then Release frees the Postgres row for the sibling to re-acquire.
func (m *HostManager) CloseHosts() {
	m.sessionsMutex.Lock()
	sessions := make(map[string]*transport.Server, len(m.sessions))
	for escrowID, srv := range m.sessions {
		sessions[escrowID] = srv
	}
	m.sessions = make(map[string]*transport.Server)
	m.sessionsMutex.Unlock()

	for escrowID, srv := range sessions {
		srv.Host().Close()
		observability.DeleteEscrowMetrics(escrowID)
	}
}

// Close stops all live session hosts and releases storage resources.
func (m *HostManager) Close() error {
	m.CloseHosts()
	m.CloseHeightSync()
	return m.store.Close()
}

// SessionServer resolves or creates the per-escrow transport server.
// Prefer BindOwnerChat for HTTP chat; this remains for tests and explicit bind.
func (m *HostManager) SessionServer(escrowID string) (*transport.Server, error) {
	return m.getOrCreate(escrowID, nil)
}

// SessionServerExisting returns a live or recovered session server.
// It never CreateSession / binds a protocol version. Missing sessions return
// storage.ErrSessionNotFound.
func (m *HostManager) SessionServerExisting(escrowID string) (*transport.Server, error) {
	if srv, ok := m.existingServer(escrowID); ok {
		return srv, nil
	}
	m.recoveryGate.begin(escrowID)
	defer m.recoveryGate.end()
	srv, err := m.recoverAndStoreSession(escrowID)
	if err != nil {
		return nil, err
	}
	return srv, nil
}

// BindOwnerChat verifies the request as the escrow owner, then returns an
// existing session or binds a new one with this process's boundVersion.
// Auth context (sender + body) is injected for HandleInference.
func (m *HostManager) BindOwnerChat(c echo.Context) (*transport.Server, error) {
	escrowID := c.Param("id")
	if err := devshardpkg.ValidateEscrowID(escrowID); err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	addr, body, err := transport.VerifyPOSTAuth(c, m.verifier, escrowID, m.maxBodySize)
	if err != nil {
		return nil, err
	}

	srv, err := m.SessionServerExisting(escrowID)
	if err == nil {
		if !srv.IsOwner(addr) {
			return nil, echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner")
		}
		transport.InjectAuthContext(c, addr, body)
		return srv, nil
	}
	if !errors.Is(err, storage.ErrSessionNotFound) {
		return nil, err
	}

	escrow, err := m.bridge.GetEscrow(escrowID)
	if err != nil {
		return nil, fmt.Errorf("get escrow: %w", err)
	}
	if escrow == nil || escrow.CreatorAddress == "" || addr != escrow.CreatorAddress {
		return nil, echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner")
	}
	if escrow.Settled {
		m.rememberResolutionFailure(escrowID, bridge.ErrEscrowSettled, time.Now())
		return nil, fmt.Errorf("%w: escrow %s", bridge.ErrEscrowSettled, escrowID)
	}

	// Pass the already-fetched escrow so create() does not GetEscrow again.
	srv, err = m.getOrCreate(escrowID, escrow)
	if err != nil {
		return nil, err
	}
	if !srv.IsOwner(addr) {
		return nil, echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner")
	}
	transport.InjectAuthContext(c, addr, body)
	return srv, nil
}

// HandleSettlementFinalized marks the session inactive and drops the live
// transport server so RecoverSessions will not resurrect settled escrows.
func (m *HostManager) HandleSettlementFinalized(escrowID string) error {
	m.sessionsMutex.Lock()
	srv, hadSession := m.sessions[escrowID]
	delete(m.sessions, escrowID)
	m.sessionsMutex.Unlock()
	if hadSession {
		srv.Host().Close()
		observability.DeleteEscrowMetrics(escrowID)
	}

	// Negative-cache so a concurrent/next bind does not recoverStoredSession
	// the just-settled row (getOrCreate also rejects non-active meta).
	m.rememberResolutionFailure(escrowID, bridge.ErrEscrowSettled, time.Now())

	if err := m.store.MarkSettled(escrowID); err != nil {
		if errors.Is(err, storage.ErrSessionNotFound) && !hadSession {
			return nil
		}
		return err
	}
	return nil
}

// getOrCreate returns a live session, recovering from store or creating.
// When escrow is non-nil (BindOwnerChat first-bind path), create reuses it and
// skips a second bridge.GetEscrow.
func (m *HostManager) getOrCreate(escrowID string, escrow *bridge.EscrowInfo) (*transport.Server, error) {
	if srv, ok := m.existingServer(escrowID); ok {
		return srv, nil
	}
	if err := m.cachedResolutionFailure(escrowID, time.Now()); err != nil {
		return nil, err
	}

	v, err, _ := m.sf.Do(escrowID, func() (interface{}, error) {
		if srv, ok := m.existingServer(escrowID); ok {
			return srv, nil
		}
		if err := m.cachedResolutionFailure(escrowID, time.Now()); err != nil {
			return nil, err
		}

		// Prefer recovering an already-bound session over CreateSession.
		srv, obsRepair, err := func() (*transport.Server, *obsRepairJob, error) {
			m.recoveryGate.begin(escrowID)
			defer m.recoveryGate.end()
			return m.recoverStoredSession(escrowID)
		}()
		if err == nil {
			installed, storeErr := m.storeSessionIfAbsent(escrowID, srv)
			if storeErr != nil {
				return nil, storeErr
			}
			m.startObsRepair(escrowID, obsRepair)
			return installed, nil
		}
		if !errors.Is(err, storage.ErrSessionNotFound) {
			return nil, err
		}

		srv, err = m.create(escrowID, escrow)
		if err != nil {
			return nil, err
		}

		installed, storeErr := m.storeSessionIfAbsent(escrowID, srv)
		if storeErr != nil {
			return nil, storeErr
		}
		return installed, nil
	})

	if err != nil {
		m.rememberResolutionFailure(escrowID, err, time.Now())
		return nil, err
	}
	return v.(*transport.Server), nil
}

func (m *HostManager) cachedResolutionFailure(escrowID string, now time.Time) error {
	m.sessionsMutex.Lock()
	defer m.sessionsMutex.Unlock()
	cached, ok := m.resolutionFailures[escrowID]
	if !ok {
		return nil
	}
	if !now.Before(cached.expiresAt) {
		delete(m.resolutionFailures, escrowID)
		return nil
	}
	return cached.err
}

func (m *HostManager) rememberResolutionFailure(escrowID string, err error, now time.Time) {
	if err == nil {
		return
	}
	ttl := resolutionFailureTTL
	if isPermanentResolutionFailure(err) {
		ttl = permanentFailureTTL
	}
	m.sessionsMutex.Lock()
	m.resolutionFailures[escrowID] = resolutionFailure{err: err, expiresAt: now.Add(ttl)}
	if len(m.resolutionFailures) > maxResolutionFailures {
		m.sweepExpiredResolutionFailuresLocked(now)
		// Sweeping only drops expired entries, so it cannot hold the bound on
		// its own: settlement events arrive for every escrow this host holds a
		// slot in, each parking a live 10-minute tombstone. Evict the entries
		// closest to expiry so a burst cannot grow the map without limit.
		m.evictOldestResolutionFailuresLocked(resolutionFailureLowWater)
	}
	m.sessionsMutex.Unlock()
}

func (m *HostManager) sweepExpiredResolutionFailuresLocked(now time.Time) {
	for escrowID, cached := range m.resolutionFailures {
		if !now.Before(cached.expiresAt) {
			delete(m.resolutionFailures, escrowID)
		}
	}
}

// evictOldestResolutionFailuresLocked trims the map down to limit, dropping the
// entries nearest expiry first. Dropping a live tombstone only costs a repeated
// resolution attempt, which then re-caches the failure.
func (m *HostManager) evictOldestResolutionFailuresLocked(limit int) {
	if limit <= 0 || len(m.resolutionFailures) <= limit {
		return
	}
	type entry struct {
		escrowID  string
		expiresAt time.Time
	}
	entries := make([]entry, 0, len(m.resolutionFailures))
	for escrowID, cached := range m.resolutionFailures {
		entries = append(entries, entry{escrowID: escrowID, expiresAt: cached.expiresAt})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].expiresAt.Before(entries[j].expiresAt)
	})
	for _, e := range entries[:len(entries)-limit] {
		delete(m.resolutionFailures, e.escrowID)
	}
}

func isPermanentResolutionFailure(err error) bool {
	return errors.Is(err, storage.ErrSessionVersionConflict) ||
		errors.Is(err, storage.ErrSessionEpochConflict) ||
		errors.Is(err, storage.ErrEpochPruned) ||
		errors.Is(err, storage.ErrSessionNotActive) ||
		errors.Is(err, bridge.ErrEscrowSettled)
}

// storeSessionIfAbsent installs srv unless the escrow was settled while the
// caller was building it. The settled tombstone is re-checked under
// sessionsMutex so a settlement racing create/recover cannot be erased by the
// unconditional resolutionFailures delete below.
func (m *HostManager) storeSessionIfAbsent(escrowID string, srv *transport.Server) (*transport.Server, error) {
	installed, settled := m.installSession(escrowID, srv, time.Now())
	if settled {
		srv.Host().Close()
		if err := m.store.MarkSettled(escrowID); err != nil && !errors.Is(err, storage.ErrSessionNotFound) {
			logging.Error("failed to mark racing session settled", inferenceTypes.System,
				"escrow_id", escrowID, "error", err)
		}
		return nil, fmt.Errorf("%w: escrow %s", bridge.ErrEscrowSettled, escrowID)
	}
	if installed != srv {
		srv.Host().Close()
	}
	return installed, nil
}

func (m *HostManager) installSession(escrowID string, srv *transport.Server, now time.Time) (*transport.Server, bool) {
	m.sessionsMutex.Lock()
	defer m.sessionsMutex.Unlock()
	if existing, ok := m.sessions[escrowID]; ok {
		return existing, false
	}
	if cached, ok := m.resolutionFailures[escrowID]; ok &&
		now.Before(cached.expiresAt) && errors.Is(cached.err, bridge.ErrEscrowSettled) {
		return nil, true
	}
	delete(m.resolutionFailures, escrowID)
	m.sessions[escrowID] = srv
	srv.Host().Start()
	return srv, false
}

// EvictBefore drops in-memory sessions whose epoch is below cutoffEpoch and
// closes their hosts. Returns the number of evicted sessions.
func (m *HostManager) EvictBefore(cutoffEpoch uint64) int {
	if cutoffEpoch == 0 {
		return 0
	}
	m.sessionsMutex.Lock()
	evicted := make(map[string]*transport.Server)
	for escrowID, srv := range m.sessions {
		if srv.Host().EpochID() >= cutoffEpoch {
			continue
		}
		evicted[escrowID] = srv
		delete(m.sessions, escrowID)
		delete(m.resolutionFailures, escrowID)
	}
	m.sessionsMutex.Unlock()

	for escrowID, srv := range evicted {
		srv.Host().Close()
		observability.DeleteEscrowMetrics(escrowID)
	}
	return len(evicted)
}

func (m *HostManager) create(escrowID string, escrow *bridge.EscrowInfo) (*transport.Server, error) {
	if err := devshardpkg.ValidateEscrowID(escrowID); err != nil {
		return nil, err
	}
	if escrow == nil {
		var err error
		escrow, err = m.bridge.GetEscrow(escrowID)
		if err != nil {
			return nil, fmt.Errorf("get escrow: %w", err)
		}
	}
	if escrow.Settled {
		return nil, fmt.Errorf("%w: escrow %s", bridge.ErrEscrowSettled, escrowID)
	}

	group, err := bridge.BuildGroupFromEscrow(escrow)
	if err != nil {
		return nil, fmt.Errorf("build group: %w", err)
	}

	creatorAddr := escrow.CreatorAddress

	config := bridge.SessionConfigAtBind(len(group), escrow)

	sm, err := state.NewStateMachine(escrowID, config, group, escrow.Amount, creatorAddr, m.verifier, m.store,
		m.sessionSMOpts(state.WithWarmKeyResolver(m.bridge.VerifyWarmKey), state.WithVersion(m.boundVersion))...,
	)
	if err != nil {
		return nil, fmt.Errorf("create state machine: %w", err)
	}

	hostOpts := m.hostOpts(escrow.EpochID)

	h, err := host.NewHost(sm, m.signer, m.engine, escrowID, group, nil, hostOpts...)
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
		h.Close()
		return nil, fmt.Errorf("init storage session: %w", err)
	}

	srv, err := transport.NewServer(h, m.store, m.verifier, creatorAddr, m.transportServerOpts()...)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("create server: %w", err)
	}

	return srv, nil
}

// RecoverSessions rebuilds in-memory sessions from the shared store.
// For each locally-active session it asks the chain whether the escrow is
// already settled and, if so, finalizes instead of replaying. Transient
// GetEscrow failures fail-open so a chain blip at boot does not drop work
// this host already bound. Remaining sessions restore a host snapshot when
// one exists, then replay only the post-snapshot diffs through a fresh
// StateMachine. Recovery runs up to recoverSessionsConcurrency workers in
// parallel. Call this on startup after constructing the HostManager.
func (m *HostManager) RecoverSessions() error {
	return m.RecoverSessionsContext(context.Background())
}

// StartRecovery runs recovery in the background so the HTTP listener can bind
// immediately instead of waiting out the backlog; sessions that are requested
// before their turn are recovered on demand by getOrCreate. RecoveryComplete
// gates /ready. The returned func waits for the backlog to drain and is safe to
// call after ctx is cancelled.
func (m *HostManager) StartRecovery(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer m.recoveryComplete.Store(true)
		if err := m.RecoverSessionsContext(ctx); err != nil {
			logging.Error("devshard session recovery failed", inferenceTypes.System, "error", err)
		}
	}()
	return func() {
		m.recoveryGate.stop()
		<-done
	}
}

// RecoveryComplete reports whether background recovery has finished. It is true
// once the backlog drains, including when recovery was cancelled or failed.
func (m *HostManager) RecoveryComplete() bool {
	return m.recoveryComplete.Load()
}

// RecoverSessionsContext is RecoverSessions with cancellation: a cancelled ctx
// stops the workers between sessions and leaves the rest to lazy recovery.
func (m *HostManager) RecoverSessionsContext(ctx context.Context) error {
	startedAt := time.Now()
	active, err := m.store.ListActiveSessions()
	if err != nil {
		return fmt.Errorf("list active sessions: %w", err)
	}

	checkDeadline := time.Now().Add(recoveryEscrowCheckBudget)
	filtered := active[:0]
	for _, sess := range active {
		if err := devshardpkg.ValidateEscrowID(sess.EscrowID); err != nil {
			logging.Error("retiring devshard session with non-canonical escrow id", inferenceTypes.System,
				"escrow_id", sess.EscrowID, "error", err)
			if markErr := m.store.MarkSettled(sess.EscrowID); markErr != nil {
				logging.Error("failed to retire non-canonical devshard session", inferenceTypes.System,
					"escrow_id", sess.EscrowID, "error", markErr)
			}
			continue
		}
		// Local status is "active" only. A missed ESCROW_SETTLED (or a
		// settlement that aged out of the dapi ring) leaves that row in place,
		// and recoverStoredSession never asks the chain. One GetEscrow here
		// prevents serving work that VerifyDevshardSettlement will refuse to
		// pay for. Transient query failures fail-open: recover the row.
		//
		// HandleSettlementFinalized persists MarkSettled, so a chain node that
		// wrongly reports Settled retires the session for good. That is
		// deliberate: its in-memory tombstone expires after permanentFailureTTL,
		// after which the still-active row would be recovered by the next bind
		// and serve unpayable work until the following restart. Durable state is
		// the only way to make the decision stick, and a node that lies about
		// Settled is already outside the trust model create() relies on.
		if m.chainReportsSettled(sess.EscrowID, checkDeadline) {
			if err := m.HandleSettlementFinalized(sess.EscrowID); err != nil {
				logging.Error("failed to finalize chain-settled escrow during recovery", inferenceTypes.System,
					"escrow_id", sess.EscrowID, "error", err)
			} else {
				logging.Info("skipping recovery of chain-settled escrow", inferenceTypes.System,
					"escrow_id", sess.EscrowID)
			}
			continue
		}
		filtered = append(filtered, sess)
	}
	active = filtered

	if len(active) == 0 {
		logging.Info("completed devshard session recovery", inferenceTypes.System,
			"session_count", 0, "worker_count", 0, "recovered_count", 0, "failed_count", 0,
			"version_skipped_count", 0, "cancelled_count", 0, "duration", time.Since(startedAt))
		return nil
	}

	workers := recoverSessionsConcurrency
	if len(active) < workers {
		workers = len(active)
	}
	logging.Info("starting devshard session recovery", inferenceTypes.System,
		"session_count", len(active), "worker_count", workers)

	queue := &recoveryQueue{
		pending:    append([]storage.ActiveSession(nil), active...),
		prioritize: m.recoveryGate.isRequested,
	}
	var wg sync.WaitGroup
	var recoveredCount atomic.Int64
	var failedCount atomic.Int64
	var versionSkippedCount atomic.Int64
	var cancelledCount atomic.Int64
	var prioritizedCount atomic.Int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				sess, ok := queue.next()
				if !ok {
					return
				}
				if ctx.Err() != nil {
					cancelledCount.Add(1)
					continue
				}
				// A session a caller already asked for runs immediately; a
				// cold one yields until no request is in flight.
				if m.recoveryGate.isRequested(sess.EscrowID) {
					prioritizedCount.Add(1)
				} else {
					m.recoveryGate.waitTurn(sess.EscrowID)
				}
				sessionStartedAt := time.Now()
				if _, err := m.recoverAndStoreSession(sess.EscrowID); err != nil {
					if errors.Is(err, storage.ErrSessionVersionConflict) {
						versionSkippedCount.Add(1)
						logging.Info("skipping devshard session with foreign version", inferenceTypes.System,
							"escrow_id", sess.EscrowID, "epoch_id", sess.EpochID,
							"host_version", m.boundVersion,
							"duration", time.Since(sessionStartedAt), "error", err)
						continue
					}
					failedCount.Add(1)
					logging.Error("skipping corrupt session", inferenceTypes.System,
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
	wg.Wait()

	logging.Info("completed devshard session recovery", inferenceTypes.System,
		"session_count", len(active), "worker_count", workers,
		"recovered_count", recoveredCount.Load(),
		"failed_count", failedCount.Load(),
		"version_skipped_count", versionSkippedCount.Load(),
		"cancelled_count", cancelledCount.Load(),
		"prioritized_count", prioritizedCount.Load(),
		"duration", time.Since(startedAt))
	return nil
}

// chainReportsSettled is true only when the live query says the escrow is
// settled. A missing bridge, a nil info, or any query error (including chain
// unavailable) returns false so RecoverSessions still brings the local row up.
//
// Each query is bounded by recoveryEscrowCheckTimeout and all of them together
// by deadline, because recovery is synchronous in host startup.
func (m *HostManager) chainReportsSettled(escrowID string, deadline time.Time) bool {
	budget := time.Until(deadline)
	if budget > recoveryEscrowCheckTimeout {
		budget = recoveryEscrowCheckTimeout
	}
	settled, err := bridge.SettledWithin(m.bridge, escrowID, budget)
	if err != nil {
		logging.Warn("chain settled-check failed during recovery; recovering local row",
			inferenceTypes.System, "escrow_id", escrowID, "error", err)
		return false
	}
	return settled
}

func (m *HostManager) recoverAndStoreSession(escrowID string) (*transport.Server, error) {
	if srv, ok := m.existingServer(escrowID); ok {
		return srv, nil
	}
	v, err, _ := m.sf.Do(escrowID, func() (interface{}, error) {
		if srv, ok := m.existingServer(escrowID); ok {
			return srv, nil
		}
		srv, obsRepair, err := m.recoverStoredSession(escrowID)
		if err != nil {
			return nil, err
		}
		installed, storeErr := m.storeSessionIfAbsent(escrowID, srv)
		if storeErr != nil {
			return nil, storeErr
		}
		m.startObsRepair(escrowID, obsRepair)
		return installed, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*transport.Server), nil
}

// recoverStoredSession rebuilds a single session from storage. A host snapshot
// at nonce N skips replaying diffs 1..N; only N+1..latest are applied. Decode
// or load failures fall back to a full journal replay (v3 HostManager behavior).
// A non-nil obsRepairJob means the caller should hand it to startObsRepair once
// the session is published.
func (m *HostManager) recoverStoredSession(escrowID string) (_ *transport.Server, obsRepair *obsRepairJob, err error) {
	if err := devshardpkg.ValidateEscrowID(escrowID); err != nil {
		return nil, nil, err
	}
	meta, err := m.store.GetSessionMeta(escrowID)
	if err != nil {
		return nil, nil, fmt.Errorf("get session meta: %w", err)
	}
	if meta.Status != "active" {
		return nil, nil, fmt.Errorf("%w: escrow %s status %q", storage.ErrSessionNotActive, escrowID, meta.Status)
	}
	if meta.Version != "" && meta.Version != m.boundVersion {
		return nil, nil, fmt.Errorf("%w: stored %s, host %s", storage.ErrSessionVersionConflict, meta.Version, m.boundVersion)
	}
	recoveredVersion := meta.Version
	if recoveredVersion == "" {
		recoveredVersion = m.boundVersion
	}
	newStateMachine := func() (*state.StateMachine, error) {
		return state.NewStateMachine(
			escrowID, meta.Config, meta.Group, meta.InitialBalance,
			meta.CreatorAddr, m.verifier, m.store,
			m.sessionSMOpts(state.WithWarmKeyResolver(m.bridge.VerifyWarmKey), state.WithVersion(recoveredVersion))...,
		)
	}
	sm, err := newStateMachine()
	if err != nil {
		return nil, nil, fmt.Errorf("create state machine: %w", err)
	}

	replayFrom := uint64(1)
	if meta.LatestNonce > 0 {
		snapNonce, snapData, snapErr := m.store.LoadSnapshot(escrowID)
		if snapErr == nil && snapNonce > 0 && snapNonce <= meta.LatestNonce {
			snapState, committedEntries, sealedNonces, decodeErr := host.UnmarshalStateSnapshotWithCommitted(snapData)
			if decodeErr != nil {
				logging.Error("failed to decode devshard snapshot, replaying full history", inferenceTypes.System,
					"escrow_id", escrowID, "snapshot_nonce", snapNonce, "error", decodeErr)
			} else {
				sm.RestoreState(snapState)
				sm.RestoreCommittedEntries(committedEntries)
				sm.RestoreSealedNonces(sealedNonces)
				if verifyErr := verifySnapshotRoot(m.store, sm, escrowID, snapNonce); verifyErr != nil {
					// Restore already mutated sm, so the rejected state has to
					// be thrown away rather than replayed on top of.
					logging.Error("devshard snapshot failed root check, replaying full history", inferenceTypes.System,
						"escrow_id", escrowID, "snapshot_nonce", snapNonce, "error", verifyErr)
					if sm, err = newStateMachine(); err != nil {
						return nil, nil, fmt.Errorf("recreate state machine after snapshot reject: %w", err)
					}
				} else {
					replayFrom = snapNonce + 1
					logging.Info("restored devshard snapshot", inferenceTypes.System,
						"escrow_id", escrowID, "snapshot_nonce", snapNonce, "latest_nonce", meta.LatestNonce)
				}
			}
		} else if snapErr != nil && !errors.Is(snapErr, storage.ErrSnapshotNotFound) {
			logging.Error("failed to load devshard snapshot, replaying full history", inferenceTypes.System,
				"escrow_id", escrowID, "error", snapErr)
		}

		var records []types.DiffRecord
		if replayFrom <= meta.LatestNonce {
			records, err = m.store.GetDiffs(escrowID, replayFrom, meta.LatestNonce)
			if err != nil {
				return nil, nil, fmt.Errorf("get diffs: %w", err)
			}
			for _, rec := range records {
				sm.InjectWarmKeys(rec.WarmKeyDelta)
				root, applyErr := sm.ApplyLocalPersisted(rec.Nonce, rec.Txs)
				if applyErr != nil {
					return nil, nil, fmt.Errorf("replay nonce %d: %w", rec.Nonce, applyErr)
				}
				if len(rec.StateHash) > 0 && len(root) > 0 {
					if !bytes.Equal(root, rec.StateHash) {
						return nil, nil, fmt.Errorf("state root mismatch at nonce %d", rec.Nonce)
					}
				}
			}
		}

		// Validation obs is written by the live apply path and is durable, so a
		// restart already has the rows for every nonce a snapshot covers.
		// Only a full journal replay needs a rebuild, because ApplyLocal
		// records no obs and the clear-then-replay is the self-heal for
		// batches the live path dropped under backpressure. Re-recording a
		// partial range is not an option: the drain removes the live row that
		// RecordValidationsAppliedOnce dedups against, so a tail replayed
		// twice would double count.
		//
		// Hand it to the gate instead of running it here: it is the expensive
		// half of recovery (a write transaction per historical seal) and it
		// would otherwise keep the caller of a cold bind waiting. Reuse the
		// journal already in hand, whose last nonce the seal set matches;
		// seals landing later reach the rebuild through the gate queue.
		if replayFrom == 1 {
			obsRepair = &obsRepairJob{
				records: records,
				sealed:  storage.SealedInferenceIDsSorted(sm.ExportSealedNonces()),
			}
		}

		if replayFrom == 1 || uint64(len(records)) >= host.SnapshotInterval {
			if saveErr := saveHostSnapshot(m.store, sm, escrowID, meta.LatestNonce); saveErr != nil {
				logging.Error("failed to save devshard recovery snapshot", inferenceTypes.System,
					"escrow_id", escrowID, "nonce", meta.LatestNonce, "error", saveErr)
			}
		}
	}

	if err := sm.RebuildSealedInferenceIndex(); err != nil {
		return nil, nil, fmt.Errorf("rebuild sealed inference index: %w", err)
	}

	h, err := host.NewHost(sm, m.signer, m.engine, escrowID, meta.Group, nil, m.hostOpts(meta.EpochID)...)
	if err != nil {
		return nil, nil, fmt.Errorf("create host: %w", err)
	}

	srv, err := transport.NewServer(h, m.store, m.verifier, meta.CreatorAddr, m.transportServerOpts()...)
	if err != nil {
		h.Close()
		return nil, nil, fmt.Errorf("create server: %w", err)
	}

	return srv, obsRepair, nil
}

// Register mounts devshard session routes on the given echo group.
// Stats routes are registered before lazy session routes so they are not
// wrapped by the session EchoMiddleware applied inside RegisterLazySessionRoutes.
func (m *HostManager) Register(g *echo.Group) {
	g.GET("/stats/shards", m.handleStatsShards)
	g.GET("/stats/shards/:escrow_id", m.handleStatsShard)
	devshardserver.RegisterLazySessionRoutes(g, m, m, m)
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

	if payloadFaultMatches(m.payloadFaultStatus, m.payloadFaultAddr, validatorAddress) {
		emit(observability.LevelWarn, "testenv payload fault", observability.MetricStatusError, observability.ReasonPayloadRetrieveErr, nil,
			"http_status", m.payloadFaultStatus)
		return echo.NewHTTPError(m.payloadFaultStatus, "testenv payload fault")
	}

	epochID, authReason, authErr := m.authenticatePayloadRequest(c, srv.Host().Group())
	if authErr != nil {
		emit(observability.LevelWarn, "payload request auth failed", observability.MetricStatusError, authReason, authErr)
		return authErr
	}

	// Retrieve payloads with adjacent epoch fallback
	promptPayload, responsePayload, servedEpoch, err := m.retrievePayloadsWithAdjacentEpochs(ctx, escrowID, inferenceID, epochID)
	if err != nil {
		if errors.Is(err, payloads.ErrNotFound) {
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

	if err := c.JSON(http.StatusOK, validationpkg.PayloadResponse{
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
	prompt, response, err := m.payloadStore.Retrieve(ctx, escrowID, parsedID, epochID)
	if err == nil {
		return prompt, response, epochID, nil
	}
	if !errors.Is(err, payloads.ErrNotFound) {
		return nil, nil, 0, err
	}

	// Try adjacent epochs (epoch boundary race condition)
	adjacentEpochs := []uint64{}
	if epochID > 0 {
		adjacentEpochs = append(adjacentEpochs, epochID-1)
	}
	adjacentEpochs = append(adjacentEpochs, epochID+1)

	for _, adjEpoch := range adjacentEpochs {
		prompt, response, err := m.payloadStore.Retrieve(ctx, escrowID, parsedID, adjEpoch)
		if err == nil {
			return prompt, response, adjEpoch, nil
		}
		if !errors.Is(err, payloads.ErrNotFound) {
			return nil, nil, 0, err
		}
	}

	return nil, nil, 0, payloads.ErrNotFound
}

// signPayloadResponse signs the payload response using the same scheme as the public endpoint.
func (m *HostManager) signPayloadResponse(inferenceID string, promptPayload, responsePayload []byte) (string, error) {
	promptHash := utils.GenerateSHA256HashBytes(promptPayload)
	responseHash := utils.GenerateSHA256HashBytes(responsePayload)
	p := inferenceID + promptHash + responseHash

	components := calculations.SignatureComponents{
		Payload:         p,
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

// ActiveEscrowIDs returns the escrow IDs of all currently loaded sessions.
// The returned slice is a snapshot; the set may change after this call.
func (m *HostManager) ActiveEscrowIDs() []string {
	m.sessionsMutex.RLock()
	defer m.sessionsMutex.RUnlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

// TryLoadFromStorage recovers a session from the local SQLite store if it
// exists and is not already in memory. Returns nil if the session is not in
// this instance's store (i.e. it belongs to another instance).
func (m *HostManager) TryLoadFromStorage(escrowID string) error {
	if _, loaded := m.existingServer(escrowID); loaded {
		return nil
	}
	_, err := m.recoverAndStoreSession(escrowID)
	if err != nil {
		if errors.Is(err, storage.ErrSessionNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// existingServer returns the transport server for an already-loaded session.
// Returns (nil, false) if the session is not currently in memory.
func (m *HostManager) existingServer(escrowID string) (*transport.Server, bool) {
	m.sessionsMutex.RLock()
	defer m.sessionsMutex.RUnlock()
	srv, ok := m.sessions[escrowID]
	return srv, ok
}

func (m *HostManager) hostSnapshot(escrowID string) (hostSnap, bool) {
	srv, ok := m.existingServer(escrowID)
	if !ok {
		return nil, false
	}
	return srv.Host(), true
}

// verifySnapshotRoot checks a restored snapshot against the state root the
// journal recorded at the snapshot nonce. Diff replay verifies every nonce this
// way; without it the snapshot path would trust the blob outright, which
// matters because the store can be shared between hosts and a snapshot is not
// self-authenticating. Journals pruned past the snapshot, and records written
// before state_hash existed, carry no root and cannot be checked.
func verifySnapshotRoot(store storage.Storage, sm *state.StateMachine, escrowID string, snapNonce uint64) error {
	records, err := store.GetDiffs(escrowID, snapNonce, snapNonce)
	if err != nil {
		return fmt.Errorf("load diff at snapshot nonce: %w", err)
	}
	if len(records) == 0 || len(records[0].StateHash) == 0 {
		return nil
	}
	want := records[0].StateHash
	got, err := sm.ComputeStateRoot()
	if err != nil {
		return fmt.Errorf("compute restored state root: %w", err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("restored root %x does not match journal root %x at nonce %d", got, want, snapNonce)
	}
	return nil
}

// obsRepairJob carries the inputs for a deferred validation-obs rebuild: the
// journal the recovery already read, and the seal set as of that journal's last
// nonce. Anything the live path writes while the rebuild runs is queued by the
// gate and applied after it, so the two never overlap.
type obsRepairJob struct {
	records []types.DiffRecord
	sealed  []uint64
}

// startObsRepair rebuilds validation obs for a freshly published session in the
// background. The gate queues the session's obs writes for the duration, so the
// rebuild gets the exclusive access it needs without holding up the apply path.
// Failures leave the counters stale, which is why nothing outside the stats API
// may depend on them.
func (m *HostManager) startObsRepair(escrowID string, job *obsRepairJob) {
	if job == nil || m.obsGate == nil {
		return
	}
	m.obsRepairWG.Add(1)
	go func() {
		defer m.obsRepairWG.Done()
		startedAt := time.Now()
		err := m.obsGate.RepairValidationObs(escrowID, func(inner storage.Storage) error {
			return storage.RebuildValidationObsFromDiffs(inner, escrowID, job.records, job.sealed)
		})
		if err != nil {
			logging.Warn("background validation obs rebuild failed", inferenceTypes.System,
				"escrow_id", escrowID, "duration", time.Since(startedAt), "error", err)
			return
		}
		logging.Info("rebuilt validation obs", inferenceTypes.System,
			"escrow_id", escrowID, "diffs", len(job.records),
			"sealed_inferences", len(job.sealed), "duration", time.Since(startedAt))
	}()
}

// WaitObsRepairs blocks until background obs rebuilds finish. Shutdown must
// call it: a rebuild interrupted after its clear leaves the counters empty, and
// recovery will not retry once a snapshot exists.
func (m *HostManager) WaitObsRepairs() {
	m.obsRepairWG.Wait()
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

func (m *HostManager) hostOpts(epochID uint64) []host.HostOption {
	opts := []host.HostOption{
		host.WithValidator(m.validator),
		host.WithValidationCompletionRecorder(m.validationRecorder),
		host.WithStorage(m.store),
		host.WithEpochID(epochID),
		host.WithAvailabilityProvider(m.availability),
	}
	if m.maxNonce != nil {
		opts = append(opts, host.WithMaxNonceProvider(m.maxNonce))
	}
	if m.params != nil {
		sp := m.params.SessionParams()
		opts = append(opts, host.WithHeartbeatConfig(sp.Heartbeat), host.WithRepairConfig(sp.Repair))
	}
	return m.appendChainOracleOpt(opts)
}

func (m *HostManager) sessionSMOpts(extra ...state.SMOption) []state.SMOption {
	if m.params != nil {
		extra = append(extra, state.WithHeartbeatConfig(m.params.SessionParams().Heartbeat))
	}
	return extra
}
