// Package manager provides HostManager: a multi-escrow subnet session manager
// with lazy creation and startup recovery.
//
// Ported from decentralized-api/internal/subnet.HostManager with the following changes:
//   - cosmosclient.CosmosMessageClient replaced by common/chain.InferenceClient for chain queries
//   - payloadstorage.PayloadStorage replaced by common/storage/payloads.Store (separate escrow/inference IDs)
//   - Payload response signing uses the subnet secp256k1 scheme (signing.Signer.Sign → base64)
//     instead of calculations.Sign (cosmos amino). Both sides must agree on this scheme.
//   - Payload request verification uses signing.Verifier.RecoverAddress instead of
//     calculations.ValidateSignatureWithGrantees.
package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/sync/singleflight"

	inferencetypes "github.com/productscience/inference/x/inference/types"

	"common/chain"
	"common/storage/payloads"

	"subnet"
	"subnet/bridge"
	"subnet/host"
	"subnet/signing"
	"subnet/state"
	"subnet/storage"
	"subnet/transport"
	"subnet/types"
)

// HostManager manages per-escrow subnet sessions with lazy creation.
type HostManager struct {
	mu       sync.RWMutex
	sessions map[string]*transport.Server
	sf       singleflight.Group

	store        storage.Storage
	signer       signing.Signer
	verifier     signing.Verifier
	engine       subnet.InferenceEngine
	validator    subnet.ValidationEngine
	br           bridge.MainnetBridge
	payloadStore *payloads.Store
	queryClient  chain.InferenceClient
}

// New creates a HostManager.
// signer may be any type satisfying signing.Signer (e.g. *common/signing.Secp256k1Signer).
func New(
	store storage.Storage,
	signer signing.Signer,
	engine subnet.InferenceEngine,
	validator subnet.ValidationEngine,
	br bridge.MainnetBridge,
	payloadStore *payloads.Store,
	queryClient chain.InferenceClient,
) *HostManager {
	return &HostManager{
		sessions:     make(map[string]*transport.Server),
		store:        store,
		signer:       signer,
		verifier:     signing.NewSecp256k1Verifier(),
		engine:       engine,
		validator:    validator,
		br:           br,
		payloadStore: payloadStore,
		queryClient:  queryClient,
	}
}

// RecoverSessions rebuilds in-memory sessions from the shared store on startup.
func (m *HostManager) RecoverSessions() error {
	escrowIDs, err := m.store.ListActiveSessions()
	if err != nil {
		return fmt.Errorf("list active sessions: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, escrowID := range escrowIDs {
		if err := m.recoverSession(escrowID); err != nil {
			// Log and skip corrupt sessions rather than aborting startup.
			fmt.Printf("subnetd: skipping corrupt session %s: %v\n", escrowID, err)
		}
	}
	return nil
}

func (m *HostManager) recoverSession(escrowID string) error {
	meta, err := m.store.GetSessionMeta(escrowID)
	if err != nil {
		return fmt.Errorf("get session meta: %w", err)
	}

	sm, err := state.NewStateMachine(
		escrowID, meta.Config, meta.Group, meta.InitialBalance,
		meta.CreatorAddr, m.verifier,
		state.WithWarmKeyResolver(m.br.VerifyWarmKey),
	)
	if err != nil {
		return fmt.Errorf("create state machine: %w", err)
	}

	if meta.LatestNonce > 0 {
		records, err := m.store.GetDiffs(escrowID, 1, meta.LatestNonce)
		if err != nil {
			return fmt.Errorf("get diffs: %w", err)
		}
		for _, rec := range records {
			sm.InjectWarmKeys(rec.WarmKeyDelta)
			root, applyErr := sm.ApplyLocal(rec.Nonce, rec.Txs)
			if applyErr != nil {
				return fmt.Errorf("replay nonce %d: %w", rec.Nonce, applyErr)
			}
			if len(rec.StateHash) > 0 && len(root) > 0 && !bytes.Equal(root, rec.StateHash) {
				return fmt.Errorf("state root mismatch at nonce %d", rec.Nonce)
			}
		}
	}

	h, err := host.NewHost(sm, m.signer, m.engine, escrowID, meta.Group, nil,
		host.WithValidator(m.validator),
		host.WithStorage(m.store),
	)
	if err != nil {
		return fmt.Errorf("create host: %w", err)
	}

	srv, err := transport.NewServer(h, m.store, m.verifier, meta.CreatorAddr,
		transport.WithBridge(m.br),
	)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	m.sessions[escrowID] = srv
	return nil
}

func (m *HostManager) getOrCreate(escrowID string) (*transport.Server, error) {
	m.mu.RLock()
	srv, ok := m.sessions[escrowID]
	m.mu.RUnlock()
	if ok {
		return srv, nil
	}

	v, err, _ := m.sf.Do(escrowID, func() (any, error) {
		m.mu.RLock()
		if srv, ok := m.sessions[escrowID]; ok {
			m.mu.RUnlock()
			return srv, nil
		}
		m.mu.RUnlock()

		srv, err := m.create(escrowID)
		if err != nil {
			return nil, err
		}

		m.mu.Lock()
		m.sessions[escrowID] = srv
		m.mu.Unlock()
		return srv, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*transport.Server), nil
}

func (m *HostManager) create(escrowID string) (*transport.Server, error) {
	group, err := bridge.BuildGroup(escrowID, m.br)
	if err != nil {
		return nil, fmt.Errorf("build group: %w", err)
	}

	escrow, err := m.br.GetEscrow(escrowID)
	if err != nil {
		return nil, fmt.Errorf("get escrow: %w", err)
	}

	cfg := types.SessionConfigWithPrice(len(group), escrow.TokenPrice)

	if err := m.store.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		CreatorAddr:    escrow.CreatorAddress,
		Config:         cfg,
		Group:          group,
		InitialBalance: escrow.Amount,
	}); err != nil {
		return nil, fmt.Errorf("init storage session: %w", err)
	}

	sm, err := state.NewStateMachine(escrowID, cfg, group, escrow.Amount, escrow.CreatorAddress, m.verifier,
		state.WithWarmKeyResolver(m.br.VerifyWarmKey),
	)
	if err != nil {
		return nil, fmt.Errorf("create state machine: %w", err)
	}

	h, err := host.NewHost(sm, m.signer, m.engine, escrowID, group, nil,
		host.WithValidator(m.validator),
		host.WithStorage(m.store),
	)
	if err != nil {
		return nil, fmt.Errorf("create host: %w", err)
	}

	srv, err := transport.NewServer(h, m.store, m.verifier, escrow.CreatorAddress,
		transport.WithBridge(m.br),
	)
	if err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}
	return srv, nil
}

// Register mounts subnet session routes on the given echo group.
func (m *HostManager) Register(g *echo.Group) {
	g.POST("/sessions/:id/chat/completions", m.withAuth(func(srv *transport.Server) echo.HandlerFunc { return srv.HandleInference }))
	g.POST("/sessions/:id/verify-timeout", m.withAuth(func(srv *transport.Server) echo.HandlerFunc { return srv.HandleVerifyTimeout }))
	g.POST("/sessions/:id/challenge-receipt", m.withAuth(func(srv *transport.Server) echo.HandlerFunc { return srv.HandleChallengeReceipt }))
	g.POST("/sessions/:id/gossip/nonce", m.withAuth(func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGossipNonce }))
	g.POST("/sessions/:id/gossip/txs", m.withAuth(func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGossipTxs }))
	g.GET("/sessions/:id/diffs", m.withoutAuth(func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGetDiffs }))
	g.GET("/sessions/:id/mempool", m.withoutAuth(func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGetMempool }))
	g.GET("/sessions/:id/signatures", m.withoutAuth(func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGetSignatures }))
	g.GET("/sessions/:id/payloads", m.handleGetPayloads)
}

// handleGetPayloads serves prompt/response payloads to validators for subnet validation.
// Authentication uses the subnet secp256k1 scheme; see signPayloadResponse for the
// response signing format.
func (m *HostManager) handleGetPayloads(c echo.Context) error {
	escrowID := c.Param("id")
	inferenceID := c.QueryParam("inference_id")
	if inferenceID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "inference_id required")
	}

	epochID, err := m.authenticatePayloadRequest(c, escrowID, inferenceID)
	if err != nil {
		return err
	}

	prompt, response, err := m.retrievePayloads(c.Request().Context(), escrowID, inferenceID, epochID)
	if err != nil {
		if errors.Is(err, payloads.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "payload not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	sig, err := m.signPayloadResponse(inferenceID, prompt, response)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "sign response failed")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"inference_id":       inferenceID,
		"prompt_payload":     prompt,
		"response_payload":   response,
		"executor_signature": sig,
	})
}

// authenticatePayloadRequest validates headers, timestamp window, group membership,
// and signature. Returns the parsed epochID on success.
//
// Signature scheme (subnet secp256k1):
//
//	message = inferenceID + ":" + timestamp + ":" + epochID + ":" + validatorAddress
//	sig     = signer.Sign([]byte(message))   // raw secp256k1
//	header  = base64(sig)
func (m *HostManager) authenticatePayloadRequest(c echo.Context, escrowID, inferenceID string) (uint64, error) {
	validatorAddr := c.Request().Header.Get("X-Validator-Address")
	timestampStr := c.Request().Header.Get("X-Timestamp")
	epochIDStr := c.Request().Header.Get("X-Epoch-Id")
	authHeader := c.Request().Header.Get("Authorization")

	if validatorAddr == "" {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "X-Validator-Address required")
	}
	if timestampStr == "" {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "X-Timestamp required")
	}
	if epochIDStr == "" {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "X-Epoch-Id required")
	}
	if authHeader == "" {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "Authorization required")
	}

	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "invalid timestamp")
	}
	epochID, err := strconv.ParseUint(epochIDStr, 10, 64)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "invalid epoch_id")
	}

	now := time.Now().UnixNano()
	if now-timestamp > int64(60*time.Second) {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "timestamp too old")
	}
	if timestamp-now > int64(10*time.Second) {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "timestamp in the future")
	}

	if err := m.checkGroupMembership(validatorAddr, escrowID); err != nil {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "not a group member")
	}

	sigBytes, err := base64.StdEncoding.DecodeString(authHeader)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "invalid signature encoding")
	}
	msg := inferenceID + ":" + timestampStr + ":" + epochIDStr + ":" + validatorAddr
	recovered, err := m.verifier.RecoverAddress([]byte(msg), sigBytes)
	if err != nil || recovered != validatorAddr {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "invalid signature")
	}

	return epochID, nil
}

// checkGroupMembership returns nil if validatorAddr is a direct group member or a
// warm key for a group member of the given escrow session.
func (m *HostManager) checkGroupMembership(validatorAddr, escrowID string) error {
	srv, err := m.getOrCreate(escrowID)
	if err != nil {
		return err
	}
	for _, slot := range srv.Host().Group() {
		if slot.ValidatorAddress == validatorAddr {
			return nil
		}
	}
	for _, slot := range srv.Host().Group() {
		ok, err := m.br.VerifyWarmKey(validatorAddr, slot.ValidatorAddress)
		if err == nil && ok {
			return nil
		}
	}
	return fmt.Errorf("not a member of escrow %s", escrowID)
}

// retrievePayloads fetches payloads, falling back to adjacent epochs for boundary races.
func (m *HostManager) retrievePayloads(ctx context.Context, escrowID, inferenceID string, epochID uint64) (prompt, response []byte, err error) {
	prompt, response, err = m.payloadStore.Retrieve(ctx, escrowID, inferenceID, epochID)
	if err == nil {
		return prompt, response, nil
	}
	if !errors.Is(err, payloads.ErrNotFound) {
		return nil, nil, err
	}

	candidates := []uint64{epochID + 1}
	if epochID > 0 {
		candidates = append(candidates, epochID-1)
	}
	for _, adj := range candidates {
		prompt, response, err = m.payloadStore.Retrieve(ctx, escrowID, inferenceID, adj)
		if err == nil {
			return prompt, response, nil
		}
		if !errors.Is(err, payloads.ErrNotFound) {
			return nil, nil, err
		}
	}
	return nil, nil, payloads.ErrNotFound
}

// signPayloadResponse signs the payload response for the requester to verify.
//
// Signature scheme (subnet secp256k1):
//
//	message = inferenceID + hex(sha256(prompt)) + hex(sha256(response))
//	sig     = signer.Sign([]byte(message))
//	result  = base64(sig)
//
// Note: differs from decentralized-api's calculations.Sign (cosmos amino) scheme.
func (m *HostManager) signPayloadResponse(inferenceID string, prompt, response []byte) (string, error) {
	promptHash := sha256.Sum256(prompt)
	respHash := sha256.Sum256(response)
	msg := inferenceID + hex.EncodeToString(promptHash[:]) + hex.EncodeToString(respHash[:])
	sig, err := m.signer.Sign([]byte(msg))
	if err != nil {
		return "", fmt.Errorf("sign payload response: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// GetValidatorPubKey resolves the secp256k1 public key for a validator address from chain.
func (m *HostManager) GetValidatorPubKey(ctx context.Context, address string) (string, error) {
	resp, err := m.queryClient.AccountByAddress(ctx, &inferencetypes.QueryAccountByAddressRequest{
		Address: address,
	})
	if err != nil {
		return "", fmt.Errorf("query account %s: %w", address, err)
	}
	return resp.Pubkey, nil
}

// GetWarmKeys returns all warm-key addresses granted by a validator.
func (m *HostManager) GetWarmKeys(ctx context.Context, granterAddress string) ([]string, error) {
	resp, err := m.queryClient.GranteesByMessageType(ctx, &inferencetypes.QueryGranteesByMessageTypeRequest{
		GranterAddress: granterAddress,
		MessageTypeUrl: "/inference.inference.MsgStartInference",
	})
	if err != nil {
		return nil, fmt.Errorf("query grantees for %s: %w", granterAddress, err)
	}
	addrs := make([]string, 0, len(resp.Grantees))
	for _, g := range resp.Grantees {
		addrs = append(addrs, g.Address)
	}
	return addrs, nil
}

func (m *HostManager) withAuth(pick func(*transport.Server) echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		srv, err := m.getOrCreate(c.Param("id"))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return srv.AuthMiddleware(pick(srv))(c)
	}
}

func (m *HostManager) withoutAuth(pick func(*transport.Server) echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		srv, err := m.getOrCreate(c.Param("id"))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return pick(srv)(c)
	}
}
