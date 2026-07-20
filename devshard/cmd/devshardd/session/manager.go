package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
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

	statsMu           sync.Mutex
	statsShardsCache  *statsShardsResponse
	statsShardsCached time.Time
	statsDetailsCache map[string]statsShardDetailCache
}

const (
	resolutionFailureTTL  = 30 * time.Second
	permanentFailureTTL   = 10 * time.Minute
	maxResolutionFailures = 1024
)

type resolutionFailure struct {
	err       error
	expiresAt time.Time
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
	return &HostManager{
		sessions:           make(map[string]*transport.Server),
		resolutionFailures: make(map[string]resolutionFailure),
		store:              store,
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
	}
}

// SetAvailabilityProvider gates completion requests on devshard_requests_enabled.
func (m *HostManager) SetAvailabilityProvider(p devshardpkg.AvailabilityProvider) {
	m.availability = p
}

// SetMaxNonceProvider enforces chain max_nonce on every host.
func (m *HostManager) SetMaxNonceProvider(p devshardpkg.MaxNonceProvider) {
	m.maxNonce = p
}

// Close stops all live session hosts and releases storage resources.
func (m *HostManager) Close() error {
	m.sessionsMutex.Lock()
	sessions := make([]*transport.Server, 0, len(m.sessions))
	for _, srv := range m.sessions {
		sessions = append(sessions, srv)
	}
	m.sessions = make(map[string]*transport.Server)
	m.sessionsMutex.Unlock()

	for _, srv := range sessions {
		srv.Host().Close()
	}
	return m.store.Close()
}

// SessionServer resolves or creates the per-escrow transport server.
func (m *HostManager) SessionServer(escrowID string) (*transport.Server, error) {
	return m.getOrCreate(escrowID)
}

// HandleSettlementFinalized marks the session inactive and drops the live
// transport server so RecoverSessions will not resurrect settled escrows.
func (m *HostManager) HandleSettlementFinalized(escrowID string) error {
	m.sessionsMutex.Lock()
	srv, hadSession := m.sessions[escrowID]
	delete(m.sessions, escrowID)
	delete(m.resolutionFailures, escrowID)
	m.sessionsMutex.Unlock()
	if hadSession {
		srv.Host().Close()
	}

	if err := m.store.MarkSettled(escrowID); err != nil {
		if errors.Is(err, storage.ErrSessionNotFound) && !hadSession {
			return nil
		}
		return err
	}
	return nil
}

func (m *HostManager) getOrCreate(escrowID string) (*transport.Server, error) {
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

		srv, err := m.create(escrowID)
		if err != nil {
			return nil, err
		}

		return m.storeSessionIfAbsent(escrowID, srv), nil
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

func isPermanentResolutionFailure(err error) bool {
	return errors.Is(err, storage.ErrSessionVersionConflict) ||
		errors.Is(err, storage.ErrSessionEpochConflict) ||
		errors.Is(err, storage.ErrEpochPruned)
}

func (m *HostManager) storeSessionIfAbsent(escrowID string, srv *transport.Server) *transport.Server {
	m.sessionsMutex.Lock()
	defer m.sessionsMutex.Unlock()
	if existing, ok := m.sessions[escrowID]; ok {
		srv.Host().Close()
		return existing
	}
	delete(m.resolutionFailures, escrowID)
	m.sessions[escrowID] = srv
	srv.Host().Start()
	return srv
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

	for _, srv := range evicted {
		srv.Host().Close()
	}
	return len(evicted)
}

func (m *HostManager) create(escrowID string) (*transport.Server, error) {
	group, err := bridge.BuildGroup(escrowID, m.bridge)
	if err != nil {
		return nil, fmt.Errorf("build group: %w", err)
	}

	escrow, err := m.bridge.GetEscrow(escrowID)
	if err != nil {
		return nil, fmt.Errorf("get escrow: %w", err)
	}

	creatorAddr := escrow.CreatorAddress

	config := bridge.SessionConfigAtBind(len(group), escrow)

	sm, err := state.NewStateMachine(escrowID, config, group, escrow.Amount, creatorAddr, m.verifier, m.store,
		state.WithWarmKeyResolver(m.bridge.VerifyWarmKey),
		state.WithVersion(m.boundVersion),
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

	srv, err := transport.NewServer(h, m.store, m.verifier, creatorAddr,
		transport.WithBridge(m.bridge),
		transport.WithRateLimit(transport.DefaultRateLimitConfig()),
	)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("create server: %w", err)
	}

	return srv, nil
}

// RecoverSessions rebuilds in-memory sessions from the shared store.
// For each active session, it replays all diffs through a fresh StateMachine,
// injecting warm key deltas from the stored DiffRecords. Call this on startup
// after constructing the HostManager.
func (m *HostManager) RecoverSessions() error {
	escrowIDs, err := m.store.ListActiveSessions()
	if err != nil {
		return fmt.Errorf("list active sessions: %w", err)
	}

	for _, active := range escrowIDs {
		if _, err := m.recoverAndStoreSession(active.EscrowID); err != nil {
			if errors.Is(err, storage.ErrSessionVersionConflict) {
				logging.Info("skipping devshard session with foreign version", inferenceTypes.System,
					"escrow_id", active.EscrowID, "error", err)
				continue
			}
			logging.Error("skipping corrupt session", inferenceTypes.System,
				"escrow_id", active.EscrowID, "error", err)
		}
	}

	return nil
}

func (m *HostManager) recoverAndStoreSession(escrowID string) (*transport.Server, error) {
	if srv, ok := m.existingServer(escrowID); ok {
		return srv, nil
	}
	v, err, _ := m.sf.Do(escrowID, func() (interface{}, error) {
		if srv, ok := m.existingServer(escrowID); ok {
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
	recoveredVersion := meta.Version
	if recoveredVersion == "" {
		recoveredVersion = m.boundVersion
	}
	sm, err := state.NewStateMachine(
		escrowID, meta.Config, meta.Group, meta.InitialBalance,
		meta.CreatorAddr, m.verifier, m.store,
		state.WithWarmKeyResolver(m.bridge.VerifyWarmKey),
		state.WithVersion(recoveredVersion),
	)
	if err != nil {
		return nil, fmt.Errorf("create state machine: %w", err)
	}

	if meta.LatestNonce > 0 {
		records, err := m.store.GetDiffs(escrowID, 1, meta.LatestNonce)
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

		if err := storage.RebuildValidationObsFromDiffs(
			m.store,
			escrowID,
			records,
			storage.SealedInferenceIDsSorted(sm.ExportSealedNonces()),
		); err != nil {
			return nil, fmt.Errorf("rebuild validation obs: %w", err)
		}
	}

	h, err := host.NewHost(sm, m.signer, m.engine, escrowID, meta.Group, nil, m.hostOpts(meta.EpochID)...)
	if err != nil {
		return nil, fmt.Errorf("create host: %w", err)
	}

	srv, err := transport.NewServer(h, m.store, m.verifier, meta.CreatorAddr,
		transport.WithBridge(m.bridge),
		transport.WithRateLimit(transport.DefaultRateLimitConfig()),
	)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("create server: %w", err)
	}

	return srv, nil
}

// Register mounts devshard session routes on the given echo group.
// Stats routes are registered before lazy session routes so they are not
// wrapped by the session EchoMiddleware applied inside RegisterLazySessionRoutes.
func (m *HostManager) Register(g *echo.Group) {
	g.GET("/stats/shards", m.handleStatsShards)
	g.GET("/stats/shards/:escrow_id", m.handleStatsShard)
	devshardserver.RegisterLazySessionRoutes(g, m, m)
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
	return opts
}
