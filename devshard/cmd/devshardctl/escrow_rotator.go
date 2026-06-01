package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"devshard/types"
)

const (
	rotationRoleRegular = "regular"
	rotationRoleTemp    = "temp"

	defaultEscrowRotationInterval = 15 * time.Second
)

var (
	errDevshardBusy                   = errors.New("devshard has active requests")
	errEscrowRotationCreateSuppressed = errors.New("escrow rotation create already failed for this epoch")
	gatewayCreateRotationEscrow       = (*Gateway).createRotationEscrow
	gatewayCreateDepletionEscrow      func(*Gateway, context.Context, GatewaySettings, EscrowRotationModelSettings, string, uint64) (*CreateDevshardEscrowResult, error)
	gatewaySettleDevshardOnChain      = (*Gateway).settleDevshardOnChain
)

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
	g.rotateEscrowsOnce()

	ticker := time.NewTicker(defaultEscrowRotationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			g.rotateEscrowsOnce()
		case <-stopCh:
			return
		}
	}
}

func (g *Gateway) rotateEscrowsOnce() {
	if g == nil || g.phaseGate == nil || g.store == nil {
		return
	}
	g.mu.Lock()
	settings := g.settings
	g.mu.Unlock()
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
		g.prepareBridgeEscrows(snapshot, settings)
		return
	}
	if !pocActive {
		g.finishBridgeEscrows(snapshot, settings)
	}
}

func (g *Gateway) prepareBridgeEscrows(snapshot ChainPhaseSnapshot, settings GatewaySettings) {
	epoch := snapshot.EpochIndex
	for _, model := range normalizedEscrowRotationModels(settings) {
		ensure, err := g.ensureRotationEscrows(context.Background(), settings, model, rotationRoleTemp, epoch, model.TempCount)
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
			settledOnChain, err := g.retireRotatedDevshard(context.Background(), devshard.ID, "escrow rotation regular retired", settings)
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

func (g *Gateway) finishBridgeEscrows(snapshot ChainPhaseSnapshot, settings GatewaySettings) {
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
		ensure, err := g.ensureRotationEscrows(context.Background(), settings, model, rotationRoleRegular, epoch, model.TargetCount)
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
			settledOnChain, err := g.retireRotatedDevshard(context.Background(), devshard.ID, "escrow rotation temp retired", settings)
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
	if count < target && g.rotationCreateFailed(model.ModelID, role, epoch) {
		return result, errEscrowRotationCreateSuppressed
	}
	for count < target {
		if _, err := gatewayCreateRotationEscrow(g, ctx, settings, model, role, epoch); err != nil {
			g.recordRotationCreateFailure(model.ModelID, role, epoch)
			return result, err
		}
		count++
		result.CreatedCount++
	}
	return result, nil
}

func (g *Gateway) createRotationEscrow(ctx context.Context, settings GatewaySettings, model EscrowRotationModelSettings, role string, epoch uint64) (*CreateDevshardEscrowResult, error) {
	signer, _, err := signerFromRequestKey("", model.PrivateKeyEnv)
	if err != nil {
		return nil, err
	}
	txClient, err := newGatewayRESTChainTxClient(settings, "", "", 0, 0)
	if err != nil {
		return nil, err
	}
	result, err := txClient.CreateDevshardEscrow(ctx, signer, model.Amount, model.ModelID)
	if err != nil {
		return nil, err
	}
	record := GatewayDevshardState{
		RuntimeConfig: RuntimeConfig{
			ID:            strconv.FormatUint(result.EscrowID, 10),
			PrivateKeyEnv: strings.TrimSpace(model.PrivateKeyEnv),
			Model:         model.ModelID,
		},
		Active:        true,
		RotationRole:  role,
		RotationEpoch: epoch,
	}
	if _, err := g.addCreatedEscrowRuntime(record); err != nil {
		return nil, err
	}
	log.Printf("escrow_rotation_created role=%s epoch=%d model=%q escrow=%d tx_hash=%s", role, epoch, model.ModelID, result.EscrowID, result.TxHash)
	return result, nil
}

func normalizedEscrowRotationModels(settings GatewaySettings) []EscrowRotationModelSettings {
	models := make([]EscrowRotationModelSettings, 0, len(settings.EscrowRotation.Models))
	for _, model := range settings.EscrowRotation.Models {
		model.ModelID = strings.TrimSpace(model.ModelID)
		model.PrivateKeyEnv = strings.TrimSpace(model.PrivateKeyEnv)
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

func (g *Gateway) rotationFailureKey(modelID, role string, epoch uint64) string {
	return fmt.Sprintf("%s|%s|%d", strings.TrimSpace(modelID), role, epoch)
}

func (g *Gateway) recordRotationCreateFailure(modelID, role string, epoch uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.rotationFailures == nil {
		g.rotationFailures = make(map[string]struct{})
	}
	g.rotationFailures[g.rotationFailureKey(modelID, role, epoch)] = struct{}{}
}

func (g *Gateway) rotationCreateFailed(modelID, role string, epoch uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.rotationFailures[g.rotationFailureKey(modelID, role, epoch)]
	return ok
}

func (g *Gateway) settleDevshardOnChain(ctx context.Context, id string, req adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
	log.Printf("devshard_settle_start escrow=%s", id)
	g.mu.Lock()
	rt, ok := g.runtimes[id]
	if ok && rt.activeRequests.Load() > 0 {
		g.mu.Unlock()
		log.Printf("devshard_settle_blocked escrow=%s reason=active_requests count=%d", id, rt.activeRequests.Load())
		return nil, errDevshardBusy
	}
	if ok {
		rt.active.Store(false)
	}
	g.mu.Unlock()
	if !ok {
		log.Printf("devshard_settle_failed escrow=%s stage=runtime_lookup error=%q", id, "devshard is not active")
		return nil, fmt.Errorf("devshard %s is not active", id)
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
	return result, nil
}
