package pocchallenge

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type fakeChain struct {
	params          types.Params
	participants    map[string]types.Participant
	live            map[string]bool
	root            types.EpochGroupData
	groups          []types.EpochGroupData
	epoch           *types.Epoch
	event           *types.ConfirmationPoCEvent
	eventActive     bool
	snapshot        types.PoCValidationSnapshot
	haveSnap        bool
	guardianEnabled bool
	guardians       []string
	summaries       map[string]types.EpochPerformanceSummary
	blocked         map[string]bool
	sends           []string
	maintState      map[string]types.MaintenanceState
	reservations    map[uint64]types.MaintenanceReservation
}

func (f *fakeChain) GetParams(ctx context.Context) (types.Params, error) { return f.params, nil }
func (f *fakeChain) GetParticipant(ctx context.Context, index string) (types.Participant, bool) {
	p, ok := f.participants[index]
	return p, ok
}
func (f *fakeChain) GetRootGroupDataWithLiveMembers(ctx context.Context) (types.EpochGroupData, map[string]bool, error) {
	return f.root, f.live, nil
}
func (f *fakeChain) GetEffectiveEpoch(ctx context.Context) (*types.Epoch, bool) {
	return f.epoch, f.epoch != nil
}
func (f *fakeChain) GetActiveConfirmationPoCEvent(ctx context.Context) (*types.ConfirmationPoCEvent, bool, error) {
	return f.event, f.eventActive, nil
}
func (f *fakeChain) GetEpochPerformanceSummary(ctx context.Context, epochIndex uint64, participantId string) (types.EpochPerformanceSummary, bool) {
	s, ok := f.summaries[participantId]
	return s, ok
}
func (f *fakeChain) EligibleChallengeVoter(ctx context.Context, epochIndex uint64, modelID, voter string) bool {
	if !f.haveSnap {
		return false
	}
	for _, mvw := range f.snapshot.ModelVotingPowers {
		if mvw == nil || mvw.ModelId != modelID {
			continue
		}
		for _, e := range mvw.VotingPowers {
			if e != nil && e.Address == voter {
				return true
			}
		}
	}
	if f.guardianEnabled {
		for _, g := range f.guardians {
			if g == voter {
				return true
			}
		}
	}
	return false
}
func (f *fakeChain) GetMaintenanceState(ctx context.Context, participant sdk.AccAddress) (types.MaintenanceState, bool) {
	s, ok := f.maintState[participant.String()]
	return s, ok
}
func (f *fakeChain) GetMaintenanceReservation(ctx context.Context, id uint64) (types.MaintenanceReservation, bool) {
	r, ok := f.reservations[id]
	return r, ok
}
func (f *fakeChain) GetAllEpochGroupData(ctx context.Context) []types.EpochGroupData {
	if len(f.groups) > 0 {
		return f.groups
	}
	return []types.EpochGroupData{f.root}
}
func (f *fakeChain) FixedEpochRewardAmount(epochsSinceGenesis uint64, initialReward uint64, decayRate *types.Decimal) (uint64, error) {
	return initialReward, nil
}
func (f *fakeChain) SendCoinsFromAccountToModule(ctx context.Context, sender sdk.AccAddress, module string, amt sdk.Coins, memo string) error {
	f.sends = append(f.sends, "lock:"+memo)
	return nil
}
func (f *fakeChain) SendCoinsFromModuleToAccount(ctx context.Context, module string, recipient sdk.AccAddress, amt sdk.Coins, memo string) error {
	f.sends = append(f.sends, "refund:"+memo)
	return nil
}
func (f *fakeChain) SendCoinsFromModuleToModule(ctx context.Context, sender, recipient string, amt sdk.Coins, memo string) error {
	f.sends = append(f.sends, "comp:"+memo)
	return nil
}
func (f *fakeChain) PayParticipantFromModule(ctx context.Context, address string, amount int64, moduleName string, memo string, vestingPeriods *uint64) error {
	f.sends = append(f.sends, "pay:"+memo)
	return nil
}
func (f *fakeChain) IsPoCParticipantBlocked(ctx context.Context, address string) bool {
	return f.blocked[address]
}
func (f *fakeChain) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{})  {}
func (f *fakeChain) LogError(msg string, subSystem types.SubSystem, keyvals ...interface{}) {}
