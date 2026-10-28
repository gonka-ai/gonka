package bridge

import (
	"context"
	"fmt"
	"time"

	"common/chain"
	devshardpkg "devshard"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// hostInfoQueryTimeout bounds a single Participant lookup. GetHostInfo has no
// context parameter, so a stalled chain endpoint would otherwise hold the
// caller forever.
const hostInfoQueryTimeout = 10 * time.Second

// GRPCBridge implements MainnetBridge query methods via common/chain gRPC.
// Notification and action methods return ErrNotImplemented. Its warm-key
// answers come from the chain's warm-key log through a copy shared by every
// bridge on the same client (WarmKeys).
type GRPCBridge struct {
	client   *chain.Client
	warmKeys *WarmKeyLogCopy
}

// NewGRPCBridge creates a bridge backed by an existing chain gRPC client.
func NewGRPCBridge(client *chain.Client) *GRPCBridge {
	return &GRPCBridge{client: client, warmKeys: sharedWarmKeyCopy(client)}
}

// WarmKeys is the bridge's grant source: the copy of the chain's warm-key
// log.
func (b *GRPCBridge) WarmKeys() *WarmKeyLogCopy {
	if b == nil {
		return nil
	}
	return b.warmKeys
}

// LiveWarmKeys implements WarmKeyGrantSource.
func (b *GRPCBridge) LiveWarmKeys(granter string) ([]WarmKeyEntry, error) {
	if b == nil || b.warmKeys == nil {
		return nil, fmt.Errorf("grpc bridge: chain client is nil")
	}
	return b.warmKeys.LiveWarmKeys(granter)
}

// WarmKeyEntry implements WarmKeyGrantSource.
func (b *GRPCBridge) WarmKeyEntry(granter string, id uint64) (WarmKeyEntry, bool, error) {
	if b == nil || b.warmKeys == nil {
		return WarmKeyEntry{}, false, fmt.Errorf("grpc bridge: chain client is nil")
	}
	return b.warmKeys.WarmKeyEntry(granter, id)
}

// NewGRPCBridgeFromURL dials grpcURL and returns a bridge on direct gRPC only.
// Caller must not close the underlying connection while the bridge is in use.
//
// Production wiring builds the bridge from the gateway's chain client
// (NewGRPCBridge), so it inherits that client's CometBFT RPC query fallback.
// This constructor exists for tests that want to exercise gRPC itself, where a
// silent fallback would hide the failure under test.
func NewGRPCBridgeFromURL(grpcURL string) (*GRPCBridge, error) {
	client, err := chain.New(grpcURL)
	if err != nil {
		return nil, err
	}
	return NewGRPCBridge(client), nil
}

func parseEscrowID(escrowID string) (uint64, error) {
	return devshardpkg.ParseEscrowID(escrowID)
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
		return nil, ClassifyQueryError(fmt.Errorf("DevshardEscrow %s: %w", escrowID, err))
	}
	if resp == nil || !resp.Found || resp.Escrow == nil {
		return nil, ErrEscrowNotFound
	}

	return EscrowInfoFromQuery(id, resp.Escrow)
}

func (b *GRPCBridge) GetHostInfo(address string) (*HostInfo, error) {
	if b == nil || b.client == nil {
		return nil, fmt.Errorf("grpc bridge: chain client is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostInfoQueryTimeout)
	defer cancel()
	resp, err := b.client.InferenceQueryClient().Participant(ctx,
		&inferencetypes.QueryGetParticipantRequest{Index: address})
	if status.Code(err) == codes.NotFound {
		return nil, fmt.Errorf("Participant %s: %w", address, ErrParticipantNotFound)
	}
	if err != nil {
		return nil, ClassifyQueryError(fmt.Errorf("Participant %s: %w", address, err))
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

// VerifyWarmKey reports whether warmAddress holds a live warm-key grant from
// validatorAddress, from the log copy. It is the local-admission answer; a
// key becomes a consensus actor only through a MsgWarmKeyBind the hosts
// confirm against the same log.
func (b *GRPCBridge) VerifyWarmKey(warmAddress, validatorAddress string) (bool, error) {
	if b == nil || b.warmKeys == nil {
		return false, fmt.Errorf("grpc bridge: chain client is nil")
	}
	return IsWarmKeyLive(b.warmKeys, validatorAddress, warmAddress, time.Now())
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
