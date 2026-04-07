// Package bridge provides a GRPCBridge that implements subnet/bridge.MainnetBridge
// by talking to the mock mainnet gRPC server instead of a live Cosmos chain.
package bridge

import (
	"context"
	"encoding/hex"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	subnetbridge "subnet/bridge"
	"subnet/state"
	"subnet/types"

	"subnet/testenv/mockchain"
)

// GRPCBridge implements subnet/bridge.MainnetBridge via the mock gRPC server.
// It is used by both subnethost participants and subnetctl when running
// inside the testenv docker-compose network.
type GRPCBridge struct {
	query mockchain.MockQueryClient
	tx    mockchain.MockTxClient
}

// NewGRPCBridge dials the mock server at addr (e.g. "mock-server:9090") and
// returns a ready-to-use GRPCBridge.
func NewGRPCBridge(addr string) (*GRPCBridge, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial mock server %s: %w", addr, err)
	}
	return &GRPCBridge{
		query: mockchain.NewMockQueryClient(conn),
		tx:    mockchain.NewMockTxClient(conn),
	}, nil
}

// GetEscrow fetches the escrow by ID from the mock server.
func (b *GRPCBridge) GetEscrow(escrowID string) (*subnetbridge.EscrowInfo, error) {
	resp, err := b.query.GetEscrow(context.Background(), &mockchain.GetEscrowRequest{EscrowId: escrowID})
	if err != nil {
		return nil, fmt.Errorf("GetEscrow gRPC: %w", err)
	}
	return &subnetbridge.EscrowInfo{
		EscrowID:       resp.EscrowId,
		Amount:         resp.Amount,
		CreatorAddress: resp.CreatorAddress,
		AppHash:        resp.AppHash,
		Slots:          resp.Slots,
		TokenPrice:     resp.TokenPrice,
	}, nil
}

// GetHostInfo fetches a participant's URL by validator address.
func (b *GRPCBridge) GetHostInfo(address string) (*subnetbridge.HostInfo, error) {
	resp, err := b.query.GetParticipant(context.Background(), &mockchain.GetParticipantRequest{Address: address})
	if err != nil {
		return nil, fmt.Errorf("GetParticipant gRPC: %w", err)
	}
	return &subnetbridge.HostInfo{
		Address: resp.Address,
		URL:     resp.Url,
	}, nil
}

// VerifyWarmKey checks whether warmAddress is authorised as a warm key for
// validatorAddress. The mock server always returns all participant addresses,
// so in the testenv warm-key verification always succeeds.
func (b *GRPCBridge) VerifyWarmKey(warmAddress, validatorAddress string) (bool, error) {
	resp, err := b.query.GetGrantees(context.Background(), &mockchain.GetGranteesRequest{
		ValidatorAddress: validatorAddress,
		MessageType:      "/inference.inference.MsgStartInference",
	})
	if err != nil {
		return false, fmt.Errorf("GetGrantees gRPC: %w", err)
	}
	for _, addr := range resp.Addresses {
		if addr == warmAddress {
			return true, nil
		}
	}
	return false, nil
}

// SubmitDisputeState converts a SettlementPayload into a SettleEscrow gRPC
// call and submits it to the mock server for verification + settlement.
func (b *GRPCBridge) SubmitDisputeState(
	escrowID string,
	stateRoot []byte,
	nonce uint64,
	sigs map[uint32][]byte,
) error {
	return subnetbridge.ErrNotImplemented
}

// SubmitSettlement sends a full SettlementPayload to the mock server.
// Call this after a session reaches PhaseSettlement and signatures are collected.
func (b *GRPCBridge) SubmitSettlement(payload *state.SettlementPayload) error {
	req := &mockchain.SettleEscrowRequest{
		EscrowId: payload.EscrowID,
		Nonce:    payload.Nonce,
		RestHash: payload.RestHash,
	}

	for slotID, hs := range payload.HostStats {
		req.HostStats = append(req.HostStats, &mockchain.HostStats{
			SlotId:               slotID,
			Missed:               hs.Missed,
			Invalid:              hs.Invalid,
			Cost:                 hs.Cost,
			RequiredValidations:  hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		})
	}
	for slotID, sig := range payload.Signatures {
		req.Signatures = append(req.Signatures, &mockchain.SlotSignature{
			SlotId:    slotID,
			Signature: sig,
		})
	}

	resp, err := b.tx.SettleEscrow(context.Background(), req)
	if err != nil {
		return fmt.Errorf("SettleEscrow gRPC: %w", err)
	}
	if !resp.Ok {
		return fmt.Errorf("settlement rejected: %s", resp.Message)
	}
	return nil
}

// CreateEscrow triggers the pre-configured escrow creation on the mock server.
// Returns the escrow ID. Idempotent.
func (b *GRPCBridge) CreateEscrow() (string, error) {
	resp, err := b.tx.CreateEscrow(context.Background(), &mockchain.CreateEscrowRequest{})
	if err != nil {
		return "", fmt.Errorf("CreateEscrow gRPC: %w", err)
	}
	return resp.EscrowId, nil
}

// OnEscrowCreated is not used in the testenv (escrow is pre-seeded).
func (b *GRPCBridge) OnEscrowCreated(_ subnetbridge.EscrowInfo) error {
	return subnetbridge.ErrNotImplemented
}

func (b *GRPCBridge) OnSettlementProposed(_ string, _ []byte, _ uint64) error {
	return subnetbridge.ErrNotImplemented
}

func (b *GRPCBridge) OnSettlementFinalized(_ string) error {
	return subnetbridge.ErrNotImplemented
}

// AppHashFromHex decodes a hex app_hash string into bytes.
func AppHashFromHex(h string) ([]byte, error) {
	return hex.DecodeString(h)
}

// HostStatsFromTypes converts the map format used by SettlementPayload.
func HostStatsFromTypes(m map[uint32]*types.HostStats) []*mockchain.HostStats {
	out := make([]*mockchain.HostStats, 0, len(m))
	for slotID, hs := range m {
		out = append(out, &mockchain.HostStats{
			SlotId:               slotID,
			Missed:               hs.Missed,
			Invalid:              hs.Invalid,
			Cost:                 hs.Cost,
			RequiredValidations:  hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		})
	}
	return out
}

// Compile-time interface check.
var _ subnetbridge.MainnetBridge = (*GRPCBridge)(nil)
