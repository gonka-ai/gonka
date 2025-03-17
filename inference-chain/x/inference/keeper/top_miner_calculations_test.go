package keeper

import (
	"github.com/google/uuid"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
)

const (
	now = 449931600 // Bonus points for anyone who gets the reference
)

var defaultPayoutSettings = PayoutSettings{
	// Note: Need to decide if it's a calendar year or this.
	PayoutPeriod:       days(365),
	TotalRewards:       120000000,
	TopNumberOfMiners:  3,
	MaxPayoutsTotal:    12,
	MaxPayoutsPerMiner: 4,
	AllowedFailureRate: *types.DecimalFromFloat(0.01),
	MaximumTime:        days(365 * 4),
	FirstQualifiedTime: now,
}

func TestNeverQualified(t *testing.T) {
	factors := &TopMinerFactors{
		MinerAddress:      "miner1",
		Qualified:         false,
		TimeOfCalculation: now,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []*types.TopMiner{},
	}
	action := GetTopMinerAction(factors)
	require.IsType(t, DoNothing{}, action)
}

var startingFactors = &TopMinerFactors{
	MinerAddress:      "miner1",
	Qualified:         true,
	TimeOfCalculation: now,
	PayoutSettings:    defaultPayoutSettings,
	TopMiners:         []*types.TopMiner{},
}

func TestAddNewMiner(t *testing.T) {
	action := GetTopMinerAction(startingFactors)
	require.IsType(t, AddMiner{}, action)
	newMiner := action.(AddMiner).Miner
	require.Equal(t, startingFactors.MinerAddress, newMiner.Address)
	require.Equal(t, startingFactors.TimeOfCalculation, newMiner.LastUpdatedTime)
	require.Equal(t, startingFactors.TimeOfCalculation, newMiner.LastQualifiedStarted)
	require.Equal(t, startingFactors.TimeOfCalculation, newMiner.FirstQualifiedStarted)
	require.Equal(t, int32(0), newMiner.RewardsPaidCount)
	require.Equal(t, int32(0), newMiner.QualifiedPeriods)
	require.Empty(t, newMiner.RewardsPaid)
	require.Equal(t, int32(0), newMiner.MissedPeriods)
	require.Equal(t, int64(0), newMiner.QualifiedTime)
	require.Equal(t, int64(0), newMiner.MissedTime)
}

func TestUpdateMinerOnce(t *testing.T) {
	action := GetTopMinerAction(startingFactors)
	newMiner := action.(AddMiner).Miner
	updatedFactors := &TopMinerFactors{
		MinerAddress:      newMiner.Address,
		Qualified:         true,
		TimeOfCalculation: startingFactors.TimeOfCalculation + 1000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []*types.TopMiner{&newMiner},
	}
	action = GetTopMinerAction(updatedFactors)
	require.IsType(t, UpdateMiner{}, action)
	updatedMiner := action.(UpdateMiner).Miner
	require.Equal(t, newMiner.Address, updatedMiner.Address)
	require.Equal(t, updatedFactors.TimeOfCalculation, updatedMiner.LastUpdatedTime)
	require.Equal(t, newMiner.LastQualifiedStarted, updatedMiner.LastQualifiedStarted)
	require.Equal(t, newMiner.FirstQualifiedStarted, updatedMiner.FirstQualifiedStarted)
	require.Equal(t, newMiner.RewardsPaidCount, updatedMiner.RewardsPaidCount)
	require.Equal(t, newMiner.QualifiedPeriods+1, updatedMiner.QualifiedPeriods)
	require.Equal(t, newMiner.RewardsPaid, updatedMiner.RewardsPaid)
	require.Equal(t, newMiner.MissedPeriods, updatedMiner.MissedPeriods)
	require.Equal(t, newMiner.QualifiedTime+1000, updatedMiner.QualifiedTime)
	require.Equal(t, newMiner.MissedTime, updatedMiner.MissedTime)
}

func TestUpdatedMinerUnqualifiedOnce(t *testing.T) {
	action := GetTopMinerAction(startingFactors)
	newMiner := action.(AddMiner).Miner
	updatedFactors := &TopMinerFactors{
		MinerAddress:      newMiner.Address,
		Qualified:         true,
		TimeOfCalculation: startingFactors.TimeOfCalculation + 1000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []*types.TopMiner{&newMiner},
	}
	action = GetTopMinerAction(updatedFactors)
	updatedMiner := action.(UpdateMiner).Miner
	updatedFactors = &TopMinerFactors{
		MinerAddress:      updatedMiner.Address,
		Qualified:         false,
		TimeOfCalculation: updatedFactors.TimeOfCalculation + 1000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []*types.TopMiner{&updatedMiner},
	}
	action = GetTopMinerAction(updatedFactors)
	require.IsType(t, UpdateMiner{}, action)
	updatedMiner = action.(UpdateMiner).Miner
	require.Equal(t, newMiner.Address, updatedMiner.Address)
	require.Equal(t, updatedFactors.TimeOfCalculation, updatedMiner.LastUpdatedTime)
	require.Equal(t, newMiner.LastQualifiedStarted, updatedMiner.LastQualifiedStarted)
	require.Equal(t, newMiner.RewardsPaidCount, updatedMiner.RewardsPaidCount)
	require.Equal(t, newMiner.QualifiedPeriods+1, updatedMiner.QualifiedPeriods)
	require.Equal(t, newMiner.RewardsPaid, updatedMiner.RewardsPaid)
	require.Equal(t, newMiner.MissedPeriods+1, updatedMiner.MissedPeriods)
	require.Equal(t, newMiner.QualifiedTime+1000, updatedMiner.QualifiedTime)
	require.Equal(t, newMiner.MissedTime+1000, updatedMiner.MissedTime)
}

func TestMinerDisqualifiedForPeriod(t *testing.T) {
	action := GetTopMinerAction(startingFactors)
	newMiner := action.(AddMiner).Miner
	disqualificationThreshold := decimal.NewFromInt(defaultPayoutSettings.PayoutPeriod).Mul(defaultPayoutSettings.AllowedFailureRate.ToDecimal())
	// Simulate many periods
	newMiner.QualifiedPeriods = 100
	newMiner.QualifiedTime = 10_000
	newMiner.MissedPeriods = 10
	newMiner.MissedTime = disqualificationThreshold.IntPart() - 100
	disqualifyingFactors := &TopMinerFactors{
		MinerAddress:      newMiner.Address,
		Qualified:         false,
		TimeOfCalculation: startingFactors.TimeOfCalculation + 1000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []*types.TopMiner{&newMiner},
	}
	action = GetTopMinerAction(disqualifyingFactors)
	updatedMiner := action.(UpdateMiner).Miner
	require.Equal(t, newMiner.Address, updatedMiner.Address)
	require.Equal(t, int32(0), updatedMiner.QualifiedPeriods)
	require.Equal(t, int64(0), updatedMiner.QualifiedTime)
	require.Equal(t, int32(0), updatedMiner.MissedPeriods)
	require.Equal(t, int64(0), updatedMiner.MissedTime)
	require.Equal(t, disqualifyingFactors.TimeOfCalculation, updatedMiner.LastUpdatedTime)
	require.Equal(t, int64(0), updatedMiner.LastQualifiedStarted)
	require.Equal(t, int64(0), updatedMiner.FirstQualifiedStarted)
}

func TestMinerGetsPaid(t *testing.T) {
	action := GetTopMinerAction(startingFactors)
	newMiner := action.(AddMiner).Miner
	updatedFactors := &TopMinerFactors{
		MinerAddress:      newMiner.Address,
		Qualified:         true,
		TimeOfCalculation: startingFactors.TimeOfCalculation + defaultPayoutSettings.PayoutPeriod + 1,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []*types.TopMiner{&newMiner},
	}
	action = GetTopMinerAction(updatedFactors)
	require.IsType(t, UpdateAndPayMiner{}, action)
	updatedMiner := action.(UpdateAndPayMiner).Miner
	require.Equal(t, newMiner.Address, updatedMiner.Address)
	require.Equal(t, updatedFactors.TimeOfCalculation, updatedMiner.LastUpdatedTime)
	require.Equal(t, updatedFactors.TimeOfCalculation, updatedMiner.LastQualifiedStarted)
	require.Equal(t, newMiner.LastQualifiedStarted, updatedMiner.FirstQualifiedStarted)
	require.Equal(t, int32(1), updatedMiner.RewardsPaidCount)
	require.Equal(t, int32(0), updatedMiner.QualifiedPeriods)
	require.Equal(t, int32(0), updatedMiner.MissedPeriods)
	require.Equal(t, int64(0), updatedMiner.QualifiedTime)
	require.Equal(t, int64(0), updatedMiner.MissedTime)
	require.Equal(t, defaultPayoutSettings.TotalRewards/int64(defaultPayoutSettings.MaxPayoutsTotal), action.(UpdateAndPayMiner).Payout)
}

func Test4thMinerDoesNotGetPaid(t *testing.T) {
	miner1 := getTestMiner(days(370))
	miner2 := getTestMiner(days(369))
	miner3 := getTestMiner(days(368))
	miner4 := getTestMiner(days(365) - 1000)
	factors := &TopMinerFactors{
		MinerAddress:      miner4.Address,
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner1,
			miner2,
			miner3,
			miner4,
		},
	}
	minerShouldNotBePaid(t, miner4, factors)
}

func TestMinerGetsSecondReward(t *testing.T) {
	miner := getTestMiner(days(365*2) - 1000)
	factors := &TopMinerFactors{
		MinerAddress:      miner.Address,
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner,
		},
	}
	action := GetTopMinerAction(factors)
	require.IsType(t, UpdateAndPayMiner{}, action)
	require.Equal(t, defaultPayoutSettings.GetPayoutAmount(), action.(UpdateAndPayMiner).Payout)
	newMiner := action.(UpdateAndPayMiner).Miner
	require.Equal(t, int32(2), newMiner.RewardsPaidCount)
	require.Equal(t, int32(0), newMiner.QualifiedPeriods)
	require.Equal(t, int32(0), newMiner.MissedPeriods)
	require.Equal(t, int64(0), newMiner.QualifiedTime)
	require.Equal(t, int64(0), newMiner.MissedTime)
	require.Equal(t, factors.TimeOfCalculation, newMiner.LastUpdatedTime)
	require.Equal(t, factors.TimeOfCalculation, newMiner.LastQualifiedStarted)
	require.Equal(t, now-days(365*2)+1000, newMiner.FirstQualifiedStarted)
}

func TestMinerGetsDisqualifiedAfterReward(t *testing.T) {
	testMiner := getTestMiner(days(365*2) + 1000)
	require.Equal(t, int32(2), testMiner.RewardsPaidCount)
	miner := getDisqualifiedMiner(testMiner)
	require.Equal(t, int32(2), miner.RewardsPaidCount)
}

func TestMinerGetsNo5thReward(t *testing.T) {
	miner := getTestMiner(days(365*5) - 1000)
	factors := &TopMinerFactors{
		MinerAddress:      miner.Address,
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner,
		},
	}
	minerShouldNotBePaid(t, miner, factors)
}

func TestMinerGetsNo13thReward(t *testing.T) {
	miner1 := getTestMiner(days(365*4) + 1000)
	miner2 := getTestMiner(days(365*4) + 1000)
	miner3 := getTestMiner(days(365) + 1000)
	miner4 := getTestMiner(days(365*3) + 1000)
	miner5 := getTestMiner(days(365) - 1000)
	factors := &TopMinerFactors{
		MinerAddress:      miner5.Address,
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner1,
			miner2,
			miner3,
			miner4,
			miner5,
		},
	}
	minerShouldNotBePaid(t, miner5, factors)
}

func getDisqualifiedMiner(miner *types.TopMiner) *types.TopMiner {
	miner.MissedTime = defaultPayoutSettings.GetDisqualificationThreshold() - 100
	disqFactors := &TopMinerFactors{
		MinerAddress:      miner.Address,
		Qualified:         false,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner,
		},
	}
	action := GetTopMinerAction(disqFactors)
	topMiner := action.(UpdateMiner).Miner
	return &topMiner
}

func TestMinerGetsPaidAfterOthersDisqualified(t *testing.T) {
	miner1 := getTestMiner(days(365*4) + 1000)
	miner2 := getTestMiner(days(365*4) + 1000)
	miner3 := getDisqualifiedMiner(getTestMiner(days(365) + 1000))

	miner4 := getTestMiner(days(365) - 1000)
	factors := &TopMinerFactors{
		MinerAddress:      miner4.Address,
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner1,
			miner2,
			miner3,
			miner4,
		},
	}
	action := GetTopMinerAction(factors)
	require.IsType(t, UpdateAndPayMiner{}, action)
	require.Equal(t, defaultPayoutSettings.GetPayoutAmount(), action.(UpdateAndPayMiner).Payout)
	paidMiner := action.(UpdateAndPayMiner).Miner
	require.Equal(t, int32(1), paidMiner.RewardsPaidCount)
	require.Equal(t, int32(0), paidMiner.QualifiedPeriods)
	require.Equal(t, int32(0), paidMiner.MissedPeriods)
	require.Equal(t, int64(0), paidMiner.QualifiedTime)
	require.Equal(t, int64(0), paidMiner.MissedTime)
	require.Equal(t, factors.TimeOfCalculation, paidMiner.LastUpdatedTime)
	require.Equal(t, factors.TimeOfCalculation, paidMiner.LastQualifiedStarted)
	require.Equal(t, now-days(365)+1000, paidMiner.FirstQualifiedStarted)
}

func TestMinerDoesNotGetPaidAfterOthersMaxedOut(t *testing.T) {
	miner1 := getTestMiner(days(365*4) + 1000)
	miner2 := getTestMiner(days(365*4) + 1000)
	miner3 := getTestMiner(days(365*4) + 1000)
	// now we have to disqualify one of the miners so they no longer count as "top 3". Should STILL not pay out!
	miner3.MissedTime = defaultPayoutSettings.GetDisqualificationThreshold() - 100
	disqFactors := &TopMinerFactors{
		MinerAddress:      miner3.Address,
		Qualified:         false,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner1,
			miner2,
			miner3,
		},
	}
	action := GetTopMinerAction(disqFactors)
	require.IsType(t, UpdateMiner{}, action)
	disqMiner := action.(UpdateMiner).Miner
	miner4 := getTestMiner(days(365) - 1000)
	factors := &TopMinerFactors{
		MinerAddress:      miner4.Address,
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner1,
			miner2,
			&disqMiner,
			miner4,
		},
	}
	action = GetTopMinerAction(factors)
	require.IsType(t, UpdateMiner{}, action)
	paidMiner := action.(UpdateMiner).Miner
	require.Equal(t, int32(0), paidMiner.RewardsPaidCount)
	require.Equal(t, int32(365), paidMiner.QualifiedPeriods)
	require.Equal(t, int32(0), paidMiner.MissedPeriods)
	require.Equal(t, days(365)+9000, paidMiner.QualifiedTime)
	require.Equal(t, int64(0), paidMiner.MissedTime)
	require.Equal(t, factors.TimeOfCalculation, paidMiner.LastUpdatedTime)
	require.Equal(t, factors.TimeOfCalculation-days(365)-9000, paidMiner.LastQualifiedStarted)
}

func TestResolveTiesByPower(t *testing.T) {
	miner1 := getTestMiner(days(365*1) - 1000)
	miner2 := getTestMiner(days(365*1) - 1000)
	miner3 := getTestMiner(days(365*1) - 1000)
	miner4 := getTestMiner(days(365*1) - 1000)
	miner1.InitialPower = 100
	miner2.InitialPower = 205
	miner3.InitialPower = 200
	miner4.InitialPower = 101
	factors := &TopMinerFactors{
		MinerAddress:      miner4.Address,
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner1,
			miner2,
			miner3,
			miner4,
		},
	}
	minerShouldNotBePaid(t, miner1, factors)
	minerShouldBePaid(t, miner2, factors)
	minerShouldBePaid(t, miner3, factors)
	minerShouldBePaid(t, miner4, factors)
}

func minerShouldBePaid(t *testing.T, miner *types.TopMiner, factors *TopMinerFactors) {
	factors.MinerAddress = miner.Address
	action := GetTopMinerAction(factors)
	require.IsType(t, UpdateAndPayMiner{}, action)
}

func minerShouldNotBePaid(t *testing.T, miner *types.TopMiner, factors *TopMinerFactors) {
	factors.MinerAddress = miner.Address
	action := GetTopMinerAction(factors)
	require.IsType(t, UpdateMiner{}, action)
}

func TestResolveTiesByOrder(t *testing.T) {
	miner1 := getTestMiner(days(365*1) - 1000)
	miner2 := getTestMiner(days(365*1) - 1000)
	miner3 := getTestMiner(days(365*1) - 1000)
	miner4 := getTestMiner(days(365*1) - 1000)
	miner1.InitialPower = 101
	miner1.InitialOrder = 0
	miner2.InitialPower = 100
	miner2.InitialOrder = 3
	miner3.InitialPower = 100
	miner3.InitialOrder = 2
	miner4.InitialPower = 100
	miner4.InitialOrder = 1
	factors := &TopMinerFactors{
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner1,
			miner2,
			miner3,
			miner4,
		},
	}
	minerShouldBePaid(t, miner1, factors)
	minerShouldBePaid(t, miner3, factors)
	minerShouldBePaid(t, miner4, factors)
	minerShouldNotBePaid(t, miner2, factors)
}

func TestMinerShouldGetPaidOnceAfterCutoff(t *testing.T) {
	miner1 := getTestMiner(days(365*4) + 1000)
	miner2 := getTestMiner(days(365*1) - 1000)
	defaultPayoutSettings.FirstQualifiedTime = miner1.FirstQualifiedStarted
	factors := &TopMinerFactors{
		MinerAddress:      miner2.Address,
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner1,
			miner2,
		},
	}
	action := GetTopMinerAction(factors)
	require.IsType(t, UpdateAndPayMiner{}, action)
	paidMiner := action.(UpdateAndPayMiner).Miner
	secondFactors := &TopMinerFactors{
		MinerAddress:      paidMiner.Address,
		Qualified:         true,
		TimeOfCalculation: now + 20000 + days(365),
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []*types.TopMiner{
			miner1,
			&paidMiner,
		},
	}
	action = GetTopMinerAction(secondFactors)
	require.IsType(t, UpdateMiner{}, action)
}

func TestSortedMiners(t *testing.T) {
	miner1 := getTestMiner(days(365*4) + 1000)
	miner2 := getTestMiner(days(365*1) - 1000)
	miner3 := getTestMiner(days(365*1) - 1000)
	miner4 := getTestMiner(days(365*1) - 1000)
	miner5 := getTestMiner(days(365*1) - 1000)
	miner6 := getTestMiner(days(365*5) - 1000)
	miner3.InitialPower = 100
	miner2.InitialPower = 200
	miner4.InitialPower = 50
	miner5.InitialPower = 50
	miner4.InitialOrder = 1
	miner5.InitialOrder = 2
	minerSet := &TopMinerSet{
		TopMiners: []*types.TopMiner{
			miner5,
			miner1,
			miner3,
			miner4,
			miner2,
			miner6,
		},
		Participants: []*Miner{
			{
				miner3.Address,
				true, nil,
			},
			{
				miner1.Address,
				false,
				nil,
			},
			{
				miner2.Address,
				true,
				nil,
			},
			{
				miner5.Address,
				true,
				nil,
			},
			{
				miner4.Address,
				true,
				nil,
			},
			{
				"NewMinerAddress",
				true,
				nil,
			},
		},
	}
	sorted := getSortedMiners(minerSet)
	require.Equal(t, 6, len(sorted))
	require.Equal(t, miner1.Address, sorted[0].Address)
	require.Equal(t, miner2.Address, sorted[1].Address)
	require.Equal(t, miner3.Address, sorted[2].Address)
	require.Equal(t, miner4.Address, sorted[3].Address)
	require.Equal(t, miner5.Address, sorted[4].Address)
	require.Equal(t, "NewMinerAddress", sorted[5].Address)
}

// GetTopMinerActions tests!!
func getTestSet() *TopMinerSet {
	miner1 := getTestMiner(days(365*4) + 1000)
	miner2 := getTestMiner(days(365*1) - 1000)
	miner3 := getTestMiner(days(365*1) - 1000)
	miner4 := getTestMiner(days(365*1) - 1000)
	miner1.InitialPower = 1000
	miner2.InitialPower = 200
	miner3.InitialPower = 100
	miner3.InitialOrder = 1
	miner4.InitialPower = 100
	miner4.InitialOrder = 2
	set := &TopMinerSet{
		TopMiners: []*types.TopMiner{miner1, miner2, miner3, miner4},
		Participants: []*Miner{
			{
				"NewMinerAddress",
				true,
				nil,
			},
			{
				miner2.Address,
				true, nil,
			},
			{
				miner1.Address,
				true, nil,
			},
			{
				miner4.Address,
				true, nil,
			},
			{
				"NonQualAddress",
				false,
				nil,
			},
			{
				miner3.Address,
				true, nil,
			},
		},
		PayoutSettings:    defaultPayoutSettings,
		TimeOfCalculation: now + 10000,
	}
	return set

}

// TODO: Just standard, make sure all values match
func TestGetTopMinerActions(t *testing.T) {
	set := getTestSet()
	actions := GetTopMinerActions(set)
	require.Equal(t, 6, len(actions))
	requireAction(t, actions[0], set.TopMiners[0].Address, UpdateMiner{})
	requireAction(t, actions[1], set.TopMiners[1].Address, UpdateAndPayMiner{})
	requireAction(t, actions[2], set.TopMiners[2].Address, UpdateAndPayMiner{})
	requireAction(t, actions[3], set.TopMiners[3].Address, UpdateMiner{})
	requireAction(t, actions[4], "NewMinerAddress", AddMiner{})
	requireAction(t, actions[5], "NonQualAddress", DoNothing{})
}

func TestGetTopMinerActionsWithOneReward(t *testing.T) {
	set := getTestSet()
	set.TopMiners[0].RewardsPaidCount = defaultPayoutSettings.MaxPayoutsTotal - 1
	actions := GetTopMinerActions(set)
	require.Equal(t, 6, len(actions))
	requireAction(t, actions[0], set.TopMiners[0].Address, UpdateMiner{})
	requireAction(t, actions[1], set.TopMiners[1].Address, UpdateAndPayMiner{})
	// No more rewards to give!
	requireAction(t, actions[2], set.TopMiners[2].Address, UpdateMiner{})
	requireAction(t, actions[3], set.TopMiners[3].Address, UpdateMiner{})
	requireAction(t, actions[4], "NewMinerAddress", AddMiner{})
	requireAction(t, actions[5], "NonQualAddress", DoNothing{})
}

func TestGetTopMinerOneLeftWithDisqualified(t *testing.T) {
	set := getTestSet()
	set.TopMiners[0].RewardsPaidCount = defaultPayoutSettings.MaxPayoutsTotal - 1
	set.Participants[1].Qualified = false
	set.TopMiners[1].MissedTime = defaultPayoutSettings.GetDisqualificationThreshold() - 100
	actions := GetTopMinerActions(set)
	require.Equal(t, 6, len(actions))
	requireAction(t, actions[0], set.TopMiners[0].Address, UpdateMiner{})
	requireAction(t, actions[1], set.TopMiners[1].Address, UpdateMiner{})
	// No more rewards to give!
	requireAction(t, actions[2], set.TopMiners[2].Address, UpdateAndPayMiner{})
	requireAction(t, actions[3], set.TopMiners[3].Address, UpdateMiner{})
	requireAction(t, actions[4], "NewMinerAddress", AddMiner{})
	requireAction(t, actions[5], "NonQualAddress", DoNothing{})
}

func Test3ToWithDisqualified(t *testing.T) {
	set := getTestSet()
	oldAddress := set.TopMiners[0].Address
	set.TopMiners[0] = getTestMiner(days(365*1) - 1000)
	set.TopMiners[0].Address = oldAddress
	set.TopMiners[0].InitialPower = 1000
	set.Participants[1].Qualified = false
	set.TopMiners[1].MissedTime = defaultPayoutSettings.GetDisqualificationThreshold() - 100
	actions := GetTopMinerActions(set)
	require.Equal(t, 6, len(actions))
	requireAction(t, actions[0], set.TopMiners[0].Address, UpdateAndPayMiner{})
	requireAction(t, actions[1], set.TopMiners[1].Address, UpdateMiner{})
	requireAction(t, actions[2], set.TopMiners[2].Address, UpdateAndPayMiner{})
	requireAction(t, actions[3], set.TopMiners[3].Address, UpdateAndPayMiner{})
	requireAction(t, actions[4], "NewMinerAddress", AddMiner{})
	requireAction(t, actions[5], "NonQualAddress", DoNothing{})
}

func requireAction(t *testing.T, action TopMinerAction, address string, miner TopMinerAction) {
	require.Equal(t, address, action.MinerAddress())
	require.Equal(t, miner.TopMinerActionName(), action.TopMinerActionName())
}

func TestGetTestMiner(t *testing.T) {
	miner := getTestMiner(days(340))
	require.Equal(t, days(340), miner.QualifiedTime)
	require.Equal(t, int32(340), miner.QualifiedPeriods)
	require.Equal(t, int32(0), miner.RewardsPaidCount)

	paidMiner := getTestMiner(days(370))
	require.Equal(t, days(370-365), paidMiner.QualifiedTime)
	require.Equal(t, int32(370-365), paidMiner.QualifiedPeriods)
	require.Equal(t, int32(1), paidMiner.RewardsPaidCount)
	require.Equal(t, defaultPayoutSettings.TotalRewards/int64(defaultPayoutSettings.MaxPayoutsTotal), paidMiner.RewardsPaid[0])
	require.Equal(t, int64(0), paidMiner.MissedTime)
	require.Equal(t, int32(0), paidMiner.MissedPeriods)
	require.Equal(t, now-days(370), paidMiner.FirstQualifiedStarted)
}

func getTestMiner(timeSinceJoined int64) *types.TopMiner {
	var timesPaid = timeSinceJoined / defaultPayoutSettings.PayoutPeriod
	var timeSinceLastPaid = timeSinceJoined % defaultPayoutSettings.PayoutPeriod
	testMiner := &types.TopMiner{
		Address:               uuid.New().String(),
		LastQualifiedStarted:  now - timeSinceLastPaid,
		FirstQualifiedStarted: now - timeSinceJoined,
		LastUpdatedTime:       now,
		RewardsPaidCount:      int32(timesPaid),
		QualifiedPeriods:      int32(timeSinceLastPaid / days(1)),
		QualifiedTime:         timeSinceLastPaid,
	}
	for i := int64(0); i < timesPaid; i++ {
		testMiner.RewardsPaid = append(testMiner.RewardsPaid, defaultPayoutSettings.TotalRewards/int64(defaultPayoutSettings.MaxPayoutsTotal))
	}
	return testMiner

}

func days(days int64) int64 {
	return days * 60 * 60 * 24
}
