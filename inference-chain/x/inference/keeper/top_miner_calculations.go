package keeper

import (
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

type TopMinerFactors struct {
	TopMiners         []types.TopMiner
	MinerAddress      string
	Qualified         bool
	TimeOfCalculation int64
	PayoutSettings    PayoutSettings
}

type PayoutSettings struct {
	PayoutPeriod       int64
	TotalRewards       int64
	TopNumberOfMiners  int32
	MaxPayoutsTotal    int32
	MaxPayoutsPerMiner int32
	AllowedFailureRate float32
	MaximumTime        int64
	FirstQualifiedTime int64
}

func (p PayoutSettings) GetPayoutAmount() int64 {
	return p.TotalRewards / int64(p.MaxPayoutsTotal)
}

func (p PayoutSettings) GetDisqualificationThreshold() int64 {
	return decimal.NewFromInt(p.PayoutPeriod).Mul(decimal.NewFromFloat32(p.AllowedFailureRate)).IntPart()
}

type TopMinerAction interface {
	TopMinerActionName() string
}

type AddMiner struct {
	miner types.TopMiner
}

func (a AddMiner) TopMinerActionName() string {
	return "AddMiner"
}

type UpdateMiner struct {
	miner types.TopMiner
}

func (u UpdateMiner) TopMinerActionName() string {
	return "UpdateMiner"
}

type DoNothing struct{}

func (d DoNothing) TopMinerActionName() string {
	return "DoNothing"
}

type UpdateAndPayMiner struct {
	miner  types.TopMiner
	payout int64
}

func (u UpdateAndPayMiner) TopMinerActionName() string {
	return "UpdateAndPayMiner"
}

func GetTopMinerAction(factors *TopMinerFactors) (TopMinerAction, error) {
	existingMiner := findMiner(factors.MinerAddress, factors.TopMiners)
	if existingMiner == nil {
		if !factors.Qualified {
			return DoNothing{}, nil
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

func addDisqualifyingPeriod(timeSinceLastUpdate int64, existingMiner *types.TopMiner) (TopMinerAction, error) {
	existingMiner.MissedPeriods++
	existingMiner.MissedTime += timeSinceLastUpdate
	return UpdateMiner{miner: *existingMiner}, nil
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
		if firstMinerIsGreater(&miner, existingMiner) {
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

func disqualifyMiner(existingMiner *types.TopMiner) (TopMinerAction, error) {
	existingMiner.LastQualifiedStarted = 0
	existingMiner.FirstQualifiedStarted = 0
	existingMiner.QualifiedPeriods = 0
	existingMiner.MissedPeriods = 0
	existingMiner.QualifiedTime = 0
	existingMiner.MissedTime = 0
	return UpdateMiner{miner: *existingMiner}, nil
}

func extendQualification(timeSinceLastUpdate int64, existingMiner *types.TopMiner) (TopMinerAction, error) {
	existingMiner.QualifiedPeriods++
	existingMiner.QualifiedTime += timeSinceLastUpdate
	return UpdateMiner{miner: *existingMiner}, nil
}

func payMiner(factors *TopMinerFactors, existingMiner *types.TopMiner) (TopMinerAction, error) {
	// TODO: Not accounting for "top 3" here, yet
	existingMiner.RewardsPaidCount++
	existingMiner.LastQualifiedStarted = factors.TimeOfCalculation
	existingMiner.QualifiedTime = 0
	existingMiner.QualifiedPeriods = 0
	existingMiner.MissedPeriods = 0
	existingMiner.MissedTime = 0
	payout := factors.PayoutSettings.TotalRewards / int64(factors.PayoutSettings.MaxPayoutsTotal)
	return UpdateAndPayMiner{miner: *existingMiner, payout: payout}, nil
}

func addNewMiner(factors *TopMinerFactors) (TopMinerAction, error) {
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
	return AddMiner{miner: newMiner}, nil
}

// TODO: consider perf here? Default is this should be a TINY list, so no big hit
func findMiner(address string, miners []types.TopMiner) *types.TopMiner {
	for _, miner := range miners {
		if miner.Address == address {
			return &miner
		}
	}
	return nil

}
