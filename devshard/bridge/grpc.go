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

const warmKeyMsgTypeGRPC = "/inference.inference.MsgStartInference"

type warmCacheKey struct {
	host string
	warm string
}

// GRPCBridge implements MainnetBridge query methods via common/chain gRPC.
// Notification and action methods return ErrNotImplemented.
type GRPCBridge struct {
	client    *chain.Client
	warmCache sync.Map // warmCacheKey -> bool
}

// NewGRPCBridge creates a bridge backed by an existing chain gRPC client.
func NewGRPCBridge(client *chain.Client) *GRPCBridge {
	return &GRPCBridge{client: client}
}

// NewGRPCBridgeFromURL dials grpcURL and returns a bridge. Caller must not close
// the underlying connection while the bridge is in use.
func NewGRPCBridgeFromURL(grpcURL string) (*GRPCBridge, error) {
	client, err := chain.New(grpcURL)
	if err != nil {
		return nil, err
	}
	return NewGRPCBridge(client), nil
}

func parseEscrowID(escrowID string) (uint64, error) {
	id, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid escrow id %q: %w", escrowID, err)
	}
	return id, nil
}

func (b *GRPCBridge) GetEscrow(escrowID string) (*EscrowInfo, error) {
	if b == nil || b.client == nil {
		return nil, fmt.Errorf("grpc bridge: chain client is nil")
	}
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
		return nil, ErrEscrowNotFound
	}

	e := resp.Escrow
	appHash, err := hex.DecodeString(e.AppHash)
	if err != nil {
		return nil, fmt.Errorf("decode app_hash: %w", err)
	}

	slots := make([]string, len(e.Slots))
	copy(slots, e.Slots)

	return &EscrowInfo{
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

func (b *GRPCBridge) GetHostInfo(address string) (*HostInfo, error) {
	if b == nil || b.client == nil {
		return nil, fmt.Errorf("grpc bridge: chain client is nil")
	}
	resp, err := b.client.InferenceQueryClient().Participant(context.Background(),
		&inferencetypes.QueryGetParticipantRequest{Index: address})
	if err != nil {
		return nil, fmt.Errorf("Participant %s: %w", address, err)
	}
	if resp == nil || resp.Participant.Address == "" {
		return nil, ErrParticipantNotFound
	}

	return &HostInfo{
		Address: resp.Participant.Address,
		URL:     resp.Participant.InferenceUrl,
	}, nil
}

func (b *GRPCBridge) GetValidationThreshold(epochID uint64, modelID string) (*Decimal, error) {
	if b == nil || b.client == nil {
		return nil, fmt.Errorf("grpc bridge: chain client is nil")
	}
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
	return &Decimal{
		Value:    threshold.Value,
		Exponent: threshold.Exponent,
	}, nil
}

func (b *GRPCBridge) VerifyWarmKey(warmAddress, validatorAddress string) (bool, error) {
	if b == nil || b.client == nil {
		return false, fmt.Errorf("grpc bridge: chain client is nil")
	}
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
	for _, g := range resp.GetGrantees() {
		if g.GetAddress() == warmAddress {
			found = true
			break
		}
	}
	b.warmCache.Store(key, found)
	return found, nil
}

func (b *GRPCBridge) OnEscrowCreated(_ EscrowInfo) error {
	return ErrNotImplemented
}

func (b *GRPCBridge) OnSettlementProposed(_ string, _ []byte, _ uint64) error {
	return ErrNotImplemented
}

func (b *GRPCBridge) OnSettlementFinalized(_ string) error {
	return ErrNotImplemented
}

func (b *GRPCBridge) SubmitDisputeState(_ string, _ []byte, _ uint64, _ map[uint32][]byte) error {
	return ErrNotImplemented
}
