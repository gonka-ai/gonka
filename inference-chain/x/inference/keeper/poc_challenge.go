package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

func (k Keeper) GetPoCChallenge(ctx context.Context, target string) (types.PoCChallenge, bool, error) {
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return types.PoCChallenge{}, false, err
	}
	ch, err := k.PoCChallenges.Get(ctx, addr)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.PoCChallenge{}, false, nil
		}
		return types.PoCChallenge{}, false, err
	}
	return ch, true, nil
}

func (k Keeper) SetPoCChallenge(ctx context.Context, ch types.PoCChallenge) error {
	addr, err := sdk.AccAddressFromBech32(ch.Target)
	if err != nil {
		return err
	}
	return k.PoCChallenges.Set(ctx, addr, ch)
}

func (k Keeper) ListPoCChallenges(ctx context.Context) ([]types.PoCChallenge, error) {
	iter, err := k.PoCChallenges.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	return iter.Values()
}

func (k Keeper) CountPoCChallenges(ctx context.Context) (int, error) {
	list, err := k.ListPoCChallenges(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ch := range list {
		if ch.State == types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
			n++
		}
	}
	return n, nil
}

func (k Keeper) SameEpochChallengeTargets(ctx context.Context, epochIndex uint64) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	list, err := k.ListPoCChallenges(ctx)
	if err != nil {
		return nil, err
	}
	for _, ch := range list {
		if ch.EpochIndex == epochIndex && ch.Target != "" {
			out[ch.Target] = struct{}{}
		}
	}
	return out, nil
}

func (k Keeper) ChallengeSafetyFinish(ctx context.Context, ch types.PoCChallenge) (int64, error) {
	params, err := k.GetParams(ctx)
	if err != nil {
		return 0, err
	}
	if params.EpochParams == nil {
		return 0, sdkerrors.Wrap(types.ErrIllegalState, "epoch params not set")
	}
	epoch, found := k.GetEpoch(ctx, ch.EpochIndex)
	if !found || epoch == nil {
		return 0, sdkerrors.Wrap(types.ErrEffectiveEpochNotFound, "challenge epoch not found")
	}
	epochContext := types.NewEpochContext(*epoch, *params.EpochParams)
	return SafetyWindowHeight(epochContext.NextPoCStart(), params.EpochParams.ConfirmationPocSafetyWindow), nil
}

func SafetyWindowHeight(nextPoCStart, safetyWindow int64) int64 {
	return nextPoCStart - safetyWindow
}

func (k Keeper) ChallengeFinish(ctx context.Context, ch types.PoCChallenge) (int64, error) {
	safety, err := k.ChallengeSafetyFinish(ctx, ch)
	if err != nil {
		return 0, err
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return 0, err
	}
	event, ok, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return 0, err
	}
	if ok && event != nil &&
		event.EpochIndex == ch.EpochIndex &&
		event.Phase != types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED &&
		params.EpochParams != nil {
		return event.GetExchangeEnd(params.EpochParams) + 1, nil
	}
	return safety, nil
}

// IsUnderChallenge is true while the record is OPEN and height is below the safety window.
func (k Keeper) IsUnderChallenge(ctx context.Context, addr string) bool {
	ch, found, err := k.GetPoCChallenge(ctx, addr)
	if err != nil || !found {
		return false
	}
	if ch.State != types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
		return false
	}
	safety, err := k.ChallengeSafetyFinish(ctx, ch)
	if err != nil {
		return false
	}
	return sdk.UnwrapSDKContext(ctx).BlockHeight() < safety
}

func (k Keeper) HasActiveChallengeRecord(ctx context.Context, addr string) bool {
	ch, found, err := k.GetPoCChallenge(ctx, addr)
	if err != nil || !found {
		return false
	}
	if ch.State != types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
		return false
	}
	epoch, ok := k.GetEffectiveEpochIndex(ctx)
	if !ok {
		return false
	}
	return ch.EpochIndex == epoch
}

func (k Keeper) ListChallengeCommits(ctx context.Context, target string) ([]types.PoCV2StoreCommit, error) {
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return nil, err
	}
	iter, err := k.PoCChallengeCommits.Iterate(ctx, collections.NewPrefixedPairRange[sdk.AccAddress, string](addr))
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	return iter.Values()
}

func (k Keeper) ListChallengeValidations(ctx context.Context, target string) ([]types.PoCValidationV2, error) {
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return nil, err
	}
	iter, err := k.PoCChallengeValidations.Iterate(ctx, collections.NewPrefixedTripleRange[sdk.AccAddress, string, sdk.AccAddress](addr))
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	return iter.Values()
}

func (k Keeper) DeleteChallengeSegmentData(ctx context.Context, target string) error {
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return err
	}
	commitIter, err := k.PoCChallengeCommits.Iterate(ctx, collections.NewPrefixedPairRange[sdk.AccAddress, string](addr))
	if err != nil {
		return err
	}
	var commitKeys []collections.Pair[sdk.AccAddress, string]
	for ; commitIter.Valid(); commitIter.Next() {
		key, err := commitIter.Key()
		if err != nil {
			commitIter.Close()
			return err
		}
		commitKeys = append(commitKeys, key)
	}
	commitIter.Close()
	for _, key := range commitKeys {
		if err := k.PoCChallengeCommits.Remove(ctx, key); err != nil {
			return err
		}
	}

	valIter, err := k.PoCChallengeValidations.Iterate(ctx, collections.NewPrefixedTripleRange[sdk.AccAddress, string, sdk.AccAddress](addr))
	if err != nil {
		return err
	}
	var valKeys []collections.Triple[sdk.AccAddress, string, sdk.AccAddress]
	for ; valIter.Valid(); valIter.Next() {
		key, err := valIter.Key()
		if err != nil {
			valIter.Close()
			return err
		}
		valKeys = append(valKeys, key)
	}
	valIter.Close()
	for _, key := range valKeys {
		if err := k.PoCChallengeValidations.Remove(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func (k Keeper) DeletePoCChallenge(ctx context.Context, target string) error {
	if err := k.DeleteChallengeSegmentData(ctx, target); err != nil {
		return err
	}
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return err
	}
	return k.PoCChallenges.Remove(ctx, addr)
}

// MarkChallengeAborted closes a challenge without a failure.
func (k Keeper) MarkChallengeAborted(ctx context.Context, target, cause string) error {
	ch, found, err := k.GetPoCChallenge(ctx, target)
	if err != nil || !found {
		return err
	}
	if ch.State != types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
		return nil
	}
	ch.State = types.PoCChallengeState_POC_CHALLENGE_STATE_ABORTED
	if err := k.SetPoCChallenge(ctx, ch); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"poc_challenge_aborted",
		sdk.NewAttribute("target", target),
		sdk.NewAttribute("challenger", ch.Challenger),
		sdk.NewAttribute("cause", cause),
	))
	return k.DeleteChallengeSegmentData(ctx, target)
}

func (k Keeper) MarkChallengeFailed(ctx context.Context, target string) error {
	ch, found, err := k.GetPoCChallenge(ctx, target)
	if err != nil || !found {
		return err
	}
	if ch.State != types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
		return nil
	}
	ch.State = types.PoCChallengeState_POC_CHALLENGE_STATE_CHALLENGE_FAILED
	return k.SetPoCChallenge(ctx, ch)
}

func (k Keeper) MarkChallengePassed(ctx context.Context, target string) error {
	ch, found, err := k.GetPoCChallenge(ctx, target)
	if err != nil || !found {
		return err
	}
	if ch.State != types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
		return nil
	}
	ch.State = types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED
	return k.SetPoCChallenge(ctx, ch)
}

func (k Keeper) RotateChallengeSegment(ctx context.Context, target string, startHeight int64, seed []byte) error {
	ch, found, err := k.GetPoCChallenge(ctx, target)
	if err != nil || !found {
		return err
	}
	if ch.State != types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
		return nil
	}
	if err := k.DeleteChallengeSegmentData(ctx, target); err != nil {
		return err
	}
	ch.StartHeight = startHeight
	ch.Seed = seed
	return k.SetPoCChallenge(ctx, ch)
}

func allowedChallenger(params *types.PoCChallengeParams, creator string) bool {
	if params == nil || len(params.AllowedChallengers) == 0 {
		return false
	}
	for _, addr := range params.AllowedChallengers {
		if addr == creator {
			return true
		}
	}
	return false
}

func blockingMaintenance(ctx context.Context, k Keeper, target string, beforeHeight int64) bool {
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return true
	}
	state, found := k.GetMaintenanceState(ctx, addr)
	if !found {
		return false
	}
	if state.ActiveReservationId != 0 {
		return true
	}
	if state.ScheduledReservationId == 0 {
		return false
	}
	res, ok := k.GetMaintenanceReservation(ctx, state.ScheduledReservationId)
	if !ok {
		return false
	}
	return res.StartHeight < beforeHeight
}

func (k Keeper) CreatePoCChallenge(ctx context.Context, msg *types.MsgCreatePoCChallenge) (*types.MsgCreatePoCChallengeResponse, error) {
	if msg.Creator == msg.Target {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "challenger cannot target itself")
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	cp := params.PocChallengeParams
	if cp == nil {
		cp = types.DefaultPoCChallengeParams()
	}
	if !allowedChallenger(cp, msg.Creator) {
		return nil, types.ErrPoCChallengeNotAllowed
	}
	if _, found, err := k.GetPoCChallenge(ctx, msg.Target); err != nil {
		return nil, err
	} else if found {
		return nil, types.ErrPoCChallengeAlreadyOpen
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()

	root, live, err := k.GetRootGroupDataWithLiveMembers(ctx)
	if err != nil {
		return nil, err
	}
	if !live[msg.Target] {
		return nil, types.ErrActiveParticipantNotFound
	}
	target, found := k.GetParticipant(ctx, msg.Target)
	if !found || target.Status != types.ParticipantStatus_ACTIVE {
		return nil, types.ErrActiveParticipantNotFound
	}

	epoch, found := k.GetEffectiveEpoch(ctx)
	if !found || epoch == nil || params.EpochParams == nil {
		return nil, types.ErrEffectiveEpochNotFound
	}
	epochContext := types.NewEpochContext(*epoch, *params.EpochParams)
	if epochContext.GetCurrentPhase(height) != types.InferencePhase {
		return nil, sdkerrors.Wrap(types.ErrPoCChallengeWindow, "not in inference phase")
	}

	event, isActive, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return nil, err
	}
	if isActive && event != nil {
		return nil, sdkerrors.Wrap(types.ErrPoCChallengeWindow, "confirmation PoC is active")
	}

	if params.ConfirmationPocParams == nil || params.ConfirmationPocParams.AlphaThreshold == nil ||
		params.ConfirmationPocParams.AlphaThreshold.ToDecimal().IsZero() {
		return nil, sdkerrors.Wrap(types.ErrPoCChallengeWindow, "confirmation PoC alpha is unset")
	}

	nextPoCStart := epochContext.NextPoCStart()
	safetyHeight := SafetyWindowHeight(nextPoCStart, params.EpochParams.ConfirmationPocSafetyWindow)
	if height >= safetyHeight {
		return nil, sdkerrors.Wrap(types.ErrPoCChallengeWindow, "safety window")
	}
	startHeight := height + 1
	minPunishable := types.EffectiveMinPunishableSegmentBlocks(cp)
	if safetyHeight-startHeight < minPunishable {
		return nil, sdkerrors.Wrapf(types.ErrPoCChallengeWindow, "remaining segment shorter than %d blocks", minPunishable)
	}

	nextEpoch := epochContext.NextEpochContext()
	if blockingMaintenance(ctx, k, msg.Target, nextEpoch.SetNewValidators()) {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "target has current or scheduled maintenance")
	}

	open, err := k.CountPoCChallenges(ctx)
	if err != nil {
		return nil, err
	}
	if uint32(open) >= cp.MaxActiveChallenges {
		return nil, types.ErrPoCChallengeCapExceeded
	}

	var targetWeight, totalWeight int64
	for _, vw := range root.ValidationWeights {
		if vw == nil {
			continue
		}
		totalWeight += vw.Weight
		if vw.MemberAddress == msg.Target {
			targetWeight = vw.Weight
		}
	}
	if targetWeight <= 0 || totalWeight <= 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "target has no current-epoch weight")
	}

	bitcoin := params.BitcoinRewardParams
	if bitcoin == nil {
		return nil, fmt.Errorf("bitcoin reward params not set")
	}
	var epochsSinceGenesis uint64
	if epoch.Index >= bitcoin.GenesisEpoch {
		epochsSinceGenesis = epoch.Index - bitcoin.GenesisEpoch
	}
	fixedReward, err := CalculateFixedEpochReward(epochsSinceGenesis, bitcoin.InitialEpochReward, bitcoin.DecayRate)
	if err != nil {
		return nil, err
	}

	denom := safetyHeight - epochContext.SetNewValidators()
	if denom <= 0 {
		return nil, sdkerrors.Wrap(types.ErrPoCChallengeWindow, "invalid scale denominator")
	}
	eFull := decimal.NewFromInt(targetWeight).
		Div(decimal.NewFromInt(totalWeight)).
		Mul(decimal.NewFromInt(int64(fixedReward)))
	scale := decimal.NewFromInt(safetyHeight - height).Div(decimal.NewFromInt(denom))
	expected := eFull.Mul(scale).Floor()
	if !expected.IsPositive() {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "PoC challenge locked payment is zero")
	}
	expectedReward := uint64(expected.IntPart())
	ratio := decimal.Zero
	if cp.PaymentRatio != nil {
		ratio = cp.PaymentRatio.ToDecimal()
	}
	locked := ratio.Mul(decimal.NewFromInt(int64(expectedReward))).Floor()
	if !locked.IsPositive() {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "PoC challenge locked payment is zero")
	}
	lockedPayment := uint64(locked.IntPart())

	challengerAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, err.Error())
	}
	coins, err := types.GetCoins(int64(lockedPayment))
	if err != nil {
		return nil, err
	}
	if err := k.BankKeeper.SendCoinsFromAccountToModule(ctx, challengerAddr, types.ModuleName, coins, "poc_challenge_lock"); err != nil {
		return nil, err
	}

	ch := types.PoCChallenge{
		EpochIndex:     epoch.Index,
		Challenger:     msg.Creator,
		Target:         msg.Target,
		ExpectedReward: expectedReward,
		LockedPayment:  lockedPayment,
		StartHeight:    startHeight,
		Seed:           sdkCtx.HeaderInfo().Hash,
	}
	if err := k.SetPoCChallenge(ctx, ch); err != nil {
		return nil, err
	}
	k.LogInfo("PoCChallenge created", types.PoC,
		"challenger", msg.Creator,
		"target", msg.Target,
		"start", startHeight,
		"expected", expectedReward,
		"locked", lockedPayment)
	return &types.MsgCreatePoCChallengeResponse{}, nil
}

func (k Keeper) PayAndDeleteOldChallenges(ctx context.Context, newEffectiveEpoch uint64) {
	list, err := k.ListPoCChallenges(ctx)
	if err != nil {
		k.LogError("PayAndDeleteOldChallenges: failed to list challenges", types.PoC, "error", err)
		return
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("PayAndDeleteOldChallenges: failed to get params", types.PoC, "error", err)
		return
	}
	var vesting *uint64
	if params.TokenomicsParams != nil && params.TokenomicsParams.RewardVestingPeriod > 0 {
		v := params.TokenomicsParams.RewardVestingPeriod
		vesting = &v
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, ch := range list {
		if ch.EpochIndex >= newEffectiveEpoch {
			continue
		}
		cacheCtx, writeFn := sdkCtx.CacheContext()
		if ch.State == types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
			if err := k.MarkChallengeAborted(cacheCtx, ch.Target, "unresolved_at_settlement"); err != nil {
				k.LogError("PayAndDeleteOldChallenges: failed to abort unresolved challenge", types.PoC,
					"target", ch.Target, "error", err)
				continue
			}
			ch.State = types.PoCChallengeState_POC_CHALLENGE_STATE_ABORTED
		}
		if err := k.payLockedChallengePayment(cacheCtx, ch, vesting); err != nil {
			k.LogError("PayAndDeleteOldChallenges: payout failed", types.PoC,
				"target", ch.Target, "challenger", ch.Challenger, "error", err)
			sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
				"epoch_error",
				sdk.NewAttribute("stage", "poc_challenge_payout"),
				sdk.NewAttribute("target", ch.Target),
				sdk.NewAttribute("challenger", ch.Challenger),
				sdk.NewAttribute("P", fmt.Sprintf("%d", ch.LockedPayment)),
			))
			continue
		}
		if err := k.DeletePoCChallenge(cacheCtx, ch.Target); err != nil {
			k.LogError("PayAndDeleteOldChallenges: delete failed", types.PoC,
				"target", ch.Target, "error", err)
			sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
				"epoch_error",
				sdk.NewAttribute("stage", "poc_challenge_payout"),
				sdk.NewAttribute("target", ch.Target),
				sdk.NewAttribute("challenger", ch.Challenger),
				sdk.NewAttribute("P", fmt.Sprintf("%d", ch.LockedPayment)),
			))
			continue
		}
		writeFn()
	}
}

func (k Keeper) payLockedChallengePayment(ctx context.Context, ch types.PoCChallenge, vesting *uint64) error {
	switch ch.State {
	case types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED:
		if ch.LockedPayment == 0 {
			return nil
		}
		return k.PayParticipantFromModule(ctx, ch.Target, int64(ch.LockedPayment), types.ModuleName, "poc_challenge_pass", vesting)
	case types.PoCChallengeState_POC_CHALLENGE_STATE_CHALLENGE_FAILED, types.PoCChallengeState_POC_CHALLENGE_STATE_ABORTED:
		if ch.LockedPayment == 0 {
			return nil
		}
		// TODO: pay min(E, forfeited reward) to the challenger on CHALLENGE_FAILED.
		challenger, err := sdk.AccAddressFromBech32(ch.Challenger)
		if err != nil {
			return err
		}
		coins, err := types.GetCoins(int64(ch.LockedPayment))
		if err != nil {
			return err
		}
		return k.BankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, challenger, coins, "poc_challenge_refund")
	default:
		return fmt.Errorf("challenge %s is not terminal", ch.Target)
	}
}

// filterOutChallengeParticipants removes members currently under challenge.
// Hardware is on challenge PoC, so they must not receive new inference work.
func (k Keeper) filterOutChallengeParticipants(ctx context.Context, members []*group.GroupMember) []*group.GroupMember {
	blocked := make(map[string]struct{})
	list, err := k.ListPoCChallenges(ctx)
	if err != nil {
		return members
	}
	for _, ch := range list {
		if k.IsUnderChallenge(ctx, ch.Target) {
			blocked[ch.Target] = struct{}{}
		}
	}
	if len(blocked) == 0 {
		return members
	}
	filtered := make([]*group.GroupMember, 0, len(members))
	for _, member := range members {
		if member == nil || member.Member == nil {
			continue
		}
		if _, ok := blocked[member.Member.Address]; ok {
			continue
		}
		filtered = append(filtered, member)
	}
	return filtered
}

func (k Keeper) WaiveDevshardMissesForActiveChallenge(
	ctx context.Context,
	host string,
	hostStats types.DevshardSettlementHostStats,
	assignedToSlot uint64,
) (types.DevshardSettlementHostStats, uint64) {
	if !k.HasActiveChallengeRecord(ctx, host) {
		return hostStats, assignedToSlot
	}
	original := uint64(hostStats.Missed)
	hostStats.Missed = 0
	if assignedToSlot >= original {
		assignedToSlot -= original
	} else {
		assignedToSlot = 0
	}
	return hostStats, assignedToSlot
}
