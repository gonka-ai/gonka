package keeper

import (
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"sort"
)

type TopMinerFactors struct {
	TopMiners         []*types.TopMiner
	MinerAddress      string
	Qualified         bool
	TimeOfCalculation int64
	PayoutSettings    PayoutSettings
}

type TopMinerSet struct {
	TopMiners         []*types.TopMiner
	Participants      []*Miner
	TimeOfCalculation int64
	PayoutSettings    PayoutSettings
}

type PayoutSettings struct {
	PayoutPeriod       int64
	TotalRewards       int64
	TopNumberOfMiners  int32
	MaxPayoutsTotal    int32
	MaxPayoutsPerMiner int32
	AllowedFailureRate types.Decimal
	MaximumTime        int64
	FirstQualifiedTime int64
}

func (p PayoutSettings) GetPayoutAmount() int64 {
	return p.TotalRewards / int64(p.MaxPayoutsTotal)
}

func (p PayoutSettings) GetDisqualificationThreshold() int64 {
	return decimal.NewFromInt(p.PayoutPeriod).Mul(p.AllowedFailureRate.ToDecimal()).IntPart()
}

type TopMinerAction interface {
	TopMinerActionName() string
	MinerAddress() string
}

type AddMiner struct {
	Miner types.TopMiner
}

func (a AddMiner) MinerAddress() string {
	return a.Miner.Address
}

func (a AddMiner) TopMinerActionName() string {
	return "AddMiner"
}

type UpdateMiner struct {
	Miner types.TopMiner
}

func (u UpdateMiner) TopMinerActionName() string {
	return "UpdateMiner"
}

func (u UpdateMiner) MinerAddress() string {
	return u.Miner.Address
}

type DoNothing struct {
	minerAddress string
}

func (d DoNothing) MinerAddress() string {
	return d.minerAddress
}

func (d DoNothing) TopMinerActionName() string {
	return "DoNothing"
}

type UpdateAndPayMiner struct {
	Miner  types.TopMiner
	Payout int64
}

func (u UpdateAndPayMiner) TopMinerActionName() string {
	return "UpdateAndPayMiner"
}

func (u UpdateAndPayMiner) MinerAddress() string {
	return u.Miner.Address
}

type Miner struct {
	Address        string
	Qualified      bool
	TopMinerRecord *types.TopMiner
}

func getSortedMiners(set *TopMinerSet) []Miner {
	existingMiners := mapMiners(set)
	sortedMiners := existingMiners
	sort.SliceStable(sortedMiners, func(i, j int) bool {
		if sortedMiners[i].TopMinerRecord == nil && sortedMiners[j].TopMinerRecord == nil {
			return false
		}
		if sortedMiners[i].TopMinerRecord == nil {
			return false
		}
		if sortedMiners[j].TopMinerRecord == nil {
			return true
		}
		return firstMinerIsGreater(sortedMiners[i].TopMinerRecord, sortedMiners[j].TopMinerRecord)
	})

	return sortedMiners
}

func mapMiners(set *TopMinerSet) []Miner {
	mapped := []Miner{}
	for _, participant := range set.Participants {
		existing := findMiner(participant.Address, set.TopMiners)
		mapped = append(mapped, Miner{
			Address:        participant.Address,
			Qualified:      participant.Qualified,
			TopMinerRecord: existing,
		})
	}
	return mapped
}

func GetTopMinerActions(set *TopMinerSet) []TopMinerAction {
	sortedMiners := getSortedMiners(set)
	var actions []TopMinerAction
	topMiners := set.TopMiners
	// TODO: We are relying on the mutability of the TopMiner records, which
	// feels very NOT functional. Each Miner is adjusted as they go through
	// the factors, which means the NEXT calculation is accurate.
	for _, miner := range sortedMiners {
		action := GetTopMinerAction(&TopMinerFactors{
			TopMiners:         topMiners,
			MinerAddress:      miner.Address,
			Qualified:         miner.Qualified,
			TimeOfCalculation: set.TimeOfCalculation,
			PayoutSettings:    set.PayoutSettings,
		})
		actions = append(actions, action)
		// To propagate the calculations, we must apply each action as we go
		topMiners = applyAction(action, topMiners)
	}
	return actions
}

func applyAction(action TopMinerAction, miners []*types.TopMiner) []*types.TopMiner {
	switch typedAction := action.(type) {
	case UpdateMiner:
		return replace(miners, typedAction.Miner)
	case UpdateAndPayMiner:
		return replace(miners, typedAction.Miner)
	case AddMiner:
		return append(miners, &typedAction.Miner)
	default:
		return miners
	}
}

func replace(miners []*types.TopMiner, miner types.TopMiner) []*types.TopMiner {
	for i, m := range miners {
		if m.Address == miner.Address {
			miners[i] = &miner
			return miners
		}
	}
	return miners
}

func GetTopMinerAction(factors *TopMinerFactors) TopMinerAction {
	existingMiner := findMiner(factors.MinerAddress, factors.TopMiners)
	if existingMiner == nil {
		if !factors.Qualified {
			return DoNothing{
				minerAddress: factors.MinerAddress,
			}
		}
		return addNewMiner(factors)
	}
	timeSinceLastUpdate := factors.TimeOfCalculation - existingMiner.LastUpdatedTime
	existingMiner.LastUpdatedTime = factors.TimeOfCalculation
	if factors.Qualified {
		if minerShouldGetPayout(factors, existingMiner) {
			return payMiner(factors, existingMiner)
		}
		return extendQualification(timeSinceLastUpdate, existingMiner)
	} else {
		if minerWillBeDisqualified(timeSinceLastUpdate, factors, existingMiner) {
			return disqualifyMiner(existingMiner)
		}
		return addDisqualifyingPeriod(timeSinceLastUpdate, existingMiner)
	}
}

func minerWillBeDisqualified(timeSinceLastUpdate int64, factors *TopMinerFactors, existingMiner *types.TopMiner) bool {
	return existingMiner.MissedTime+timeSinceLastUpdate > factors.PayoutSettings.GetDisqualificationThreshold()
}

func addDisqualifyingPeriod(timeSinceLastUpdate int64, existingMiner *types.TopMiner) TopMinerAction {
	existingMiner.MissedPeriods++
	existingMiner.MissedTime += timeSinceLastUpdate
	return UpdateMiner{Miner: *existingMiner}
}

func minerShouldGetPayout(factors *TopMinerFactors, existingMiner *types.TopMiner) bool {
	return factors.TimeOfCalculation-existingMiner.LastQualifiedStarted > factors.PayoutSettings.PayoutPeriod &&
		existingMiner.RewardsPaidCount < factors.PayoutSettings.MaxPayoutsPerMiner &&
		minerIsInTopN(factors, existingMiner) &&
		rewardsStillAvailable(factors, existingMiner)
}

func minerIsInTopN(factors *TopMinerFactors, existingMiner *types.TopMiner) bool {
	topMiners := factors.TopMiners
	if int32(len(topMiners)) < factors.PayoutSettings.TopNumberOfMiners {
		return true
	}
	minersRemaining := factors.PayoutSettings.TopNumberOfMiners
	for _, miner := range topMiners {
		if miner.FirstQualifiedStarted == 0 {
			continue
		}
		if firstMinerIsGreater(miner, existingMiner) {
			minersRemaining--
		}
		if minersRemaining <= 0 {
			return false
		}
	}
	return true
}

func firstMinerIsGreater(a, b *types.TopMiner) bool {
	if a.Address == b.Address {
		return false
	}
	if a.FirstQualifiedStarted != b.FirstQualifiedStarted {
		return a.FirstQualifiedStarted < b.FirstQualifiedStarted
	}
	if a.InitialPower != b.InitialPower {
		return a.InitialPower > b.InitialPower
	}
	return a.InitialOrder < b.InitialOrder
}

func rewardsStillAvailable(factors *TopMinerFactors, miner *types.TopMiner) bool {
	cutoff := factors.PayoutSettings.FirstQualifiedTime + factors.PayoutSettings.MaximumTime
	if miner.LastQualifiedStarted > cutoff {
		return false
	}
	var allRewardsPaid = int32(0)
	for _, miner := range factors.TopMiners {
		allRewardsPaid += miner.RewardsPaidCount
	}
	return allRewardsPaid < factors.PayoutSettings.MaxPayoutsTotal
}

func disqualifyMiner(existingMiner *types.TopMiner) TopMinerAction {
	existingMiner.LastQualifiedStarted = 0
	existingMiner.FirstQualifiedStarted = 0
	existingMiner.QualifiedPeriods = 0
	existingMiner.MissedPeriods = 0
	existingMiner.QualifiedTime = 0
	existingMiner.MissedTime = 0
	return UpdateMiner{Miner: *existingMiner}
}

func extendQualification(timeSinceLastUpdate int64, existingMiner *types.TopMiner) TopMinerAction {
	existingMiner.QualifiedPeriods++
	existingMiner.QualifiedTime += timeSinceLastUpdate
	return UpdateMiner{Miner: *existingMiner}
}

func payMiner(factors *TopMinerFactors, existingMiner *types.TopMiner) TopMinerAction {
	// TODO: Not accounting for "top 3" here, yet
	existingMiner.RewardsPaidCount++
	existingMiner.LastQualifiedStarted = factors.TimeOfCalculation
	existingMiner.QualifiedTime = 0
	existingMiner.QualifiedPeriods = 0
	existingMiner.MissedPeriods = 0
	existingMiner.MissedTime = 0
	payout := factors.PayoutSettings.TotalRewards / int64(factors.PayoutSettings.MaxPayoutsTotal)
	return UpdateAndPayMiner{Miner: *existingMiner, Payout: payout}
}

func addNewMiner(factors *TopMinerFactors) TopMinerAction {
	newMiner := types.TopMiner{
		Address:               factors.MinerAddress,
		LastUpdatedTime:       factors.TimeOfCalculation,
		LastQualifiedStarted:  factors.TimeOfCalculation,
		FirstQualifiedStarted: factors.TimeOfCalculation,
		RewardsPaidCount:      0,
		QualifiedPeriods:      0,
		RewardsPaid:           []int64{},
		MissedPeriods:         0,
		QualifiedTime:         0,
		MissedTime:            0,
	}
	return AddMiner{Miner: newMiner}
}

// TODO: consider perf here? Default is this should be a TINY list, so no big hit
func findMiner(address string, miners []*types.TopMiner) *types.TopMiner {
	for _, miner := range miners {
		if miner.Address == address {
			// We don't want to modify the underlying data.o
			copyOfMiner := *miner
			return &copyOfMiner
		}
	}
	return nil

}
