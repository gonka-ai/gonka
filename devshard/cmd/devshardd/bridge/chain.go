package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"common/chain"
	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/cmd/devshardd/events"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// hostInfoQueryTimeout bounds a single Participant lookup. GetHostInfo has no
// context parameter, so a stalled chain endpoint would otherwise hold the
// caller forever.
const hostInfoQueryTimeout = 10 * time.Second

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

	// warmKeys is the host's copy of the chain's warm-key log. dapi writes it
	// when the feed lists warm keys; otherwise the process Comet socket does.
	// WarmKeyLog is the per-granter catch-up, and HostWarmKeyRefresh is the
	// backstop when both feeds are quiet.
	warmKeys *bridge.WarmKeyLogCopy
}

// HostWarmKeyRefresh is how old a granter's warm-key copy may be on a host
// before an access reloads it in the background. Events are the primary
// feed, so this only bounds the staleness when they are lost.
const HostWarmKeyRefresh = 10 * time.Minute

var (
	_ bridge.MainnetBridge = (*ChainBridge)(nil)
)

// NewChainBridge creates a ChainBridge. submitter may be nil if SubmitDisputeState is not needed.
func NewChainBridge(client *chain.Client, submitter Submitter) *ChainBridge {
	var fetch bridge.WarmKeyLogFetcher
	if client != nil {
		fetch = bridge.NewChainWarmKeyFetcher(client.InferenceQueryClient())
	}
	return &ChainBridge{
		client:    client,
		submitter: submitter,
		warmKeys:  bridge.NewWarmKeyLogCopy(fetch, HostWarmKeyRefresh),
	}
}

// WarmKeys is the host's grant source: the copy of the chain's warm-key log.
func (b *ChainBridge) WarmKeys() *bridge.WarmKeyLogCopy {
	return b.warmKeys
}

// LiveWarmKeys implements bridge.WarmKeyGrantSource.
func (b *ChainBridge) LiveWarmKeys(granter string) ([]bridge.WarmKeyEntry, error) {
	return b.warmKeys.LiveWarmKeys(granter)
}

// WarmKeyEntry implements bridge.WarmKeyGrantSource.
func (b *ChainBridge) WarmKeyEntry(granter string, id uint64) (bridge.WarmKeyEntry, bool, error) {
	return b.warmKeys.WarmKeyEntry(granter, id)
}

// ConfirmWarmKeyBind is the state machine's WarmKeyConfirmer: the log entry
// of cold with id exists and names warm.
func (b *ChainBridge) ConfirmWarmKeyBind(cold, warm string, id uint64) error {
	return bridge.ConfirmWarmKeyBind(b.warmKeys, cold, warm, id)
}

func (b *ChainBridge) Subscribe(l *events.Listener) {
	l.OnDevshardEscrowCreated(func(_ context.Context, e events.DevshardEscrowCreatedEvent) {
		// The websocket event is id-only. Fetch once and hand the escrow to
		// OnEscrowCreatedHandler (directory warm). Do not CreateSession here.
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
	return devshardpkg.ParseEscrowID(escrowID)
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
		return nil, bridge.ClassifyQueryError(fmt.Errorf("DevshardEscrow %s: %w", escrowID, err))
	}
	if resp == nil || !resp.Found || resp.Escrow == nil {
		return nil, bridge.ErrEscrowNotFound
	}

	return bridge.EscrowInfoFromQuery(id, resp.Escrow)
}

func (b *ChainBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hostInfoQueryTimeout)
	defer cancel()
	resp, err := b.client.InferenceQueryClient().Participant(ctx,
		&inferencetypes.QueryGetParticipantRequest{Index: address})
	if err != nil {
		return nil, bridge.ClassifyQueryError(fmt.Errorf("Participant %s: %w", address, err))
	}

	return &bridge.HostInfo{
		Address: resp.Participant.Address,
		URL:     resp.Participant.InferenceUrl,
	}, nil
}

func (b *ChainBridge) GetValidationThreshold(epochID uint64, modelID string) (*bridge.Decimal, error) {
	snapshot, err := b.modelSnapshot(context.Background(), epochID, modelID)
	if err != nil {
		return nil, err
	}
	if snapshot.ValidationThreshold == nil {
		return nil, fmt.Errorf("validation threshold not found for epoch %d model %s", epochID, modelID)
	}
	return &bridge.Decimal{
		Value:    snapshot.ValidationThreshold.Value,
		Exponent: snapshot.ValidationThreshold.Exponent,
	}, nil
}

// GetModelSource returns the Hugging Face repo and commit the chain pins for the model in the epoch.
func (b *ChainBridge) GetModelSource(ctx context.Context, epochID uint64, modelID string) (hfRepo, hfCommit string, err error) {
	snapshot, err := b.modelSnapshot(ctx, epochID, modelID)
	if err != nil {
		return "", "", err
	}
	return snapshot.HfRepo, snapshot.HfCommit, nil
}

func (b *ChainBridge) modelSnapshot(ctx context.Context, epochID uint64, modelID string) (*inferencetypes.Model, error) {
	resp, err := b.client.InferenceQueryClient().EpochGroupData(ctx,
		&inferencetypes.QueryGetEpochGroupDataRequest{
			EpochIndex: epochID,
			ModelId:    modelID,
		})
	if err != nil {
		return nil, fmt.Errorf("EpochGroupData epoch=%d model=%s: %w", epochID, modelID, err)
	}
	if resp == nil || resp.EpochGroupData.ModelSnapshot == nil {
		return nil, fmt.Errorf("model snapshot not found for epoch %d model %s", epochID, modelID)
	}
	return resp.EpochGroupData.ModelSnapshot, nil
}

// VerifyWarmKey reports whether warmAddress holds a live warm-key grant from
// validatorAddress, from the log copy. It is the local-admission answer
// (transport membership, unbound payload fetches, repair). Diff application
// uses ConsensusWarmKey, which does not consult the clock.
func (b *ChainBridge) VerifyWarmKey(warmAddress, validatorAddress string) (bool, error) {
	return bridge.IsWarmKeyLive(b.warmKeys, validatorAddress, warmAddress, time.Now())
}

// ConsensusWarmKey reports whether warmAddress is named by a grant from
// validatorAddress that the log has not ended. Expiration is ignored, so two
// hosts applying the same diff agree when their clocks differ.
func (b *ChainBridge) ConsensusWarmKey(warmAddress, validatorAddress string) (bool, error) {
	if b == nil || b.warmKeys == nil {
		return false, fmt.Errorf("warm-key log: copy is nil")
	}
	return b.warmKeys.HasUnendedGrant(validatorAddress, warmAddress)
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
