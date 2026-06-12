package devshard

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"

	"devshard/bridge"
	devshardtypes "devshard/types"

	"github.com/productscience/inference/x/inference/types"
)

// ChainBridge implements bridge.MainnetBridge via gRPC through CosmosMessageClient.
type ChainBridge struct {
	client InferenceQueryClientProvider
}

func NewChainBridge(client InferenceQueryClientProvider) *ChainBridge {
	return &ChainBridge{client: client}
}

func (b *ChainBridge) GetEscrow(escrowID string) (*bridge.EscrowInfo, error) {
	id, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse escrow id: %w", err)
	}

	ctx := context.Background()
	qc := b.client.NewInferenceQueryClient()

	resp, err := qc.DevshardEscrow(ctx, &types.QueryGetDevshardEscrowRequest{Id: id})
	if err != nil {
		return nil, fmt.Errorf("query devshard escrow: %w", err)
	}
	if resp == nil || !resp.Found || resp.Escrow == nil {
		return nil, bridge.ErrEscrowNotFound
	}

	appHash, err := hex.DecodeString(resp.Escrow.AppHash)
	if err != nil {
		return nil, fmt.Errorf("decode app_hash: %w", err)
	}

	return &bridge.EscrowInfo{
		EscrowID:                  escrowID,
		Amount:                    resp.Escrow.Amount,
		CreatorAddress:            resp.Escrow.Creator,
		AppHash:                   appHash,
		Slots:                     resp.Escrow.Slots,
		TokenPrice:                resp.Escrow.TokenPrice,
		CreateDevshardFee:         resp.Escrow.CreateDevshardFee,
		FeePerNonce:               resp.Escrow.FeePerNonce,
		InferenceSealGraceNonces:  resp.Escrow.InferenceSealGraceNonces,
		InferenceSealGraceSeconds: resp.Escrow.InferenceSealGraceSeconds,
		AutoSealEveryNNonces:      resp.Escrow.AutoSealEveryNNonces,
		EpochID:                   resp.Escrow.EpochIndex,
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

func (b *ChainBridge) GetValidationThreshold(epochID uint64, modelID string) (*bridge.Decimal, error) {
	ctx := context.Background()
	qc := b.client.NewInferenceQueryClient()

	resp, err := qc.EpochGroupData(ctx, &types.QueryGetEpochGroupDataRequest{
		EpochIndex: epochID,
		ModelId:    modelID,
	})
	if err != nil {
		return nil, fmt.Errorf("query model validation threshold: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("model snapshot not found for epoch %d model %s", epochID, modelID)
	}
	epochGroupData := resp.GetEpochGroupData()
	if epochGroupData.ModelSnapshot == nil {
		return nil, fmt.Errorf("model snapshot not found for epoch %d model %s", epochID, modelID)
	}
	threshold := epochGroupData.ModelSnapshot.ValidationThreshold
	if threshold == nil {
		return nil, fmt.Errorf("validation threshold missing for epoch %d model %s", epochID, modelID)
	}
	return &bridge.Decimal{Value: threshold.Value, Exponent: threshold.Exponent}, nil
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

func (b *ChainBridge) GetSessionBindParams() (devshardtypes.LiveSessionBindParams, error) {
	ctx := context.Background()
	qc := b.client.NewInferenceQueryClient()

	resp, err := qc.Params(ctx, &types.QueryParamsRequest{})
	if err != nil {
		return devshardtypes.LiveSessionBindParams{}, fmt.Errorf("query params: %w", err)
	}
	if resp == nil || resp.Params.DevshardEscrowParams == nil {
		return devshardtypes.LiveSessionBindParams{}, fmt.Errorf("devshard escrow params missing from chain params response")
	}
	dep := resp.Params.DevshardEscrowParams
	return devshardtypes.LiveSessionBindParams{
		RefusalTimeout:      dep.RefusalTimeout,
		ExecutionTimeout:    dep.ExecutionTimeout,
		ValidationRate:      dep.ValidationRate,
		VoteThresholdFactor: dep.VoteThresholdFactor,
	}, nil
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
var (
	_ bridge.MainnetBridge         = (*ChainBridge)(nil)
	_ bridge.SessionBindParamsBridge = (*ChainBridge)(nil)
)
