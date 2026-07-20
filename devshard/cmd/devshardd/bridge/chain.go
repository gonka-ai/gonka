package bridge

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"common/chain"
	"devshard/bridge"
	"devshard/cmd/devshardd/events"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const warmKeyMsgTypeGRPC = "/inference.inference.MsgStartInference"

type warmCacheKey struct {
	host string
	warm string
}

// Submitter broadcasts dispute state to the chain.
// Implemented by the wiring layer (e.g. common/chain/tx.Manager).
type Submitter interface {
	SubmitDisputeState(escrowID uint64, stateRoot []byte, nonce uint64, sigs map[uint32][]byte) error
}

// ChainBridge implements bridge.MainnetBridge using common/chain for all queries and actions.
// Handlers are registered by the session layer and dispatched on chain events.
type ChainBridge struct {
	client    *chain.Client
	submitter Submitter

	escrowCreatedHandler       func(bridge.EscrowInfo) error
	settlementProposedHandler  func(escrowID string, stateRoot []byte, nonce uint64) error
	settlementFinalizedHandler func(escrowID string) error

	warmCache sync.Map // warmCacheKey -> bool
}

var (
	_ bridge.MainnetBridge = (*ChainBridge)(nil)
)

// NewChainBridge creates a ChainBridge. submitter may be nil if SubmitDisputeState is not needed.
func NewChainBridge(client *chain.Client, submitter Submitter) *ChainBridge {
	return &ChainBridge{client: client, submitter: submitter}
}

func (b *ChainBridge) Subscribe(l *events.Listener) {
	l.OnDevshardEscrowCreated(func(_ context.Context, e events.DevshardEscrowCreatedEvent) {
		info, err := b.GetEscrow(e.EscrowID)
		if err != nil {
			slog.Warn("chain events: failed to fetch escrow", "escrow_id", e.EscrowID, "error", err)
			return
		}
		if err := b.OnEscrowCreated(*info); err != nil {
			slog.Warn("chain events: escrow created handler failed", "escrow_id", e.EscrowID, "error", err)
		}
	})
	// TODO: OnSettlementProposed?
	l.OnDevshardEscrowSettled(func(_ context.Context, e events.DevshardEscrowSettledEvent) {
		if err := b.OnSettlementFinalized(e.EscrowID); err != nil {
			slog.Warn("chain events: settlement finalized handler failed", "escrow_id", e.EscrowID, "error", err)
		}
	})
}

func (b *ChainBridge) OnEscrowCreatedHandler(fn func(bridge.EscrowInfo) error) {
	b.escrowCreatedHandler = fn
}

func (b *ChainBridge) OnSettlementProposedHandler(fn func(string, []byte, uint64) error) {
	b.settlementProposedHandler = fn
}

func (b *ChainBridge) OnSettlementFinalizedHandler(fn func(string) error) {
	b.settlementFinalizedHandler = fn
}

func parseEscrowID(escrowID string) (uint64, error) {
	id, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid escrow id %q: %w", escrowID, err)
	}
	return id, nil
}

// -- MainnetBridge query methods --

func (b *ChainBridge) GetEscrow(escrowID string) (*bridge.EscrowInfo, error) {
	id, err := parseEscrowID(escrowID)
	if err != nil {
		return nil, err
	}

	resp, err := b.client.InferenceQueryClient().DevshardEscrow(context.Background(),
		&inferencetypes.QueryGetDevshardEscrowRequest{Id: id})
	if err != nil {
		return nil, fmt.Errorf("DevshardEscrow %s: %w", escrowID, err)
	}
	if resp == nil || !resp.Found || resp.Escrow == nil {
		return nil, bridge.ErrEscrowNotFound
	}

	e := resp.Escrow
	appHash, err := hex.DecodeString(e.AppHash)
	if err != nil {
		return nil, fmt.Errorf("decode app_hash: %w", err)
	}

	slots := make([]string, len(e.Slots))
	copy(slots, e.Slots)

	return &bridge.EscrowInfo{
		EscrowID:                  escrowID,
		Amount:                    e.Amount,
		CreatorAddress:            e.Creator,
		AppHash:                   appHash,
		Slots:                     slots,
		TokenPrice:                e.TokenPrice,
		CreateDevshardFee:         e.CreateDevshardFee,
		FeePerNonce:               e.FeePerNonce,
		InferenceSealGraceNonces:  e.InferenceSealGraceNonces,
		InferenceSealGraceSeconds: e.InferenceSealGraceSeconds,
		AutoSealEveryNNonces:      e.AutoSealEveryNNonces,
		ValidationRate:            e.ValidationRate,
		VoteThresholdFactor:       e.VoteThresholdFactor,
		EpochID:                   e.EpochIndex,
	}, nil
}

func (b *ChainBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	resp, err := b.client.InferenceQueryClient().Participant(context.Background(),
		&inferencetypes.QueryGetParticipantRequest{Index: address})
	if err != nil {
		return nil, fmt.Errorf("Participant %s: %w", address, err)
	}

	return &bridge.HostInfo{
		Address: resp.Participant.Address,
		URL:     resp.Participant.InferenceUrl,
	}, nil
}

func (b *ChainBridge) GetValidationThreshold(epochID uint64, modelID string) (*bridge.Decimal, error) {
	resp, err := b.client.InferenceQueryClient().EpochGroupData(context.Background(),
		&inferencetypes.QueryGetEpochGroupDataRequest{
			EpochIndex: epochID,
			ModelId:    modelID,
		})
	if err != nil {
		return nil, fmt.Errorf("EpochGroupData epoch=%d model=%s: %w", epochID, modelID, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("validation threshold not found for epoch %d model %s", epochID, modelID)
	}
	egd := resp.EpochGroupData
	if egd.ModelSnapshot == nil || egd.ModelSnapshot.ValidationThreshold == nil {
		return nil, fmt.Errorf("validation threshold not found for epoch %d model %s", epochID, modelID)
	}
	threshold := egd.ModelSnapshot.ValidationThreshold
	return &bridge.Decimal{
		Value:    threshold.Value,
		Exponent: threshold.Exponent,
	}, nil
}

func (b *ChainBridge) VerifyWarmKey(warmAddress, validatorAddress string) (bool, error) {
	key := warmCacheKey{host: validatorAddress, warm: warmAddress}
	if cached, ok := b.warmCache.Load(key); ok {
		return cached.(bool), nil
	}

	resp, err := b.client.InferenceQueryClient().GranteesByMessageType(context.Background(),
		&inferencetypes.QueryGranteesByMessageTypeRequest{
			GranterAddress: validatorAddress,
			MessageTypeUrl: warmKeyMsgTypeGRPC,
		})
	if err != nil {
		return false, fmt.Errorf("GranteesByMessageType: %w", err)
	}

	found := false
	for _, g := range resp.Grantees {
		if g.Address == warmAddress {
			found = true
			break
		}
	}
	b.warmCache.Store(key, found)
	return found, nil
}

// -- MainnetBridge notification methods --

func (b *ChainBridge) OnEscrowCreated(escrow bridge.EscrowInfo) error {
	if b.escrowCreatedHandler != nil {
		return b.escrowCreatedHandler(escrow)
	}
	return nil
}

func (b *ChainBridge) OnSettlementProposed(escrowID string, stateRoot []byte, nonce uint64) error {
	if b.settlementProposedHandler != nil {
		return b.settlementProposedHandler(escrowID, stateRoot, nonce)
	}
	return nil
}

func (b *ChainBridge) OnSettlementFinalized(escrowID string) error {
	if b.settlementFinalizedHandler != nil {
		return b.settlementFinalizedHandler(escrowID)
	}
	return nil
}

// -- MainnetBridge action methods --

func (b *ChainBridge) SubmitDisputeState(escrowID string, stateRoot []byte, nonce uint64, sigs map[uint32][]byte) error {
	if b.submitter == nil {
		return bridge.ErrNotImplemented
	}
	id, err := parseEscrowID(escrowID)
	if err != nil {
		return err
	}
	return b.submitter.SubmitDisputeState(id, stateRoot, nonce, sigs)
}
