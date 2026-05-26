package keeper

import (
	"context"
	"fmt"
	"math"
	"math/bits"
	"slices"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SettleDevshardEscrow(goCtx context.Context, msg *types.MsgSettleDevshardEscrow) (*types.MsgSettleDevshardEscrowResponse, error) {
	if err := k.CheckPermission(goCtx, msg, EscrowAllowListPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	escrow, found := k.GetDevshardEscrow(goCtx, msg.EscrowId)
	if !found {
		return nil, fmt.Errorf("escrow %d not found", msg.EscrowId)
	}

	warmKeyChecker := func(granter, grantee string) bool {
		return k.HasWarmKeyGrant(goCtx, granter, grantee)
	}
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get params: %w", err)
	}
	devshardParams := params.DevshardEscrowParams
	if devshardParams == nil {
		return nil, fmt.Errorf("devshard escrow params not configured")
	}
	if err := VerifyDevshardSettlement(escrow, msg, devshardParams.MaxNonce, warmKeyChecker); err != nil {
		return nil, err
	}

	if len(escrow.Slots) == 0 {
		return nil, fmt.Errorf("escrow %d has no slots", escrow.Id)
	}
	currentEpochIndex, found := k.GetEffectiveEpochIndex(goCtx)
	if !found {
		return nil, fmt.Errorf("failed to get effective epoch index")
	}
	if currentEpochIndex < escrow.EpochIndex || currentEpochIndex > escrow.EpochIndex+1 {
		return nil, fmt.Errorf("devshard settlement only supports current or previous epoch: current epoch %d, escrow epoch %d", currentEpochIndex, escrow.EpochIndex)
	}

	uniqueAddrs := make([]string, 0, len(escrow.Slots))
	seenAddrs := make(map[string]bool, len(escrow.Slots))
	for _, addr := range escrow.Slots {
		if seenAddrs[addr] {
			continue
		}
		seenAddrs[addr] = true
		uniqueAddrs = append(uniqueAddrs, addr)
	}
	slices.Sort(uniqueAddrs)

	participantByAddr := make(map[string]*types.Participant, len(uniqueAddrs))
	treatAsCurrentEpochSettle := make(map[string]bool, len(uniqueAddrs))
	for _, addr := range uniqueAddrs {
		participant, found := k.GetParticipant(goCtx, addr)
		if !found {
			return nil, fmt.Errorf("participant %s not found", addr)
		}
		participantByAddr[addr] = &participant
		if escrow.EpochIndex != currentEpochIndex {
			treatAsCurrentEpochSettle[addr] = false
			continue
		}
		participantAddr, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid participant address %s: %w", addr, err)
		}
		active, err := k.ActiveParticipantsSet.Has(ctx, collections.Join(currentEpochIndex, participantAddr))
		if err != nil {
			return nil, fmt.Errorf("failed to check active participant set for %s: %w", addr, err)
		}
		treatAsCurrentEpochSettle[addr] = active && escrow.EpochIndex == currentEpochIndex
	}
	touchedParticipants := make(map[string]bool)

	totalSlots := uint64(len(escrow.Slots))
	// How much of the total fees will be assigned to each slot
	feePerSlot := msg.Fees / totalSlots
	// Leftover fees; will be distributed 1 per slot
	remainderFees := msg.Fees % totalSlots

	// Aggregate costs + fees per unique validator address (deterministic: iterate by slot order)
	validatorPayouts := make(map[string]uint64)
	for _, hs := range msg.HostStats {
		if int(hs.SlotId) >= len(escrow.Slots) {
			return nil, fmt.Errorf("host_stats slot_id %d out of range", hs.SlotId)
		}
		addr := escrow.Slots[hs.SlotId]

		// Assign cost of running inferences to this slot's validator
		nextValidatorPayout, carry := bits.Add64(validatorPayouts[addr], hs.Cost, 0)
		if carry != 0 {
			return nil, fmt.Errorf("validator cost overflow for %s", addr)
		}

		// Assign fees paid by the user to this slot's validator
		nextValidatorPayout, carry = bits.Add64(nextValidatorPayout, feePerSlot, 0)
		if carry != 0 {
			return nil, fmt.Errorf("validator fee share overflow for %s", addr)
		}

		// If there are remainder fees, distribute 1 additional coin to this slot.
		if remainderFees > 0 {
			nextValidatorPayout, carry = bits.Add64(nextValidatorPayout, 1, 0)
			if carry != 0 {
				return nil, fmt.Errorf("validator remainder fee overflow for %s", addr)
			}
			remainderFees--
		}
		validatorPayouts[addr] = nextValidatorPayout
	}

	// Sanity check
	if remainderFees != 0 {
		return nil, fmt.Errorf("failed to allocate all remainder fees, %d left", remainderFees)
	}

	var totalPayout uint64
	for _, payout := range validatorPayouts {
		nextTotalPayout, carry := bits.Add64(totalPayout, payout, 0)
		if carry != 0 {
			return nil, fmt.Errorf("total validator payout overflow")
		}
		totalPayout = nextTotalPayout
	}
	if totalPayout > escrow.Amount {
		return nil, fmt.Errorf("total payout %d exceeds escrow amount %d", totalPayout, escrow.Amount)
	}

	// Pay validators in slot order (deterministic iteration over escrow.Slots).
	// Each validator receives total accumulated slot costs and fee shares.
	paidValidators := make(map[string]bool)
	for _, addr := range escrow.Slots {
		payout, hasPayout := validatorPayouts[addr]
		if !hasPayout || payout == 0 {
			continue
		}
		if paidValidators[addr] {
			continue
		}
		paidValidators[addr] = true

		recipientAddr, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid validator address %s: %w", addr, err)
		}
		inCurrentEpoch := treatAsCurrentEpochSettle[addr]
		if inCurrentEpoch {
			participant, found := participantByAddr[addr]
			if !found {
				return nil, fmt.Errorf("participant %s not found", addr)
			}
			if err := k.AddToCoinBalance(goCtx, participant, payout, "devshard_settle"); err != nil {
				return nil, err
			}
			touchedParticipants[addr] = true
		} else {
			if err := k.payCoinsDirectly(goCtx, payout, recipientAddr); err != nil {
				return nil, err
			}
		}
	}

	// Refund remainder to creator after validator costs and fee shares.
	remainder := escrow.Amount - totalPayout
	if remainder > 0 {
		if remainder > math.MaxInt64 {
			return nil, fmt.Errorf("refund amount %d exceeds max int64", remainder)
		}
		creatorAddr, err := sdk.AccAddressFromBech32(escrow.Creator)
		if err != nil {
			return nil, fmt.Errorf("invalid creator address: %w", err)
		}
		coins, err := types.GetCoins(int64(remainder))
		if err != nil {
			return nil, fmt.Errorf("invalid refund amount: %w", err)
		}
		err = k.BankKeeper.SendCoinsFromModuleToAccount(goCtx, types.ModuleName, creatorAddr, coins, "devshard_escrow_refund")
		if err != nil {
			return nil, fmt.Errorf("failed to refund creator: %w", err)
		}
	}

	// Aggregate host stats per validator per epoch (deterministic: iterate msg.HostStats by slot_id order)
	seenValidators := make(map[string]bool)
	for _, hs := range msg.HostStats {
		addr := escrow.Slots[hs.SlotId]
		participantAddr, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid participant address %s: %w", addr, err)
		}
		_, seen := seenValidators[addr]
		firstForValidator := !seen
		if err := k.UpdateDevshardHostEpochStats(goCtx, escrow.EpochIndex, participantAddr, *hs, firstForValidator); err != nil {
			return nil, fmt.Errorf("failed to aggregate host stats: %w", err)
		}
		if treatAsCurrentEpochSettle[addr] {
			participant, found := participantByAddr[addr]
			if !found {
				return nil, fmt.Errorf("participant %s not found", addr)
			}
			assignedToSlot, err := devshardAssignedUpperBoundForSlot(msg.Nonce, totalSlots, hs.SlotId)
			if err != nil {
				return nil, fmt.Errorf("failed to derive assigned upper bound for slot %d: %w", hs.SlotId, err)
			}
			if err := AggregateDevshardHostStatsIntoCurrentEpochStats(participant, *hs, assignedToSlot); err != nil {
				return nil, fmt.Errorf("failed to aggregate host stats into participant epoch stats: %w", err)
			}
			touchedParticipants[addr] = true
		}
		if firstForValidator {
			seenValidators[addr] = true
		}
	}

	touchedAddrs := make([]string, 0, len(touchedParticipants))
	for addr := range touchedParticipants {
		touchedAddrs = append(touchedAddrs, addr)
	}
	slices.Sort(touchedAddrs)
	for _, addr := range touchedAddrs {
		participant := participantByAddr[addr]
		if err := k.SetParticipant(goCtx, *participant); err != nil {
			return nil, fmt.Errorf("failed to update participant %s: %w", addr, err)
		}
	}

	escrow.Settled = true
	if err := k.SetDevshardEscrow(goCtx, escrow); err != nil {
		return nil, fmt.Errorf("failed to update escrow: %w", err)
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"devshard_escrow_settled",
		sdk.NewAttribute("escrow_id", fmt.Sprint(escrow.Id)),
		sdk.NewAttribute("version", msg.Version),
		sdk.NewAttribute("settler", msg.Settler),
		sdk.NewAttribute("total_payout", fmt.Sprint(totalPayout)),
		sdk.NewAttribute("fees", fmt.Sprint(msg.Fees)),
		sdk.NewAttribute("remainder", fmt.Sprint(remainder)),
	))

	return &types.MsgSettleDevshardEscrowResponse{}, nil
}

func (k Keeper) payCoinsDirectly(goCtx context.Context, payout uint64, recipientAddr sdk.AccAddress) error {
	if payout > math.MaxInt64 {
		return fmt.Errorf("payout amount %d exceeds max int64", payout)
	}
	coins, err := types.GetCoins(int64(payout))
	if err != nil {
		return fmt.Errorf("invalid payout amount: %w", err)
	}
	err = k.BankKeeper.SendCoinsFromModuleToAccount(goCtx, types.ModuleName, recipientAddr, coins, "devshard_escrow_payment")
	if err != nil {
		return fmt.Errorf("failed to pay validator %s: %w", recipientAddr.String(), err)
	}
	return nil
}
