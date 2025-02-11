package keeper

import (
	"github.com/google/uuid"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

const (
	now = 449931600 // Bonus points for anyone who gets the reference
)

var defaultPayoutSettings = PayoutSettings{
	// Note: Need to decide if it's a calendar year or this.
	PayoutPeriod:       int64(time.Hour.Seconds() * 24 * 365),
	TotalRewards:       120000000,
	TopNumberOfMiners:  3,
	MaxPayoutsTotal:    12,
	MaxPayoutsPerMiner: 4,
	AllowedFailureRate: 0.01,
}

func TestNeverQualified(t *testing.T) {
	factors := &TopMinerFactors{
		MinerAddress:      "miner1",
		Qualified:         false,
		TimeOfCalculation: now,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []types.TopMiner{},
	}
	action, err := GetTopMinerAction(factors)
	require.NoError(t, err)
	require.IsType(t, DoNothing{}, action)
}

var startingFactors = &TopMinerFactors{
	MinerAddress:      "miner1",
	Qualified:         true,
	TimeOfCalculation: now,
	PayoutSettings:    defaultPayoutSettings,
	TopMiners:         []types.TopMiner{},
}

func TestAddNewMiner(t *testing.T) {
	action, err := GetTopMinerAction(startingFactors)
	require.NoError(t, err)
	require.IsType(t, AddMiner{}, action)
	newMiner := action.(AddMiner).miner
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
	action, _ := GetTopMinerAction(startingFactors)
	newMiner := action.(AddMiner).miner
	updatedFactors := &TopMinerFactors{
		MinerAddress:      newMiner.Address,
		Qualified:         true,
		TimeOfCalculation: startingFactors.TimeOfCalculation + 1000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []types.TopMiner{newMiner},
	}
	action, err := GetTopMinerAction(updatedFactors)
	require.NoError(t, err)
	require.IsType(t, UpdateMiner{}, action)
	updatedMiner := action.(UpdateMiner).miner
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
	action, _ := GetTopMinerAction(startingFactors)
	newMiner := action.(AddMiner).miner
	updatedFactors := &TopMinerFactors{
		MinerAddress:      newMiner.Address,
		Qualified:         true,
		TimeOfCalculation: startingFactors.TimeOfCalculation + 1000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []types.TopMiner{newMiner},
	}
	action, _ = GetTopMinerAction(updatedFactors)
	updatedMiner := action.(UpdateMiner).miner
	updatedFactors = &TopMinerFactors{
		MinerAddress:      updatedMiner.Address,
		Qualified:         false,
		TimeOfCalculation: updatedFactors.TimeOfCalculation + 1000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []types.TopMiner{updatedMiner},
	}
	action, err := GetTopMinerAction(updatedFactors)
	require.NoError(t, err)
	require.IsType(t, UpdateMiner{}, action)
	updatedMiner = action.(UpdateMiner).miner
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
	action, _ := GetTopMinerAction(startingFactors)
	newMiner := action.(AddMiner).miner
	disqualificationThreshold := decimal.NewFromInt(defaultPayoutSettings.PayoutPeriod).Mul(decimal.NewFromFloat32(defaultPayoutSettings.AllowedFailureRate))
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
		TopMiners:         []types.TopMiner{newMiner},
	}
	action, _ = GetTopMinerAction(disqualifyingFactors)
	updatedMiner := action.(UpdateMiner).miner
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
	action, _ := GetTopMinerAction(startingFactors)
	newMiner := action.(AddMiner).miner
	updatedFactors := &TopMinerFactors{
		MinerAddress:      newMiner.Address,
		Qualified:         true,
		TimeOfCalculation: startingFactors.TimeOfCalculation + defaultPayoutSettings.PayoutPeriod + 1,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners:         []types.TopMiner{newMiner},
	}
	action, _ = GetTopMinerAction(updatedFactors)
	require.IsType(t, UpdateAndPayMiner{}, action)
	updatedMiner := action.(UpdateAndPayMiner).miner
	require.Equal(t, newMiner.Address, updatedMiner.Address)
	require.Equal(t, updatedFactors.TimeOfCalculation, updatedMiner.LastUpdatedTime)
	require.Equal(t, updatedFactors.TimeOfCalculation, updatedMiner.LastQualifiedStarted)
	require.Equal(t, newMiner.LastQualifiedStarted, updatedMiner.FirstQualifiedStarted)
	require.Equal(t, int32(1), updatedMiner.RewardsPaidCount)
	require.Equal(t, int32(0), updatedMiner.QualifiedPeriods)
	require.Equal(t, int32(0), updatedMiner.MissedPeriods)
	require.Equal(t, int64(0), updatedMiner.QualifiedTime)
	require.Equal(t, int64(0), updatedMiner.MissedTime)
	require.Equal(t, defaultPayoutSettings.TotalRewards/int64(defaultPayoutSettings.MaxPayoutsTotal), action.(UpdateAndPayMiner).payout)
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
		TopMiners: []types.TopMiner{
			*miner1,
			*miner2,
			*miner3,
			*miner4,
		},
	}
	action, err := GetTopMinerAction(factors)
	require.NoError(t, err)
	require.IsType(t, UpdateMiner{}, action)
}

func TestMinerGetsSecondReward(t *testing.T) {
	miner := getTestMiner(days(365*2) - 1000)
	factors := &TopMinerFactors{
		MinerAddress:      miner.Address,
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []types.TopMiner{
			*miner,
		},
	}
	action, err := GetTopMinerAction(factors)
	require.NoError(t, err)
	require.IsType(t, UpdateAndPayMiner{}, action)
	require.Equal(t, defaultPayoutSettings.GetPayoutAmount(), action.(UpdateAndPayMiner).payout)
	newMiner := action.(UpdateAndPayMiner).miner
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
		TopMiners: []types.TopMiner{
			*miner,
		},
	}
	action, err := GetTopMinerAction(factors)
	require.NoError(t, err)
	require.IsType(t, UpdateMiner{}, action)
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
		TopMiners: []types.TopMiner{
			*miner1,
			*miner2,
			*miner3,
			*miner4,
			*miner5,
		},
	}
	action, err := GetTopMinerAction(factors)
	require.NoError(t, err)
	require.IsType(t, UpdateMiner{}, action)
}

func getDisqualifiedMiner(miner *types.TopMiner) *types.TopMiner {
	miner.MissedTime = defaultPayoutSettings.GetDisqualificationThreshold() - 100
	disqFactors := &TopMinerFactors{
		MinerAddress:      miner.Address,
		Qualified:         false,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []types.TopMiner{
			*miner,
		},
	}
	action, _ := GetTopMinerAction(disqFactors)
	topMiner := action.(UpdateMiner).miner
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
		TopMiners: []types.TopMiner{
			*miner1,
			*miner2,
			*miner3,
			*miner4,
		},
	}
	action, err := GetTopMinerAction(factors)
	require.NoError(t, err)
	require.IsType(t, UpdateAndPayMiner{}, action)
	require.Equal(t, defaultPayoutSettings.GetPayoutAmount(), action.(UpdateAndPayMiner).payout)
	paidMiner := action.(UpdateAndPayMiner).miner
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
		TopMiners: []types.TopMiner{
			*miner1,
			*miner2,
			*miner3,
		},
	}
	action, err := GetTopMinerAction(disqFactors)
	require.NoError(t, err)
	require.IsType(t, UpdateMiner{}, action)
	disqMiner := action.(UpdateMiner).miner
	miner4 := getTestMiner(days(365) - 1000)
	factors := &TopMinerFactors{
		MinerAddress:      miner4.Address,
		Qualified:         true,
		TimeOfCalculation: now + 10000,
		PayoutSettings:    defaultPayoutSettings,
		TopMiners: []types.TopMiner{
			*miner1,
			*miner2,
			disqMiner,
			*miner4,
		},
	}
	action, err = GetTopMinerAction(factors)
	require.NoError(t, err)
	require.IsType(t, UpdateMiner{}, action)
	paidMiner := action.(UpdateMiner).miner
	require.Equal(t, int32(0), paidMiner.RewardsPaidCount)
	require.Equal(t, int32(365), paidMiner.QualifiedPeriods)
	require.Equal(t, int32(0), paidMiner.MissedPeriods)
	require.Equal(t, int64(days(365)+9000), paidMiner.QualifiedTime)
	require.Equal(t, int64(0), paidMiner.MissedTime)
	require.Equal(t, factors.TimeOfCalculation, paidMiner.LastUpdatedTime)
	require.Equal(t, factors.TimeOfCalculation-days(365)-9000, paidMiner.LastQualifiedStarted)
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
