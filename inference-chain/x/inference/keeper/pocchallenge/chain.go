package pocchallenge

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// Chain is the narrow keeper surface pocchallenge needs. *keeper.Keeper satisfies it.
type Chain interface {
	GetParams(ctx context.Context) (types.Params, error)
	GetParticipant(ctx context.Context, index string) (types.Participant, bool)
	GetRootGroupDataWithLiveMembers(ctx context.Context) (types.EpochGroupData, map[string]bool, error)
	GetEffectiveEpoch(ctx context.Context) (*types.Epoch, bool)
	GetActiveConfirmationPoCEvent(ctx context.Context) (*types.ConfirmationPoCEvent, bool, error)
	GetEpochPerformanceSummary(ctx context.Context, epochIndex uint64, participantId string) (types.EpochPerformanceSummary, bool)
	EligibleChallengeVoter(ctx context.Context, epochIndex uint64, modelID, voter string) bool
	GetMaintenanceState(ctx context.Context, participant sdk.AccAddress) (types.MaintenanceState, bool)
	GetMaintenanceReservation(ctx context.Context, id uint64) (types.MaintenanceReservation, bool)
	GetAllEpochGroupData(ctx context.Context) []types.EpochGroupData
	FixedEpochRewardAmount(epochsSinceGenesis uint64, initialReward uint64, decayRate *types.Decimal) (uint64, error)
	SendCoinsFromAccountToModule(ctx context.Context, sender sdk.AccAddress, module string, amt sdk.Coins, memo string) error
	SendCoinsFromModuleToAccount(ctx context.Context, module string, recipient sdk.AccAddress, amt sdk.Coins, memo string) error
	SendCoinsFromModuleToModule(ctx context.Context, sender, recipient string, amt sdk.Coins, memo string) error
	PayParticipantFromModule(ctx context.Context, address string, amount int64, moduleName string, memo string, vestingPeriods *uint64) error
	IsPoCParticipantBlocked(ctx context.Context, address string) bool
	LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{})
	LogError(msg string, subSystem types.SubSystem, keyvals ...interface{})
}
