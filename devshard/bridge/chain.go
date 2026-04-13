package bridge

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"

	"common/chain"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const warmKeyMsgTypeGRPC = "/inference.inference.MsgSettleSubnetEscrow"

// Submitter broadcasts dispute state to the chain.
// Implemented by the wiring layer (e.g. common/chain/tx.Manager).
type Submitter interface {
	SubmitDisputeState(escrowID string, stateRoot []byte, nonce uint64, sigs map[uint32][]byte) error
}

// ChainBridge implements MainnetBridge using common/chain for all queries and actions.
// Notifications are dispatched to registered callbacks.
type ChainBridge struct {
	client    *chain.Client
	submitter Submitter

	onEscrowCreated       func(EscrowInfo) error
	onSettlementProposed  func(escrowID string, stateRoot []byte, nonce uint64) error
	onSettlementFinalized func(escrowID string) error

	warmCache sync.Map // warmCacheKey -> bool
}

// NewChainBridge creates a ChainBridge. submitter may be nil if SubmitDisputeState is not needed.
func NewChainBridge(client *chain.Client, submitter Submitter) *ChainBridge {
	return &ChainBridge{client: client, submitter: submitter}
}

// OnEscrowCreatedHandler registers the callback for escrow creation events.
func (b *ChainBridge) OnEscrowCreatedHandler(fn func(EscrowInfo) error) {
	b.onEscrowCreated = fn
}

// OnSettlementProposedHandler registers the callback for settlement proposal events.
func (b *ChainBridge) OnSettlementProposedHandler(fn func(escrowID string, stateRoot []byte, nonce uint64) error) {
	b.onSettlementProposed = fn
}

// OnSettlementFinalizedHandler registers the callback for settlement finalization events.
func (b *ChainBridge) OnSettlementFinalizedHandler(fn func(escrowID string) error) {
	b.onSettlementFinalized = fn
}

// -- MainnetBridge query methods --

func (b *ChainBridge) GetEscrow(escrowID string) (*EscrowInfo, error) {
	id, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid escrow id %q: %w", escrowID, err)
	}

	resp, err := b.client.InferenceQueryClient().DevshardEscrow(context.Background(),
		&inferencetypes.QueryGetDevshardEscrowRequest{Id: id})
	if err != nil {
		return nil, fmt.Errorf("DevshardEscrow %d: %w", id, err)
	}

	e := resp.Escrow
	appHash, err := hex.DecodeString(e.AppHash)
	if err != nil {
		return nil, fmt.Errorf("decode app_hash: %w", err)
	}

	slots := make([]string, len(e.Slots))
	copy(slots, e.Slots)

	return &EscrowInfo{
		EscrowID:       escrowID,
		Amount:         e.Amount,
		CreatorAddress: e.Creator,
		AppHash:        appHash,
		Slots:          slots,
		TokenPrice:     e.TokenPrice,
	}, nil
}

func (b *ChainBridge) GetHostInfo(address string) (*HostInfo, error) {
	resp, err := b.client.InferenceQueryClient().Participant(context.Background(),
		&inferencetypes.QueryGetParticipantRequest{Index: address})
	if err != nil {
		return nil, fmt.Errorf("Participant %s: %w", address, err)
	}

	return &HostInfo{
		Address: resp.Participant.Address,
		URL:     resp.Participant.InferenceUrl,
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

func (b *ChainBridge) OnEscrowCreated(escrow EscrowInfo) error {
	if b.onEscrowCreated == nil {
		return nil
	}
	return b.onEscrowCreated(escrow)
}

func (b *ChainBridge) OnSettlementProposed(escrowID string, stateRoot []byte, nonce uint64) error {
	if b.onSettlementProposed == nil {
		return nil
	}
	return b.onSettlementProposed(escrowID, stateRoot, nonce)
}

func (b *ChainBridge) OnSettlementFinalized(escrowID string) error {
	if b.onSettlementFinalized == nil {
		return nil
	}
	return b.onSettlementFinalized(escrowID)
}

// -- MainnetBridge action methods --

func (b *ChainBridge) SubmitDisputeState(escrowID string, stateRoot []byte, nonce uint64, sigs map[uint32][]byte) error {
	if b.submitter == nil {
		return ErrNotImplemented
	}
	return b.submitter.SubmitDisputeState(escrowID, stateRoot, nonce, sigs)
}
