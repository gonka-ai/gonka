package app

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// ProvideGonkaGovTallyFn supplies a CalculateVoteResultsAndVotingPowerFn to x/gov via depinject
// that iterates ALL bonded PoC validators (not capped at MaxValidators=100).
func ProvideGonkaGovTallyFn(sk *stakingkeeper.Keeper) govkeeper.CalculateVoteResultsAndVotingPowerFn {
	return func(
		ctx context.Context,
		k govkeeper.Keeper,
		proposal v1.Proposal,
	) (math.LegacyDec, math.Int, map[v1.VoteOption]math.LegacyDec, error) {
		validators := make(map[string]v1.ValidatorGovInfo)
		totalValPower := math.ZeroInt()

		if err := sk.IterateValidators(ctx, func(_ int64, validator stakingtypes.ValidatorI) bool {
			if !validator.IsBonded() || validator.GetTokens().IsZero() || validator.GetDelegatorShares().IsZero() {
				return false
			}
			valBz, err := sk.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
			if err != nil {
				return false
			}
			validatorPower := validator.GetValidatorPower()
			validators[validator.GetOperator()] = v1.NewValidatorGovInfo(
				valBz,
				validatorPower,
				validator.GetDelegatorShares(),
				math.LegacyZeroDec(),
				v1.WeightedVoteOptions{},
			)
			totalValPower = totalValPower.Add(validatorPower)
			return false
		}); err != nil {
			return math.LegacyZeroDec(), math.ZeroInt(), nil, err
		}

		totalVotingPower := math.LegacyZeroDec()
		results := map[v1.VoteOption]math.LegacyDec{
			v1.OptionYes:        math.LegacyZeroDec(),
			v1.OptionAbstain:    math.LegacyZeroDec(),
			v1.OptionNo:         math.LegacyZeroDec(),
			v1.OptionNoWithVeto: math.LegacyZeroDec(),
		}

		rng := collections.NewPrefixedPairRange[uint64, sdk.AccAddress](proposal.Id)
		var votesToRemove []collections.Pair[uint64, sdk.AccAddress]
		err := k.Votes.Walk(ctx, rng, func(key collections.Pair[uint64, sdk.AccAddress], vote v1.Vote) (bool, error) {
			voter := key.K2()
			valAddrStr, err := sk.ValidatorAddressCodec().BytesToString(voter)
			if err != nil {
				return false, err
			}
			if val, ok := validators[valAddrStr]; ok {
				val.Vote = vote.Options
				validators[valAddrStr] = val
			}

			err = sk.IterateDelegations(ctx, voter, func(_ int64, delegation stakingtypes.DelegationI) bool {
				valAddrStr := delegation.GetValidatorAddr()
				if val, ok := validators[valAddrStr]; ok {
					val.DelegatorDeductions = val.DelegatorDeductions.Add(delegation.GetShares())
					validators[valAddrStr] = val

					votingPower := delegation.GetShares().MulInt(val.BondedTokens).Quo(val.DelegatorShares)
					for _, option := range vote.Options {
						weight, _ := math.LegacyNewDecFromStr(option.Weight)
						results[option.Option] = results[option.Option].Add(votingPower.Mul(weight))
					}
					totalVotingPower = totalVotingPower.Add(votingPower)
				}
				return false
			})
			if err != nil {
				return false, err
			}

			votesToRemove = append(votesToRemove, key)
			return false, nil
		})
		if err != nil {
			return math.LegacyZeroDec(), math.ZeroInt(), nil, fmt.Errorf("error while iterating delegations: %w", err)
		}

		for _, key := range votesToRemove {
			if err := k.Votes.Remove(ctx, key); err != nil {
				return math.LegacyDec{}, math.ZeroInt(), nil, fmt.Errorf("error while removing vote (%d/%s): %w", key.K1(), key.K2(), err)
			}
		}

		for _, val := range validators {
			if len(val.Vote) == 0 {
				continue
			}
			sharesAfterDeductions := val.DelegatorShares.Sub(val.DelegatorDeductions)
			votingPower := sharesAfterDeductions.MulInt(val.BondedTokens).Quo(val.DelegatorShares)
			for _, option := range val.Vote {
				weight, _ := math.LegacyNewDecFromStr(option.Weight)
				results[option.Option] = results[option.Option].Add(votingPower.Mul(weight))
			}
			totalVotingPower = totalVotingPower.Add(votingPower)
		}

		return totalVotingPower, totalValPower, results, nil
	}
}
