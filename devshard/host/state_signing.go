package host

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"common/chainoracle/blocks"
	"devshard/types"
	"google.golang.org/protobuf/proto"
)

// stateSigningSlots rechecks startup slot ownership against the exact state
// nonce. A replica can outlive its warm grant or a sequenced nonce revoke.
// Host membership is immutable; the state machine synchronizes its own reads.
func (h *Host) stateSigningSlots(nonce uint64) []uint32 {
	if !types.RequiresMainnetHeightProof(h.sm.ProtocolVersion()) {
		return h.sortedSlots
	}
	actors := h.SlotActorsAt(nonce)
	var slots []uint32
	for _, slot := range h.sortedSigningSlots {
		if actors.Allows(slot, h.signer.Address()) {
			slots = append(slots, slot)
		}
	}
	return slots
}

type mainnetTipObservation struct {
	header           *blocks.Header
	receivedAt       time.Time
	checkpointHeight int64
	checkpointHash   []byte
}

func (h *Host) reusableMainnetTip(observation mainnetTipObservation, now time.Time) *blocks.Header {
	if observation.header == nil || now.Sub(observation.receivedAt) > time.Second {
		return nil
	}
	cp := h.sm.MainnetCheckpoint()
	if cp.Height != observation.checkpointHeight || !bytes.Equal(cp.Hash, observation.checkpointHash) {
		return nil
	}
	return observation.header
}

// signState selects ordinary state signing or attestation of a durable final
// state. The two operations use the same wire signature but separate policies.
// Caller holds h.mu, so the final identity cannot change while signing.
func (h *Host) signState(nonce uint64, root []byte, observations ...mainnetTipObservation) ([]byte, []uint32, error) {
	content := &types.StateSignatureContent{StateRoot: root, EscrowId: h.escrowID, Nonce: nonce}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if h.store != nil && types.RequiresMainnetHeightProof(h.sm.ProtocolVersion()) && h.sm.Phase() == types.PhaseSettlement {
		return h.signFinalState(ctx, content)
	}
	if err := h.checkAdmittedState(nonce); err != nil {
		return nil, nil, err
	}
	slots := h.stateSigningSlots(nonce)
	if len(slots) == 0 {
		return nil, nil, types.ErrWarmKeyUnconfirmed
	}
	now := time.Now()
	var tip *blocks.Header
	if len(observations) > 0 {
		tip = h.reusableMainnetTip(observations[0], now)
	}
	if err := h.sm.CheckMainnetHeightFreshnessWithTip(ctx, now, tip); err != nil {
		return nil, nil, err
	}
	return h.signStateContent(content, slots)
}

// signFinalState attests only the exact final (escrow, nonce, root) already
// admitted by this host. Fresh authority evidence is independent of that frozen
// state. It neither appends a diff nor changes the checkpoint or accounting.
// Caller holds h.mu. The existing empty-diff retry invokes this operation;
// no additional RPC or signature format is needed.
func (h *Host) signFinalState(ctx context.Context, final *types.StateSignatureContent) ([]byte, []uint32, error) {
	if h.store == nil {
		return nil, nil, fmt.Errorf("%w: final attestation requires durable admission", types.ErrHeaderPending)
	}
	if final == nil || final.EscrowId != h.escrowID {
		return nil, nil, types.ErrInvalidTransition
	}
	if err := h.checkAdmittedState(final.Nonce); err != nil {
		return nil, nil, err
	}
	stored, ok, err := h.store.DiffStateHash(h.escrowID, final.Nonce)
	if err != nil || !ok || !bytes.Equal(stored, final.StateRoot) {
		return nil, nil, fmt.Errorf("%w: final attestation root is not the admitted durable root", types.ErrHeaderPending)
	}
	slots, err := h.sm.FinalStateSigningSlots(ctx, final, h.signer.Address(), h.sortedSigningSlots)
	if err != nil {
		return nil, nil, err
	}
	return h.signStateContent(final, slots)
}

func (h *Host) checkAdmittedState(nonce uint64) error {
	if h.store == nil || !types.RequiresMainnetHeightProof(h.sm.ProtocolVersion()) {
		return nil
	}
	meta, err := h.store.GetSessionMeta(h.escrowID)
	if err != nil {
		return fmt.Errorf("%w: host admission prefix: %v", types.ErrHeaderPending, err)
	}
	if meta.HostAdmissionNonce < nonce {
		return fmt.Errorf("%w: unchecked host history through nonce %d", types.ErrHeaderPending, nonce)
	}
	return nil
}

func (h *Host) signStateContent(content *types.StateSignatureContent, slots []uint32) ([]byte, []uint32, error) {
	data, err := proto.Marshal(content)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal state sig content: %w", err)
	}
	sig, err := h.signer.Sign(data)
	return sig, slots, err
}
