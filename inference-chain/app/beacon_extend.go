package app

// Block BLS beacon, Step 3: real vote extensions. ExtendVote signs the round's
// pending beacon message with this validator's slot shares (one partial per
// slot) and attaches them to its precommit. Nothing is aggregated here
// (Step 5); VerifyVoteExtension stays a size cap only (rules R1, R2).

import (
	"fmt"
	"sync"
	"time"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/app/beacon"
	"github.com/productscience/inference/x/bls/blsutil"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
)

// beaconSlowExtension: building an extension slower than this is logged
// (plan budget: 50 ms total, ~0.5 ms per slot).
const beaconSlowExtension = 50 * time.Millisecond

// beaconState is what the extension builder reads from x/bls committed state.
type beaconState interface {
	BeaconExtensionsActive(ctx sdk.Context, height int64) bool
	GetPendingBeacon(ctx sdk.Context, round uint64) (blstypes.PendingBeacon, bool)
}

// extensionLog logs the "why empty" and "signed" lines once per key epoch and
// reason, so a validator without shares is visible without flooding the log.
type extensionLog struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (l *extensionLog) once(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	if l.seen[key] {
		return false
	}
	if len(l.seen) > 256 {
		l.seen = map[string]bool{}
	}
	l.seen[key] = true
	return true
}

// beaconExtensionBuilder builds the Step 3 vote extension. It returns an empty
// extension (never an error) whenever this node cannot contribute: before
// activation, without a pending message, or without verified shares for the
// pending key epoch.
func beaconExtensionBuilder(logger log.Logger, state beaconState, provider func() beacon.ShareProvider) extensionBuilder {
	var once extensionLog
	empty := func(reason string, height int64, epoch uint64, msg string, kv ...any) ([]byte, error) {
		beacon.ExtendVoteEmpty.WithLabelValues(reason).Inc()
		beacon.ExtendVoteSlots.Set(0)
		if msg != "" && once.once(fmt.Sprintf("%s/%d", reason, epoch)) {
			logger.Info(msg, append([]any{"height", height, "key_epoch", epoch, "reason", reason}, kv...)...)
		}
		return []byte{}, nil
	}
	return func(ctx sdk.Context, req *abci.RequestExtendVote) (ext []byte, err error) {
		start := time.Now()
		defer func() {
			if r := recover(); r != nil {
				beacon.ExtendVoteEmpty.WithLabelValues(beacon.EmptyPanic).Inc()
				beacon.ExtendVoteSlots.Set(0)
				ext, err = nil, fmt.Errorf("panic building beacon extension: %v", r)
			}
			beacon.ExtendVoteDuration.Observe(time.Since(start).Seconds())
		}()

		if req.Height <= 0 || !state.BeaconExtensionsActive(ctx, req.Height) {
			return empty(beacon.EmptyInactive, req.Height, 0, "")
		}
		round := uint64(req.Height)
		pending, ok := state.GetPendingBeacon(ctx, round)
		if !ok {
			return empty(beacon.EmptyNoPending, req.Height, 0, "")
		}
		p := provider()
		if p == nil {
			return empty(beacon.EmptyNoShares, req.Height, pending.KeyEpoch, "beacon extension empty: share provider disabled")
		}
		shares, ok := p.Get(ctx, pending.KeyEpoch)
		if !ok {
			return empty(beacon.EmptyNoShares, req.Height, pending.KeyEpoch,
				"beacon extension empty: no verified shares for the key epoch (normal if this validator holds no BLS slots in it)")
		}
		if shares.EpochID != pending.KeyEpoch || shares.NumSlots() == 0 ||
			int(shares.SlotEnd-shares.SlotStart)+1 != shares.NumSlots() {
			return empty(beacon.EmptyNotOwner, req.Height, pending.KeyEpoch, "beacon extension empty: shares do not match the key epoch",
				"shares_epoch", shares.EpochID)
		}

		sigs, err := blsutil.SignPartials(pending.Msg, shares.Scalars)
		if err != nil {
			return empty(beacon.EmptyError, req.Height, pending.KeyEpoch, "beacon extension empty: signing failed", "err", err.Error())
		}
		slots := make([]uint32, shares.NumSlots())
		for i := range slots {
			slots[i] = shares.SlotStart + uint32(i)
		}
		ve := blstypes.BeaconVoteExtension{
			Version:     blskeeper.BeaconExtensionVersion,
			Round:       round,
			KeyEpoch:    pending.KeyEpoch,
			SlotIndices: slots,
			Signatures:  sigs,
		}
		out, err := ve.Marshal()
		if err != nil {
			return empty(beacon.EmptyError, req.Height, pending.KeyEpoch, "beacon extension empty: marshal failed", "err", err.Error())
		}
		if limit := blskeeper.MaxBeaconExtensionBytes(pending.ITotalSlots); len(out) > limit {
			// Peers would reject our precommit: never send it.
			return empty(beacon.EmptyOversize, req.Height, pending.KeyEpoch, "beacon extension empty: over the size cap",
				"len", len(out), "max", limit)
		}

		beacon.ExtendVoteSigned.Inc()
		beacon.ExtendVoteSlots.Set(float64(len(slots)))
		took := time.Since(start)
		if took > beaconSlowExtension {
			logger.Warn("slow beacon extension", "height", req.Height, "slots", len(slots), "took", took.String())
		}
		if once.once(fmt.Sprintf("signed/%d", pending.KeyEpoch)) {
			logger.Info("beacon extension built", "round", round, "epoch", pending.KeyEpoch, "slots", len(slots), "took", took.String())
		} else {
			logger.Debug("beacon extension built", "round", round, "epoch", pending.KeyEpoch, "slots", len(slots), "took", took.String())
		}
		return out, nil
	}
}

// beaconSizeCap returns the vote-extension size cap at height: enough for a
// validator owning every slot of the larger of the current i_total_slots and
// the round's key epoch (an epoch keyed before a parameter change may have
// more slots than the parameter now says).
func beaconSizeCap(params func(ctx sdk.Context) (blstypes.Params, error), state beaconState) extensionSizeCap {
	return func(ctx sdk.Context, height int64) int {
		var slots uint32
		if p, err := params(ctx); err == nil {
			slots = p.ITotalSlots
		}
		if height > 0 && state != nil {
			if pending, ok := state.GetPendingBeacon(ctx, uint64(height)); ok && pending.ITotalSlots > slots {
				slots = pending.ITotalSlots
			}
		}
		return blskeeper.MaxBeaconExtensionBytes(slots)
	}
}
