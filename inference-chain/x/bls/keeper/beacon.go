package keeper

// Block BLS beacon, Step 3 (devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md):
// the beacon key (rule R9), the per-round pending message, the slot owner
// index and the structural check of vote extensions. Nothing here verifies
// pairings or aggregates signatures (Step 5).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/blsutil"
	"github.com/productscience/inference/x/bls/types"
)

const (
	// BeaconExtensionVersion is the only BeaconVoteExtension version.
	BeaconExtensionVersion = 1
	// beaconPendingKeepRounds: EndBlock deletes pending messages older than
	// this many rounds (safety sweep; Step 5 deletes them when aggregating).
	beaconPendingKeepRounds = 10
	// Size cap: 64 bytes per slot (48-byte signature + index + framing)
	// plus fixed overhead, never below beaconExtensionMinCapBytes.
	beaconExtensionBytesPerSlot  = 64
	beaconExtensionOverheadBytes = 512
	beaconExtensionMinCapBytes   = 4096
)

var (
	ErrBeaconExtensionVersion = errors.New("beacon extension: unsupported version")
	ErrBeaconExtensionRound   = errors.New("beacon extension: wrong round")
	ErrBeaconExtensionEpoch   = errors.New("beacon extension: wrong key epoch")
	ErrBeaconExtensionSlots   = errors.New("beacon extension: slot indices are not the signer's slot range")
	ErrBeaconExtensionSigLen  = errors.New("beacon extension: signature length does not match slot count")
	ErrBeaconExtensionSize    = errors.New("beacon extension: too large")
	ErrBeaconNoPending        = errors.New("beacon extension: no pending beacon for round")
	ErrBeaconNoOwner          = errors.New("beacon extension: signer owns no slots in the key epoch")
)

// MaxBeaconExtensionBytes is the largest acceptable extension for a key epoch
// with totalSlots slots (0 = the default parameter).
func MaxBeaconExtensionBytes(totalSlots uint32) int {
	if totalSlots == 0 {
		totalSlots = types.DefaultParams().ITotalSlots
	}
	n := int(totalSlots)*beaconExtensionBytesPerSlot + beaconExtensionOverheadBytes
	if n < beaconExtensionMinCapBytes {
		return beaconExtensionMinCapBytes
	}
	return n
}

// SetStakingKeeper wires x/staking for slot owner resolution and the coverage
// check. Without it, owner lookups fail and the coverage check is skipped.
func (k *Keeper) SetStakingKeeper(sk types.StakingKeeper) { k.stakingKeeper = sk }

// ---------------------------------------------------------------------------
// Activation (rule R6)
// ---------------------------------------------------------------------------

// GetBeaconActivation returns the activation, and false before the beacon is live.
func (k Keeper) GetBeaconActivation(ctx sdk.Context) (types.BeaconActivation, bool) {
	var a types.BeaconActivation
	return a, k.getProto(ctx, types.BeaconActivationKey, &a)
}

// ActivateBeacon makes the beacon live at height: partials in vote extensions
// for heights > height (Step 3) and the beacon tx required from block
// height+1 on (Step 4). Idempotent: an existing activation is kept unchanged.
// It also seeds the beacon key from the newest completed DKG among
// candidateEpochs. Returns whether this call activated.
func (k Keeper) ActivateBeacon(ctx sdk.Context, height uint64, candidateEpochs []uint64) bool {
	if _, ok := k.GetBeaconActivation(ctx); ok {
		return false
	}
	k.setProto(ctx, types.BeaconActivationKey, &types.BeaconActivation{ExtFrom: height, InjFrom: height})
	if _, ok := k.GetBeaconKeyEpoch(ctx); !ok {
		if epoch, ok := k.newestCompletedEpoch(ctx, candidateEpochs); ok {
			k.setBeaconKeyEpoch(ctx, epoch)
			k.rebuildBeaconSlotOwners(ctx, epoch)
			k.Logger().Info("beacon key seeded", "key_epoch", epoch)
		}
	}
	k.Logger().Info("block beacon activated", "ext_from", height, "inj_from", height)
	return true
}

// BeaconInjectionRound reports whether block proposalHeight must carry the
// beacon tx (σ or a proof of miss) and returns the pending round it is for:
// the beacon is activated, proposalHeight > inj_from, and round
// proposalHeight-1 has a PendingBeacon. Otherwise the block must not carry it.
func (k Keeper) BeaconInjectionRound(ctx sdk.Context, proposalHeight int64) (types.PendingBeacon, bool) {
	a, ok := k.GetBeaconActivation(ctx)
	if !ok || proposalHeight <= 1 || uint64(proposalHeight) <= a.InjFrom {
		return types.PendingBeacon{}, false
	}
	return k.GetPendingBeacon(ctx, uint64(proposalHeight-1))
}

// BeaconKeyMaterial is what checking a key epoch's signatures needs.
type BeaconKeyMaterial struct {
	GroupPublicKey []byte
	SlotPublicKeys [][]byte
	// Threshold is the number of valid slots σ needs: t_slots_degree + 1.
	Threshold uint32
}

// GetBeaconKeyMaterial returns the group key, slot keys and threshold of a
// completed key epoch.
func (k Keeper) GetBeaconKeyMaterial(ctx sdk.Context, epoch uint64) (BeaconKeyMaterial, error) {
	data, err := k.getEpochBLSDataBase(ctx, epoch)
	if err != nil {
		return BeaconKeyMaterial{}, err
	}
	if !dkgFinal(data.DkgPhase) {
		return BeaconKeyMaterial{}, fmt.Errorf("beacon: epoch %d DKG is %s", epoch, data.DkgPhase)
	}
	if len(data.GroupPublicKey) == 0 || len(data.SlotPublicKeys) < int(data.ITotalSlots) {
		return BeaconKeyMaterial{}, fmt.Errorf("beacon: epoch %d key material incomplete", epoch)
	}
	return BeaconKeyMaterial{
		GroupPublicKey: data.GroupPublicKey,
		SlotPublicKeys: data.SlotPublicKeys,
		Threshold:      data.TSlotsDegree + 1,
	}, nil
}

// BeaconExtensionsActive reports whether validators sign partials at height.
func (k Keeper) BeaconExtensionsActive(ctx sdk.Context, height int64) bool {
	a, ok := k.GetBeaconActivation(ctx)
	return ok && height > 0 && uint64(height) > a.ExtFrom
}

// newestCompletedEpoch is the highest epoch in ids whose DKG is COMPLETED or SIGNED.
func (k Keeper) newestCompletedEpoch(ctx sdk.Context, ids []uint64) (uint64, bool) {
	var best uint64
	found := false
	for _, id := range ids {
		if id == 0 || (found && id <= best) {
			continue
		}
		data, err := k.getEpochBLSDataBase(ctx, id)
		if err != nil || !dkgFinal(data.DkgPhase) {
			continue
		}
		best, found = id, true
	}
	return best, found
}

func dkgFinal(phase types.DKGPhase) bool {
	return phase == types.DKGPhase_DKG_PHASE_COMPLETED || phase == types.DKGPhase_DKG_PHASE_SIGNED
}

// ---------------------------------------------------------------------------
// Beacon key (rule R9)
// ---------------------------------------------------------------------------

// GetBeaconKeyEpoch returns the BLS epoch signing beacon rounds.
func (k Keeper) GetBeaconKeyEpoch(ctx sdk.Context) (uint64, bool) {
	v, err := k.storeService.OpenKVStore(ctx).Get(types.BeaconKeyEpochKey)
	if err != nil || len(v) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(v), true
}

func (k Keeper) setBeaconKeyEpoch(ctx sdk.Context, epoch uint64) {
	v := make([]byte, 8)
	binary.BigEndian.PutUint64(v, epoch)
	if err := k.storeService.OpenKVStore(ctx).Set(types.BeaconKeyEpochKey, v); err != nil {
		k.Logger().Error("beacon: set key epoch", "err", err)
	}
}

// GetBeaconKeyCandidate returns the completed DKG waiting to become the key.
func (k Keeper) GetBeaconKeyCandidate(ctx sdk.Context) (types.BeaconKeyCandidate, bool) {
	var c types.BeaconKeyCandidate
	return c, k.getProto(ctx, types.BeaconKeyCandidateKey, &c)
}

// onDKGCompleted records a newly completed DKG as the next beacon key
// candidate (rule R9): it signs from completedHeight+1+delay on. Called from
// finalizeDisputingPhase; a no-op before activation (pre-upgrade replay must
// not write state) and for epochs not newer than the current key/candidate.
func (k Keeper) onDKGCompleted(ctx sdk.Context, epoch uint64) {
	if _, ok := k.GetBeaconActivation(ctx); !ok {
		return
	}
	if cur, ok := k.GetBeaconKeyEpoch(ctx); ok && epoch <= cur {
		return
	}
	if c, ok := k.GetBeaconKeyCandidate(ctx); ok && epoch <= c.Epoch {
		return
	}
	delay := types.DefaultBeaconKeySwitchDelay
	if params, err := k.GetParams(ctx); err == nil {
		delay = params.BeaconKeySwitchDelay
	}
	cand := types.BeaconKeyCandidate{Epoch: epoch, FromRound: uint64(ctx.BlockHeight()) + 1 + delay}
	k.setProto(ctx, types.BeaconKeyCandidateKey, &cand)
	k.Logger().Info("beacon key candidate", "epoch", epoch, "from_round", cand.FromRound)
}

// ---------------------------------------------------------------------------
// Pending message
// ---------------------------------------------------------------------------

// GetPendingBeacon returns the message validators sign for round.
func (k Keeper) GetPendingBeacon(ctx sdk.Context, round uint64) (types.PendingBeacon, bool) {
	var p types.PendingBeacon
	return p, k.getProto(ctx, types.BeaconPendingKey(round), &p)
}

// ProcessBlockBeacon is the beacon part of EndBlock(H): switch to a candidate
// key whose from_round is H+1, write PendingBeacon(H+1) when vote extensions
// are enabled at H+1 and a key exists, and sweep old pending messages. It
// never returns an error that should halt the chain; failures are logged.
func (k Keeper) ProcessBlockBeacon(ctx sdk.Context) {
	if _, ok := k.GetBeaconActivation(ctx); !ok {
		return
	}
	h := ctx.BlockHeight()
	if h <= 0 {
		return
	}
	next := uint64(h) + 1

	if c, ok := k.GetBeaconKeyCandidate(ctx); ok && next >= c.FromRound {
		prev, _ := k.GetBeaconKeyEpoch(ctx)
		k.setBeaconKeyEpoch(ctx, c.Epoch)
		k.deleteKey(ctx, types.BeaconKeyCandidateKey)
		k.rebuildBeaconSlotOwners(ctx, c.Epoch)
		if prev != 0 && prev != c.Epoch {
			k.deleteBeaconSlotOwners(ctx, prev)
		}
		_ = ctx.EventManager().EmitTypedEvent(&types.EventBeaconKeySwitched{
			KeyEpoch: c.Epoch, PreviousKeyEpoch: prev, FromRound: next,
		})
		k.Logger().Info("beacon key switched", "key_epoch", c.Epoch, "previous_key_epoch", prev, "from_round", next)
	}

	k.sweepPendingBeacons(ctx, next)

	var enable int64
	if cp := ctx.ConsensusParams(); cp.Abci != nil {
		enable = cp.Abci.VoteExtensionsEnableHeight
	}
	if enable <= 0 || int64(next) < enable {
		return
	}
	keyEpoch, ok := k.GetBeaconKeyEpoch(ctx)
	if !ok {
		return // no completed DKG yet: Finalize will record MISS_NO_KEY (Step 5)
	}
	data, err := k.getEpochBLSDataBase(ctx, keyEpoch)
	if err != nil {
		k.Logger().Error("beacon: key epoch data missing", "key_epoch", keyEpoch, "err", err)
		return
	}
	msg, err := blsutil.BeaconMessage(ctx.ChainID(), keyEpoch, next, ctx.HeaderInfo().Hash)
	if err != nil {
		k.Logger().Error("beacon: message", "round", next, "err", err)
		return
	}
	k.setProto(ctx, types.BeaconPendingKey(next), &types.PendingBeacon{
		Round: next, KeyEpoch: keyEpoch, Msg: msg[:], ITotalSlots: data.ITotalSlots,
	})
}

// sweepPendingBeacons deletes pending messages for rounds older than
// next-beaconPendingKeepRounds.
func (k Keeper) sweepPendingBeacons(ctx sdk.Context, next uint64) {
	if next <= beaconPendingKeepRounds {
		return
	}
	store := k.storeService.OpenKVStore(ctx)
	it, err := store.Iterator(types.BeaconPendingPrefix, types.BeaconPendingKey(next-beaconPendingKeepRounds))
	if err != nil {
		return
	}
	var keys [][]byte
	for ; it.Valid(); it.Next() {
		keys = append(keys, append([]byte(nil), it.Key()...))
	}
	it.Close()
	for _, key := range keys {
		_ = store.Delete(key)
	}
}

// ---------------------------------------------------------------------------
// Slot owners
// ---------------------------------------------------------------------------

// rebuildBeaconSlotOwners persists epoch → participant account → slot range
// for the key epoch and runs the coverage check. Consensus addresses are not
// stored: they are resolved through x/staking at lookup time.
func (k Keeper) rebuildBeaconSlotOwners(ctx sdk.Context, epoch uint64) {
	data, err := k.getEpochBLSDataBase(ctx, epoch)
	if err != nil {
		k.Logger().Error("beacon: slot owners: epoch data", "epoch", epoch, "err", err)
		return
	}
	k.deleteBeaconSlotOwners(ctx, epoch)
	for _, p := range data.Participants {
		acc, err := sdk.AccAddressFromBech32(p.Address)
		if err != nil {
			k.Logger().Error("beacon: slot owners: participant address", "address", p.Address, "err", err)
			continue
		}
		k.setProto(ctx, types.BeaconSlotOwnerKey(epoch, acc), &types.BeaconSlotOwner{
			ParticipantAddress: p.Address, SlotStart: p.SlotStartIndex, SlotEnd: p.SlotEndIndex,
		})
	}
	k.checkBeaconSlotCoverage(ctx, &data)
}

func (k Keeper) deleteBeaconSlotOwners(ctx sdk.Context, epoch uint64) {
	store := k.storeService.OpenKVStore(ctx)
	prefix := types.BeaconSlotOwnerEpochPrefix(epoch)
	it, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return
	}
	var keys [][]byte
	for ; it.Valid(); it.Next() {
		keys = append(keys, append([]byte(nil), it.Key()...))
	}
	it.Close()
	for _, key := range keys {
		_ = store.Delete(key)
	}
}

// GetBeaconSlotOwner returns the slot range the validator with consAddr owns
// in epoch, resolving consAddr → validator → operator account through
// x/staking now (so a rotated consensus key still maps to the same slots).
func (k Keeper) GetBeaconSlotOwner(ctx sdk.Context, epoch uint64, consAddr []byte) (types.BeaconSlotOwner, bool) {
	if k.stakingKeeper == nil || len(consAddr) == 0 {
		return types.BeaconSlotOwner{}, false
	}
	val, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, sdk.ConsAddress(consAddr))
	if err != nil {
		return types.BeaconSlotOwner{}, false
	}
	oper, err := sdk.ValAddressFromBech32(val.GetOperator())
	if err != nil {
		return types.BeaconSlotOwner{}, false
	}
	var owner types.BeaconSlotOwner
	if !k.getProto(ctx, types.BeaconSlotOwnerKey(epoch, sdk.AccAddress(oper)), &owner) {
		return types.BeaconSlotOwner{}, false
	}
	owner.ConsAddr = append([]byte(nil), consAddr...)
	return owner, true
}

// checkBeaconSlotCoverage warns when the validators that can commit a block
// with just over 2/3 of voting power may hold fewer slots than the threshold:
// greedily drop the validators with the most slots per unit of power while the
// dropped power stays below 1/3, and count the slots that remain.
func (k Keeper) checkBeaconSlotCoverage(ctx sdk.Context, data *types.EpochBLSData) {
	if k.stakingKeeper == nil {
		return
	}
	vals, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		k.Logger().Error("beacon: coverage: bonded validators", "err", err)
		return
	}
	reduction := k.stakingKeeper.PowerReduction(ctx)
	slotsOf := make(map[string]uint32, len(data.Participants))
	for _, p := range data.Participants {
		if acc, err := sdk.AccAddressFromBech32(p.Address); err == nil && p.SlotEndIndex >= p.SlotStartIndex {
			slotsOf[string(acc)] += p.SlotEndIndex - p.SlotStartIndex + 1
		}
	}
	members := make([]BeaconCoverageMember, 0, len(vals))
	var totalPower int64
	covered := make(map[string]bool, len(vals))
	for _, v := range vals {
		power := v.GetConsensusPower(reduction)
		if power <= 0 {
			continue
		}
		oper, err := sdk.ValAddressFromBech32(v.GetOperator())
		if err != nil {
			continue
		}
		key := string(sdk.AccAddress(oper))
		covered[key] = true
		members = append(members, BeaconCoverageMember{Power: power, Slots: slotsOf[key]})
		totalPower += power
	}
	if totalPower == 0 {
		return // no bonded set yet (e.g. InitGenesis before x/staking): nothing to judge
	}
	var withoutValidator uint32
	for acc, n := range slotsOf {
		if !covered[acc] {
			withoutValidator += n
		}
	}
	worst := WorstCaseBeaconSlots(totalPower, members)
	threshold := data.TSlotsDegree + 1
	if worst < threshold {
		_ = ctx.EventManager().EmitTypedEvent(&types.EventBeaconSlotCoverageWarning{
			KeyEpoch: data.EpochId, WorstCaseSlots: worst, ThresholdSlots: threshold, SlotsWithoutValidator: withoutValidator,
		})
		k.Logger().Warn("beacon slot coverage below threshold", "key_epoch", data.EpochId,
			"worst_case_slots", worst, "threshold_slots", threshold, "slots_without_validator", withoutValidator)
	}
}

// BeaconCoverageMember is one bonded validator in the coverage check.
type BeaconCoverageMember struct {
	Power int64
	Slots uint32 // slots its operator account owns in the key epoch
}

// WorstCaseBeaconSlots returns the slots held by the worst-case set of
// validators that still has more than 2/3 of totalPower: validators are
// dropped greedily, most slots per unit of power first, while the dropped
// power stays strictly below 1/3 of totalPower.
func WorstCaseBeaconSlots(totalPower int64, members []BeaconCoverageMember) uint32 {
	if totalPower <= 0 {
		return 0
	}
	ms := append([]BeaconCoverageMember(nil), members...)
	var remaining uint32
	for _, m := range ms {
		remaining += m.Slots
	}
	sort.SliceStable(ms, func(i, j int) bool {
		// slots_i/power_i > slots_j/power_j, without division.
		li := math.NewInt(int64(ms[i].Slots)).Mul(math.NewInt(ms[j].Power))
		lj := math.NewInt(int64(ms[j].Slots)).Mul(math.NewInt(ms[i].Power))
		return li.GT(lj)
	})
	var dropped int64
	for _, m := range ms {
		if (dropped+m.Power)*3 >= totalPower {
			continue // dropping it would leave at most 2/3: it must sign
		}
		dropped += m.Power
		remaining -= m.Slots
	}
	return remaining
}

// ---------------------------------------------------------------------------
// Vote extension structure (used by FinalizeBeacon, Step 5)
// ---------------------------------------------------------------------------

// ValidateBeaconExtensionStructure checks a non-empty extension against the
// pending beacon of round and the signer's slot range, without pairings:
// version, round, key epoch, slot indices == the owner's full range,
// signature length and total size. An empty extension is a valid "no
// contribution" and must be filtered out by the caller before this check.
func (k Keeper) ValidateBeaconExtensionStructure(ctx sdk.Context, ext []byte, round uint64, consAddr []byte) (*types.BeaconVoteExtension, error) {
	pending, ok := k.GetPendingBeacon(ctx, round)
	if !ok {
		return nil, ErrBeaconNoPending
	}
	if len(ext) > MaxBeaconExtensionBytes(pending.ITotalSlots) {
		return nil, ErrBeaconExtensionSize
	}
	var ve types.BeaconVoteExtension
	if err := ve.Unmarshal(ext); err != nil {
		return nil, fmt.Errorf("beacon extension: decode: %w", err)
	}
	if ve.Version != BeaconExtensionVersion {
		return nil, ErrBeaconExtensionVersion
	}
	if ve.Round != round {
		return nil, ErrBeaconExtensionRound
	}
	if ve.KeyEpoch != pending.KeyEpoch {
		return nil, ErrBeaconExtensionEpoch
	}
	owner, ok := k.GetBeaconSlotOwner(ctx, pending.KeyEpoch, consAddr)
	if !ok {
		return nil, ErrBeaconNoOwner
	}
	if !IsSlotRange(ve.SlotIndices, owner.SlotStart, owner.SlotEnd) {
		return nil, ErrBeaconExtensionSlots
	}
	if len(ve.Signatures) != blsutil.SignatureLen*len(ve.SlotIndices) {
		return nil, ErrBeaconExtensionSigLen
	}
	return &ve, nil
}

// IsSlotRange reports whether idx is exactly start, start+1, ..., end.
func IsSlotRange(idx []uint32, start, end uint32) bool {
	if end < start || len(idx) != int(end-start)+1 {
		return false
	}
	for i, v := range idx {
		if v != start+uint32(i) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// store helpers
// ---------------------------------------------------------------------------

type protoMsg interface {
	Marshal() ([]byte, error)
	Unmarshal([]byte) error
}

func (k Keeper) getProto(ctx sdk.Context, key []byte, m protoMsg) bool {
	v, err := k.storeService.OpenKVStore(ctx).Get(key)
	if err != nil || v == nil {
		return false
	}
	return m.Unmarshal(v) == nil
}

func (k Keeper) setProto(ctx sdk.Context, key []byte, m protoMsg) {
	b, err := m.Marshal()
	if err != nil {
		k.Logger().Error("beacon: marshal", "key", string(bytes.TrimRight(key, "\x00")), "err", err)
		return
	}
	if err := k.storeService.OpenKVStore(ctx).Set(key, b); err != nil {
		k.Logger().Error("beacon: store", "err", err)
	}
}

func (k Keeper) deleteKey(ctx sdk.Context, key []byte) {
	_ = k.storeService.OpenKVStore(ctx).Delete(key)
}

// getEpochBLSDataBase reads EpochBLSData without rehydrating dealer parts
// and verification submissions (the beacon needs participants, phase, slot
// counts and keys only).
func (k Keeper) getEpochBLSDataBase(ctx sdk.Context, epochID uint64) (types.EpochBLSData, error) {
	v, err := k.storeService.OpenKVStore(ctx).Get(types.EpochBLSDataKey(epochID))
	if err != nil {
		return types.EpochBLSData{}, err
	}
	if v == nil {
		return types.EpochBLSData{}, types.ErrEpochBLSDataNotFound
	}
	var data types.EpochBLSData
	if err := k.cdc.Unmarshal(v, &data); err != nil {
		return types.EpochBLSData{}, err
	}
	return data, nil
}
