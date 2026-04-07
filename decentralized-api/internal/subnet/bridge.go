package subnet

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"

	"decentralized-api/cosmosclient"

	"subnet/bridge"

	"github.com/productscience/inference/x/inference/types"
)

// ChainBridge implements bridge.MainnetBridge via gRPC through CosmosMessageClient.
type ChainBridge struct {
	client cosmosclient.CosmosMessageClient
}

func NewChainBridge(client cosmosclient.CosmosMessageClient) *ChainBridge {
	return &ChainBridge{client: client}
}

func (b *ChainBridge) GetEscrow(escrowID string) (*bridge.EscrowInfo, error) {
	id, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse escrow id: %w", err)
	}

	ctx := context.Background()
	qc := b.client.NewInferenceQueryClient()

	resp, err := qc.SubnetEscrow(ctx, &types.QueryGetSubnetEscrowRequest{Id: id})
	if err != nil {
		return nil, fmt.Errorf("query subnet escrow: %w", err)
	}
	if resp == nil || !resp.Found || resp.Escrow == nil {
		return nil, bridge.ErrEscrowNotFound
	}

	appHash, err := hex.DecodeString(resp.Escrow.AppHash)
	if err != nil {
		return nil, fmt.Errorf("decode app_hash: %w", err)
	}

	return &bridge.EscrowInfo{
		EscrowID:       escrowID,
		Amount:         resp.Escrow.Amount,
		CreatorAddress: resp.Escrow.Creator,
		AppHash:        appHash,
		Slots:          resp.Escrow.Slots,
		TokenPrice:     resp.Escrow.TokenPrice,
	}, nil
}

func (b *ChainBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	ctx := context.Background()
	qc := b.client.NewInferenceQueryClient()

	resp, err := qc.Participant(ctx, &types.QueryGetParticipantRequest{Index: address})
	if err != nil {
		return nil, fmt.Errorf("query participant: %w", err)
	}

	return &bridge.HostInfo{
		Address: resp.Participant.Address,
		URL:     resp.Participant.InferenceUrl,
	}, nil
}

const warmKeyMsgType = "/inference.inference.MsgStartInference"

func (b *ChainBridge) VerifyWarmKey(warmAddress, validatorAddress string) (bool, error) {
	ctx := context.Background()
	qc := b.client.NewInferenceQueryClient()

	resp, err := qc.GranteesByMessageType(ctx, &types.QueryGranteesByMessageTypeRequest{
		GranterAddress: validatorAddress,
		MessageTypeUrl: warmKeyMsgType,
	})
	if err != nil {
		return false, fmt.Errorf("query grantees: %w", err)
	}

	for _, g := range resp.Grantees {
		if g.Address == warmAddress {
			return true, nil
		}
	}
	return false, nil
}

func (b *ChainBridge) OnEscrowCreated(_ bridge.EscrowInfo) error { return bridge.ErrNotImplemented }
func (b *ChainBridge) OnSettlementProposed(_ string, _ []byte, _ uint64) error {
	return bridge.ErrNotImplemented
}
func (b *ChainBridge) OnSettlementFinalized(_ string) error { return bridge.ErrNotImplemented }
func (b *ChainBridge) SubmitDisputeState(_ string, _ []byte, _ uint64, _ map[uint32][]byte) error {
	return bridge.ErrNotImplemented
}

// Compile-time check.
var _ bridge.MainnetBridge = (*ChainBridge)(nil)
