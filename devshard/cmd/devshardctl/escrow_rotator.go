package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"devshard/types"
)

const (
	rotationRoleRegular = "regular"
	rotationRoleTemp    = "temp"

	defaultEscrowRotationInterval = 15 * time.Second

	rotationBreakerMaxCooldownTicks = 4

	escrowWriteRetries      = 10
	escrowWriteRetryBackoff = 200 * time.Millisecond
)

var (
	errDevshardBusy                   = errors.New("devshard has active requests")
	errDevshardAlreadyExists          = errors.New("devshard already exists")
	errEscrowRotationCreateSuppressed = errors.New("escrow rotation create suppressed (backoff)")
	gatewayCreateRotationEscrow       = (*Gateway).createRotationEscrow
	gatewayCreateEscrowOnChain        = (*Gateway).createEscrowOnChain
	gatewayCreateDepletionEscrow      func(*Gateway, context.Context, GatewaySettings, EscrowRotationModelSettings, string, uint64) (*CreateDevshardEscrowResult, error)
	gatewaySettleDevshardOnChain      = (*Gateway).settleDevshardOnChain
	gatewayQueryTxEscrowID            = defaultQueryTxEscrowID
)

type rotationBreaker struct {
	consecutiveFailures int
	cooldownTicks       int
}

func (g *Gateway) startEscrowRotatorIfEnabled() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.settings.EscrowRotation.Enabled {
		g.startEscrowRotatorLocked()
	}
}

func (g *Gateway) startEscrowRotatorLocked() {
	if g == nil || g.rotatorStop != nil {
		return
	}
	g.rotatorStop = make(chan struct{})
	g.rotatorDone = make(chan struct{})
	go g.runEscrowRotator(g.rotatorStop, g.rotatorDone)
}

func (g *Gateway) stopEscrowRotator() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopEscrowRotatorLocked()
}

func (g *Gateway) stopEscrowRotatorLocked() {
	if g == nil || g.rotatorStop == nil {
		return
	}
	stopCh := g.rotatorStop
	doneCh := g.rotatorDone
	g.rotatorStop = nil
	g.rotatorDone = nil
	close(stopCh)
	g.mu.Unlock()
	<-doneCh
	g.mu.Lock()
}

func (g *Gateway) runEscrowRotator(stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	g.rotateEscrowsOnce(ctx)

	ticker := time.NewTicker(defaultEscrowRotationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			g.rotateEscrowsOnce(ctx)
		case <-stopCh:
			cancel()
			return
		}
	}
}

func (g *Gateway) rotateEscrowsOnce(ctx context.Context) {
	if g == nil || g.phaseGate == nil || g.store == nil {
		return
	}
	g.mu.Lock()
	settings := g.settings
	g.mu.Unlock()
	g.reconcileCommitments(ctx, settings)
	rotation := settings.EscrowRotation
	if !rotation.Enabled {
		return
	}
	if err := validateGatewaySettings(settings); err != nil {
		log.Printf("escrow_rotation_disabled_invalid_settings error=%v", err)
		return
	}

	snapshot := g.phaseGate.Snapshot()
	if snapshot.EpochIndex == 0 || snapshot.BlockHeight == 0 {
		return
	}
	pocActive, _ := rawPoCBlockingState(snapshot.EpochPhase, snapshot.ConfirmationPoCPhase)
	blocksToEpochSwitch := snapshot.epochSwitchBlockHeight - snapshot.BlockHeight

	if blocksToEpochSwitch >= 0 && blocksToEpochSwitch <= rotation.PrePoCBlocks {
		g.prepareBridgeEscrows(ctx, snapshot, settings)
		return
	}
	if !pocActive {
		g.finishBridgeEscrows(ctx, snapshot, settings)
	}
}

func (g *Gateway) prepareBridgeEscrows(ctx context.Context, snapshot ChainPhaseSnapshot, settings GatewaySettings) {
	epoch := snapshot.EpochIndex
	for _, model := range normalizedEscrowRotationModels(settings) {
		ensure, err := g.ensureRotationEscrows(ctx, settings, model, rotationRoleTemp, epoch, model.TempCount)
		if err != nil {
			log.Printf("escrow_rotation_temp_create_failed epoch=%d model=%q error=%v", epoch, model.ModelID, err)
			promoted, promoteErr := g.promoteActiveRegularEscrowsToTemp(model.ModelID, epoch)
			if promoteErr != nil {
				log.Printf("escrow_rotation_temp_promote_failed epoch=%d model=%q error=%v", epoch, model.ModelID, promoteErr)
			}
			g.saveRotationStatus(GatewayRotationStatus{
				ModelID:       model.ModelID,
				Stage:         "prepare_temp",
				Epoch:         epoch,
				Role:          rotationRoleTemp,
				TargetCount:   model.TempCount,
				ExistingCount: ensure.ExistingCount,
				CreatedCount:  ensure.CreatedCount,
				PromotedCount: promoted,
				CreateError:   err.Error(),
				Completed:     false,
			})
			continue
		}
		state, ok, err := g.store.LoadState()
		if err != nil || !ok {
			log.Printf("escrow_rotation_load_state_failed epoch=%d model=%q error=%v", epoch, model.ModelID, err)
			continue
		}
		settled := 0
		settleFailed := 0
		for _, devshard := range state.Devshards {
			if devshard.RotationRole == rotationRoleTemp || !devshard.Active || strings.TrimSpace(devshard.Model) != model.ModelID {
				continue
			}
			settledOnChain, err := g.retireRotatedDevshard(ctx, devshard.ID, "escrow rotation regular retired", settings)
			if err != nil {
				log.Printf("escrow_rotation_regular_retire_failed epoch=%d model=%q escrow=%s error=%v", epoch, model.ModelID, devshard.ID, err)
				settleFailed++
			} else if settledOnChain {
				settled++
			}
		}
		g.saveRotationStatus(GatewayRotationStatus{
			ModelID:           model.ModelID,
			Stage:             "prepare_temp",
			Epoch:             epoch,
			Role:              rotationRoleTemp,
			TargetCount:       model.TempCount,
			ExistingCount:     ensure.ExistingCount,
			CreatedCount:      ensure.CreatedCount,
			SettledCount:      settled,
			SettleFailedCount: settleFailed,
			Completed:         settleFailed == 0,
		})
	}
}

func (g *Gateway) finishBridgeEscrows(ctx context.Context, snapshot ChainPhaseSnapshot, settings GatewaySettings) {
	epoch := snapshot.EpochIndex
	for _, model := range normalizedEscrowRotationModels(settings) {
		state, ok, err := g.store.LoadState()
		if err != nil || !ok {
			log.Printf("escrow_rotation_load_state_failed epoch=%d model=%q error=%v", epoch, model.ModelID, err)
			continue
		}
		hasBridgeEscrows := false
		for _, devshard := range state.Devshards {
			if devshard.RotationRole == rotationRoleTemp && devshard.RotationEpoch <= epoch && devshard.Active && strings.TrimSpace(devshard.Model) == model.ModelID {
				hasBridgeEscrows = true
				break
			}
		}
		if !hasBridgeEscrows {
			continue
		}
		ensure, err := g.ensureRotationEscrows(ctx, settings, model, rotationRoleRegular, epoch, model.TargetCount)
		if err != nil {
			log.Printf("escrow_rotation_regular_create_failed epoch=%d model=%q error=%v", epoch, model.ModelID, err)
			g.saveRotationStatus(GatewayRotationStatus{
				ModelID:       model.ModelID,
				Stage:         "finish_regular",
				Epoch:         epoch,
				Role:          rotationRoleRegular,
				TargetCount:   model.TargetCount,
				ExistingCount: ensure.ExistingCount,
				CreatedCount:  ensure.CreatedCount,
				CreateError:   err.Error(),
				Completed:     false,
			})
			continue
		}
		state, ok, err = g.store.LoadState()
		if err != nil || !ok {
			log.Printf("escrow_rotation_reload_state_failed epoch=%d model=%q error=%v", epoch, model.ModelID, err)
			continue
		}
		settled := 0
		settleFailed := 0
		for _, devshard := range state.Devshards {
			if devshard.RotationRole != rotationRoleTemp || devshard.RotationEpoch > epoch || !devshard.Active || strings.TrimSpace(devshard.Model) != model.ModelID {
				continue
			}
			settledOnChain, err := g.retireRotatedDevshard(ctx, devshard.ID, "escrow rotation temp retired", settings)
			if err != nil {
				log.Printf("escrow_rotation_temp_retire_failed epoch=%d model=%q escrow=%s error=%v", epoch, model.ModelID, devshard.ID, err)
				settleFailed++
			} else if settledOnChain {
				settled++
			}
		}
		g.saveRotationStatus(GatewayRotationStatus{
			ModelID:           model.ModelID,
			Stage:             "finish_regular",
			Epoch:             epoch,
			Role:              rotationRoleRegular,
			TargetCount:       model.TargetCount,
			ExistingCount:     ensure.ExistingCount,
			CreatedCount:      ensure.CreatedCount,
			SettledCount:      settled,
			SettleFailedCount: settleFailed,
			Completed:         settleFailed == 0,
		})
	}
}

type rotationEnsureResult struct {
	TargetCount   int `json:"target_count"`
	ExistingCount int `json:"existing_count"`
	CreatedCount  int `json:"created_count"`
}

func (g *Gateway) ensureRotationEscrows(ctx context.Context, settings GatewaySettings, model EscrowRotationModelSettings, role string, epoch uint64, target int) (rotationEnsureResult, error) {
	result := rotationEnsureResult{TargetCount: target}
	if target <= 0 {
		return result, nil
	}
	state, ok, err := g.store.LoadState()
	if err != nil {
		return result, err
	}
	if !ok {
		return result, fmt.Errorf("gateway state is not initialized")
	}
	count := 0
	for _, devshard := range state.Devshards {
		if devshard.RotationRole == role && devshard.RotationEpoch == epoch && devshard.Active && strings.TrimSpace(devshard.Model) == model.ModelID {
			count++
		}
	}
	result.ExistingCount = count
	if count < target {
		if served, known := g.rotationModelServedByNetwork(model.ModelID); known && !served {
			log.Printf("escrow_rotation_skip_model_absent role=%s epoch=%d model=%q reason=model_not_in_network", role, epoch, model.ModelID)
			return result, nil
		}
	}
	if count < target && g.rotationCreateGated(model.ModelID, role) {
		return result, errEscrowRotationCreateSuppressed
	}
	for count < target {
		if _, err := gatewayCreateRotationEscrow(g, ctx, settings, model, role, epoch); err != nil {
			g.recordRotationCreateFailure(model.ModelID, role)
			return result, err
		}
		count++
		result.CreatedCount++
	}
	g.resetRotationBreaker(model.ModelID, role)
	return result, nil
}

// rotationModelServedByNetwork reports whether the model is served; known is false on cold start (empty model set) so callers don't skip a genuinely-served model.
func (g *Gateway) rotationModelServedByNetwork(modelID string) (served bool, known bool) {
	if g == nil || g.capacity == nil {
		return false, false
	}
	networkModels := g.capacity.Models()
	if len(networkModels) == 0 {
		return false, false
	}
	modelID = strings.TrimSpace(modelID)
	if slices.Contains(networkModels, modelID) {
		return true, true
	}
	return false, true
}

func (g *Gateway) createEscrowOnChain(ctx context.Context, settings GatewaySettings, model EscrowRotationModelSettings, onPrepared func(txHash string) error) (*CreateDevshardEscrowResult, error) {
	signer, _, err := signerFromRequestKey("", model.PrivateKeyEnv)
	if err != nil {
		return nil, err
	}
	txClient, err := newGatewayRESTChainTxClient(settings, "", "", 0, 0)
	if err != nil {
		return nil, err
	}
	return txClient.CreateDevshardEscrow(ctx, signer, model.Amount, model.ModelID, onPrepared)
}

func (g *Gateway) createRotationEscrow(ctx context.Context, settings GatewaySettings, model EscrowRotationModelSettings, role string, epoch uint64) (*CreateDevshardEscrowResult, error) {
	commitment := GatewayEscrowCommitment{
		Model:         model.ModelID,
		Role:          role,
		Epoch:         epoch,
		PrivateKeyEnv: model.PrivateKeyEnv,
		BlockHeight:   g.currentBlockHeight(),
	}
	// Intent-first: persist the commitment (with the precomputed tx hash) before broadcast.
	onPrepared := func(txHash string) error {
		c := commitment
		c.TxHash = txHash
		return withDBRetry(ctx, func() error { return g.store.SaveCommitment(c) })
	}
	result, err := gatewayCreateEscrowOnChain(g, ctx, settings, model, onPrepared)
	if err != nil {
		return nil, err
	}
	if err := g.persistRotationEscrow(ctx, result.EscrowID, model.ModelID, role, epoch, model.PrivateKeyEnv); err != nil {
		// Escrow is on chain; commitment survives -> reconcile recovers it by tx hash.
		log.Printf("escrow_rotation_persist_failed escrow=%d tx=%s model=%q error=%v recover_via_commitment=true", result.EscrowID, result.TxHash, model.ModelID, err)
		return nil, err
	}
	g.clearCommitment(ctx, result.TxHash)
	log.Printf("escrow_rotation_created role=%s epoch=%d model=%q escrow=%d tx_hash=%s", role, epoch, model.ModelID, result.EscrowID, result.TxHash)
	return result, nil
}

func newRotationDevshardState(result *CreateDevshardEscrowResult, model EscrowRotationModelSettings, role string, epoch uint64) GatewayDevshardState {
	return GatewayDevshardState{
		RuntimeConfig: RuntimeConfig{
			ID:            strconv.FormatUint(result.EscrowID, 10),
			PrivateKeyEnv: strings.TrimSpace(model.PrivateKeyEnv),
			Model:         strings.TrimSpace(model.ModelID),
		},
		Active:        true,
		RotationRole:  role,
		RotationEpoch: epoch,
	}
}

func normalizedEscrowRotationModels(settings GatewaySettings) []EscrowRotationModelSettings {
	models := make([]EscrowRotationModelSettings, 0, len(settings.EscrowRotation.Models))
	for _, model := range settings.EscrowRotation.Models {
		model.ModelID = strings.TrimSpace(model.ModelID)
		models = append(models, model)
	}
	return models
}

func (g *Gateway) promoteActiveRegularEscrowsToTemp(modelID string, epoch uint64) (int, error) {
	state, ok, err := g.store.LoadState()
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("gateway state is not initialized")
	}
	promoted := 0
	for _, devshard := range state.Devshards {
		if !devshard.Active || devshard.RotationRole == rotationRoleTemp || strings.TrimSpace(devshard.Model) != modelID {
			continue
		}
		devshard.RotationRole = rotationRoleTemp
		devshard.RotationEpoch = epoch
		if err := g.store.UpsertDevshard(devshard); err != nil {
			return promoted, err
		}
		promoted++
		log.Printf("escrow_rotation_promoted_regular_to_temp epoch=%d model=%q escrow=%s", epoch, modelID, devshard.ID)
	}
	return promoted, nil
}

func (g *Gateway) saveRotationStatus(status GatewayRotationStatus) {
	if g == nil || g.store == nil {
		return
	}
	if status.CreatedCount == 0 && status.PromotedCount == 0 && status.SettledCount == 0 && status.SettleFailedCount == 0 && strings.TrimSpace(status.CreateError) == "" {
		return
	}
	if err := g.store.SaveRotationStatus(status); err != nil {
		log.Printf("escrow_rotation_status_save_failed model=%q stage=%q epoch=%d error=%v", status.ModelID, status.Stage, status.Epoch, err)
	}
}

func rotationBreakerKey(modelID, role string) string {
	return strings.TrimSpace(modelID) + "|" + role
}

// rotationCreateGated reports whether create is in backoff; decrements one tick.
func (g *Gateway) rotationCreateGated(modelID, role string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	breaker := g.rotationBreakers[rotationBreakerKey(modelID, role)]
	if breaker == nil || breaker.cooldownTicks <= 0 {
		return false
	}
	breaker.cooldownTicks--
	return true
}

// recordRotationCreateFailure grows the per-(model,role) backoff after a failure.
func (g *Gateway) recordRotationCreateFailure(modelID, role string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.rotationBreakers == nil {
		g.rotationBreakers = make(map[string]*rotationBreaker)
	}
	key := rotationBreakerKey(modelID, role)
	breaker := g.rotationBreakers[key]
	if breaker == nil {
		breaker = &rotationBreaker{}
		g.rotationBreakers[key] = breaker
	}
	breaker.consecutiveFailures++
	cooldown := 1 << (breaker.consecutiveFailures - 1)
	if cooldown > rotationBreakerMaxCooldownTicks {
		cooldown = rotationBreakerMaxCooldownTicks
	}
	breaker.cooldownTicks = cooldown
}

// resetRotationBreaker clears the backoff for a (model, role) after success.
func (g *Gateway) resetRotationBreaker(modelID, role string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.rotationBreakers, rotationBreakerKey(modelID, role))
}

func (g *Gateway) currentBlockHeight() uint64 {
	if g == nil || g.phaseGate == nil {
		return 0
	}
	height := g.phaseGate.Snapshot().BlockHeight
	if height < 0 {
		return 0
	}
	return uint64(height)
}

// withDBRetry retries a DB write with backoff to ride out a transient lock.
func withDBRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < escrowWriteRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err = fn(); err == nil {
			return nil
		}
		if attempt < escrowWriteRetries-1 {
			timer := time.NewTimer(escrowWriteRetryBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return err
}

// persistRotationEscrow persists + registers a created escrow ("already exists" = ok).
func (g *Gateway) persistRotationEscrow(ctx context.Context, escrowID uint64, modelID, role string, epoch uint64, keyEnv string) error {
	record := GatewayDevshardState{
		RuntimeConfig: RuntimeConfig{
			ID:            strconv.FormatUint(escrowID, 10),
			PrivateKeyEnv: keyEnv,
			Model:         modelID,
		},
		Active:        true,
		RotationRole:  role,
		RotationEpoch: epoch,
	}
	return withDBRetry(ctx, func() error {
		if _, err := g.addCreatedEscrowRuntime(record); err != nil {
			if errors.Is(err, errDevshardAlreadyExists) {
				return nil
			}
			return err
		}
		return nil
	})
}

func (g *Gateway) clearCommitment(ctx context.Context, txHash string) {
	if g == nil || g.store == nil || strings.TrimSpace(txHash) == "" {
		return
	}
	if err := withDBRetry(ctx, func() error { return g.store.DeleteCommitment(txHash) }); err != nil {
		log.Printf("escrow_rotation_commitment_clear_failed tx=%s error=%v", txHash, err)
	}
}

// commitmentReconcileGrace is how long a not-found tx is given before the
// commitment is cleared: the unordered-tx TTL plus margin for index lag. Until
// it elapses, a 404 may be a pending/unindexed tx that still creates an escrow.
const commitmentReconcileGrace = defaultUnorderedTxTTL + 2*time.Minute

// reconcileCommitments recovers escrows from pending commitments via tx hash:
// found → persist + clear; committed-failed → clear; not-found → clear only once
// the tx can no longer land; chain error → keep for next pass.
func (g *Gateway) reconcileCommitments(ctx context.Context, settings GatewaySettings) {
	if g == nil || g.store == nil {
		return
	}
	commitments, err := g.store.LoadCommitments()
	if err != nil {
		log.Printf("escrow_commitments_load_failed error=%v", err)
		return
	}
	for _, c := range commitments {
		escrowID, found, err := gatewayQueryTxEscrowID(ctx, settings, c.TxHash)
		if errors.Is(err, errTxNotFound) {
			// Tx not on chain. An unordered tx can still land until its TTL
			// elapses, so a fresh 404 is likely mempool/index lag — keep it.
			if commitmentTxMayStillLand(c) {
				log.Printf("escrow_commitment_tx_pending tx=%s model=%q", c.TxHash, c.Model)
				continue
			}
			log.Printf("escrow_commitment_no_escrow tx=%s model=%q clearing=true", c.TxHash, c.Model)
			g.clearCommitment(ctx, c.TxHash)
			continue
		}
		if err != nil {
			log.Printf("escrow_commitment_tx_query_failed tx=%s model=%q error=%v", c.TxHash, c.Model, err)
			continue // chain unreachable — retry next pass
		}
		if !found {
			log.Printf("escrow_commitment_tx_failed tx=%s model=%q clearing=true", c.TxHash, c.Model)
			g.clearCommitment(ctx, c.TxHash)
			continue
		}
		if err := g.persistRotationEscrow(ctx, escrowID, c.Model, c.Role, c.Epoch, c.PrivateKeyEnv); err != nil {
			log.Printf("escrow_commitment_persist_failed tx=%s escrow=%d model=%q error=%v", c.TxHash, escrowID, c.Model, err)
			continue // keep commitment — retry next pass
		}
		g.clearCommitment(ctx, c.TxHash)
		g.resetRotationBreaker(c.Model, c.Role)
		log.Printf("escrow_commitment_recovered tx=%s escrow=%d model=%q role=%s", c.TxHash, escrowID, c.Model, c.Role)
	}
}

// commitmentTxMayStillLand reports whether a not-found tx could yet be committed
// (within the unordered-tx window) — if so, the commitment must be kept. A zero
// timestamp is treated as still-pending so we never clear too early.
func commitmentTxMayStillLand(c GatewayEscrowCommitment) bool {
	if c.CreatedAt.IsZero() {
		return true
	}
	return time.Since(c.CreatedAt) <= commitmentReconcileGrace
}

func defaultQueryTxEscrowID(ctx context.Context, settings GatewaySettings, txHash string) (uint64, bool, error) {
	txClient, err := newGatewayRESTChainTxClient(settings, "", "", 0, 0)
	if err != nil {
		return 0, false, err
	}
	return txClient.GetTxEscrowID(ctx, txHash)
}

func (g *Gateway) settleDevshardOnChain(ctx context.Context, id string, req adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
	log.Printf("devshard_settle_start escrow=%s", id)
	g.mu.Lock()
	rt, ok := g.runtimes[id]
	if ok && rt.escrowHasBackgroundWork() {
		g.mu.Unlock()
		log.Printf("devshard_settle_blocked escrow=%s reason=background_work active_requests=%d pending_race_cleanup=%d", id, rt.activeUserRequests.Load(), rt.pendingRaceCleanup.Load())
		return nil, errDevshardBusy
	}
	if ok {
		rt.active.Store(false)
	}
	g.mu.Unlock()
	wasResident := ok
	if !ok {
		// Non-resident devshard (inactive/settled): rehydrate a full runtime
		// (with chain access) solely to build and broadcast this settlement,
		// then release it. Concurrent settlement of the same id is guarded by
		// the caller: auto/reconcile paths hold settlementInFlight[id] via
		// scheduleAutoSettlement, and the chain rejects any duplicate settle
		// broadcast, so no additional in-flight guard is taken here (taking
		// one would deadlock the auto path, which already holds it).
		cfg, known, cfgErr := g.lazyRuntimeConfig(id)
		if cfgErr != nil {
			log.Printf("devshard_settle_failed escrow=%s stage=lazy_config error=%q", id, cfgErr.Error())
			return nil, cfgErr
		}
		if !known {
			log.Printf("devshard_settle_failed escrow=%s stage=runtime_lookup error=%q", id, "devshard is not active")
			return nil, fmt.Errorf("devshard %s is not active", id)
		}
		g.mu.Lock()
		settings := g.settings
		g.mu.Unlock()
		built, buildErr := gatewayRuntimeBuilder(cfg, settings.ChainREST, settings.DefaultModel, g.perf)
		if buildErr != nil {
			log.Printf("devshard_settle_failed escrow=%s stage=rehydrate error=%q", id, buildErr.Error())
			return nil, fmt.Errorf("rehydrate devshard %s for settlement: %w", id, buildErr)
		}
		built.active.Store(false)
		rt = built
		log.Printf("devshard_settle_rehydrated escrow=%s (transient, non-resident)", id)
		defer func() {
			// Flush a final snapshot: Finalize advances the nonce, so a later
			// read-only rebuild of this settled escrow would otherwise replay
			// the diff tail. retireClose captures the finalized state once.
			if closeErr := rt.retireClose("settled-transient"); closeErr != nil {
				log.Printf("devshard_settle_transient_close_error escrow=%s error=%v", id, closeErr)
			}
		}()
	}
	if err := g.store.SetDevshardActive(id, false); err != nil {
		log.Printf("devshard_settle_failed escrow=%s stage=persist_deactivate error=%q", id, err.Error())
		return nil, err
	}

	privateKey, privateKeyEnv, err := g.resolveDevshardSettlementKey(id, req)
	if err != nil {
		log.Printf("devshard_settle_failed escrow=%s stage=resolve_key error=%q", id, err.Error())
		return nil, err
	}
	signer, _, err := signerFromRequestKey(privateKey, privateKeyEnv)
	if err != nil {
		log.Printf("devshard_settle_failed escrow=%s stage=load_key key_env=%q error=%q", id, privateKeyEnv, err.Error())
		return nil, err
	}
	log.Printf("devshard_settle_key_loaded escrow=%s settler=%s key_env=%q", id, signer.Address(), privateKeyEnv)
	if rt.proxy.sm.Phase() != types.PhaseSettlement {
		g.finalizeMu.Lock()
		log.Printf("gateway_finalize_lock_acquired escrow=%s path=rotation_settle", id)
		if err := rt.session.Finalize(ctx); err != nil {
			g.finalizeMu.Unlock()
			log.Printf("devshard_settle_failed escrow=%s stage=finalize error=%q", id, err.Error())
			return nil, err
		}
		g.finalizeMu.Unlock()
		log.Printf("devshard_settle_finalize_completed escrow=%s phase=%s", id, sessionPhaseLabel(rt.proxy.sm.Phase()))
	} else {
		log.Printf("devshard_settle_finalize_skipped escrow=%s phase=%s", id, sessionPhaseLabel(rt.proxy.sm.Phase()))
	}
	settlement, err := rt.proxy.settlementJSON()
	if err != nil {
		log.Printf("devshard_settle_failed escrow=%s stage=settlement_json error=%q", id, err.Error())
		return nil, err
	}
	g.mu.Lock()
	settings := g.settings
	g.mu.Unlock()
	txClient, err := newGatewayRESTChainTxClient(settings, req.ChainID, req.FeeDenom, req.FeeAmount, req.GasLimit)
	if err != nil {
		log.Printf("devshard_settle_failed escrow=%s stage=tx_client chain_rest=%q error=%q", id, settings.ChainREST, err.Error())
		return nil, err
	}
	log.Printf("devshard_settle_broadcast_start escrow=%s chain_rest=%q chain_id_override=%q gas_limit=%d fee_denom=%q fee_amount=%d",
		id, settings.ChainREST, req.ChainID, req.GasLimit, req.FeeDenom, req.FeeAmount)
	result, err := txClient.SettleDevshardEscrow(ctx, signer, settlement)
	if err != nil {
		log.Printf("devshard_settle_failed escrow=%s stage=broadcast chain_rest=%q error=%q", id, settings.ChainREST, err.Error())
		return nil, err
	}
	log.Printf("devshard_settle_submitted escrow=%s tx_hash=%s settler=%s", id, result.TxHash, result.Settler)
	// A settled escrow is terminal: drop the resident runtime so its memory
	// (state machine, inference map, SQLite handles) is released now rather
	// than lingering until the next restart. Transient runtimes are closed by
	// their own deferred cleanup above.
	if wasResident {
		g.retireRuntime(id, "settled")
	}
	return result, nil
}
