package pocchallenge

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

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

func Create(ctx context.Context, chain Chain, store *Store, msg *types.MsgCreatePoCChallenge) (*types.MsgCreatePoCChallengeResponse, error) {
	if msg.Creator == msg.Target {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "challenger cannot target itself")
	}
	params, err := chain.GetParams(ctx)
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
	if store.HasOpenChallenge(ctx, msg.Target) {
		return nil, types.ErrPoCChallengeAlreadyOpen
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()

	root, live, err := chain.GetRootGroupDataWithLiveMembers(ctx)
	if err != nil {
		return nil, err
	}
	if !live[msg.Target] {
		return nil, types.ErrActiveParticipantNotFound
	}
	target, found := chain.GetParticipant(ctx, msg.Target)
	if !found || target.Status != types.ParticipantStatus_ACTIVE {
		return nil, types.ErrActiveParticipantNotFound
	}

	epoch, found := chain.GetEffectiveEpoch(ctx)
	if !found || epoch == nil || params.EpochParams == nil {
		return nil, types.ErrEffectiveEpochNotFound
	}
	epochContext, err := types.NewEpochContextFromEffectiveEpoch(*epoch, *params.EpochParams, height)
	if err != nil {
		return nil, err
	}
	if epochContext.GetCurrentPhase(height) != types.InferencePhase {
		return nil, sdkerrors.Wrap(types.ErrPoCChallengeWindow, "not in inference phase")
	}

	event, isActive, err := chain.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return nil, err
	}
	if isActive && event != nil {
		if event.Phase != types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED ||
			height <= event.GetValidationEnd(params.EpochParams) {
			return nil, sdkerrors.Wrap(types.ErrPoCChallengeWindow, "confirmation PoC is active")
		}
	}

	nextPoCStart := epochContext.NextPoCStart()
	safetyHeight := SafetyWindowHeight(nextPoCStart, params.EpochParams.ConfirmationPocSafetyWindow)
	if height >= safetyHeight {
		return nil, sdkerrors.Wrap(types.ErrPoCChallengeWindow, "safety window")
	}
	startHeight := height + 1
	if safetyHeight-startHeight < MinPunishableSegmentBlocks {
		return nil, sdkerrors.Wrap(types.ErrPoCChallengeWindow, "remaining segment shorter than 300 blocks")
	}

	nextEpoch := epochContext.NextEpochContext()
	if blockingMaintenance(ctx, chain, msg.Target, nextEpoch.SetNewValidators()) {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "target has current or scheduled maintenance")
	}

	open, err := store.CountOpen(ctx)
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
	if ConfirmationPocWeight(ConfirmationWeightNodes(ctx, chain, epoch.Index, msg.Target)) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "target has no confirmation-weight hardware")
	}

	bitcoin := params.BitcoinRewardParams
	if bitcoin == nil {
		return nil, fmt.Errorf("bitcoin reward params not set")
	}
	var epochsSinceGenesis uint64
	if epoch.Index >= bitcoin.GenesisEpoch {
		epochsSinceGenesis = epoch.Index - bitcoin.GenesisEpoch
	}
	fixedReward, err := chain.FixedEpochRewardAmount(epochsSinceGenesis, bitcoin.InitialEpochReward, bitcoin.DecayRate)
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
		return nil, types.ErrPoCChallengePaymentZero
	}
	expectedReward := uint64(expected.IntPart())
	ratio := decimal.Zero
	if cp.PaymentRatio != nil {
		ratio = cp.PaymentRatio.ToDecimal()
	}
	locked := ratio.Mul(decimal.NewFromInt(int64(expectedReward))).Floor()
	if !locked.IsPositive() {
		return nil, types.ErrPoCChallengePaymentZero
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
	if err := chain.SendCoinsFromAccountToModule(ctx, challengerAddr, types.ModuleName, coins, "poc_challenge_lock"); err != nil {
		return nil, err
	}

	ch := types.PoCChallenge{
		EpochIndex:           epoch.Index,
		Challenger:           msg.Creator,
		Target:               msg.Target,
		ChallengeStartHeight: startHeight,
		ExpectedReward:       expectedReward,
		LockedPayment:        lockedPayment,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: startHeight,
		}},
	}
	if err := store.Set(ctx, ch); err != nil {
		return nil, err
	}
	chain.LogInfo("PoCChallenge created", types.PoC,
		"challenger", msg.Creator,
		"target", msg.Target,
		"start", startHeight,
		"expected", expectedReward,
		"locked", lockedPayment)
	return &types.MsgCreatePoCChallengeResponse{}, nil
}

func blockingMaintenance(ctx context.Context, chain Chain, target string, beforeHeight int64) bool {
	addr, err := sdk.AccAddressFromBech32(target)
	if err != nil {
		return true
	}
	state, found := chain.GetMaintenanceState(ctx, addr)
	if !found {
		return false
	}
	if state.ActiveReservationId != 0 {
		return true
	}
	if state.ScheduledReservationId == 0 {
		return false
	}
	res, ok := chain.GetMaintenanceReservation(ctx, state.ScheduledReservationId)
	if !ok {
		return false
	}
	return res.StartHeight < beforeHeight
}
