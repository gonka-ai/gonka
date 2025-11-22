package types

import (
	"context"

	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
	blstypes "github.com/productscience/inference/x/bls/types"
)

// AccountKeeper defines the expected interface for the account module.
type AccountKeeper interface {
	GetAccount(context.Context, sdk.AccAddress) sdk.AccountI // only used for simulation
	GetModuleAddress(moduleName string) sdk.AccAddress
	SetAccount(ctx context.Context, acc sdk.AccountI)
	NewAccountWithAddress(context.Context, sdk.AccAddress) sdk.AccountI
	GetModuleAccount(ctx context.Context, moduleName string) sdk.ModuleAccountI
}

// BankKeeper defines the expected interface for the Bank module.
type BankKeeper interface {
	SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins
	SpendableCoin(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	GetDenomMetaData(ctx context.Context, denom string) (banktypes.Metadata, bool)
}

type GroupMessageKeeper interface {
	CreateGroup(goCtx context.Context, msg *group.MsgCreateGroup) (*group.MsgCreateGroupResponse, error)
	CreateGroupWithPolicy(ctx context.Context, msg *group.MsgCreateGroupWithPolicy) (*group.MsgCreateGroupWithPolicyResponse, error)
	UpdateGroupMembers(goCtx context.Context, msg *group.MsgUpdateGroupMembers) (*group.MsgUpdateGroupMembersResponse, error)
	UpdateGroupMetadata(goCtx context.Context, msg *group.MsgUpdateGroupMetadata) (*group.MsgUpdateGroupMetadataResponse, error)
	SubmitProposal(goCtx context.Context, msg *group.MsgSubmitProposal) (*group.MsgSubmitProposalResponse, error)
	Vote(goCtx context.Context, msg *group.MsgVote) (*group.MsgVoteResponse, error)
	GroupInfo(goCtx context.Context, request *group.QueryGroupInfoRequest) (*group.QueryGroupInfoResponse, error)
	GroupMembers(goCtx context.Context, request *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error)
	ProposalsByGroupPolicy(goCtx context.Context, request *group.QueryProposalsByGroupPolicyRequest) (*group.QueryProposalsByGroupPolicyResponse, error)
}

// ParamSubspace defines the expected Subspace interface for parameters.
type ParamSubspace interface {
	Get(context.Context, []byte, interface{})
	Set(context.Context, []byte, interface{})
}

type StakingHooks interface {
	AfterValidatorCreated(ctx context.Context, valAddr sdk.ValAddress) error                           // Must be called when a validator is created
	BeforeValidatorModified(ctx context.Context, valAddr sdk.ValAddress) error                         // Must be called when a validator's state changes
	AfterValidatorRemoved(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error // Must be called when a validator is deleted

	AfterValidatorBonded(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error         // Must be called when a validator is bonded
	AfterValidatorBeginUnbonding(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error // Must be called when a validator begins unbonding

	BeforeDelegationCreated(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error        // Must be called when a delegation is created
	BeforeDelegationSharesModified(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error // Must be called when a delegation's shares are modified
	BeforeDelegationRemoved(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error        // Must be called when a delegation is removed
	AfterDelegationModified(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error
	BeforeValidatorSlashed(ctx context.Context, valAddr sdk.ValAddress, fraction math.LegacyDec) error
}

type ValidatorSet interface {
	// iterate through validators by operator address, execute func for each validator
	IterateValidators(context.Context,
		func(index int64, validator types.ValidatorI) (stop bool)) error
}

type StakingKeeper interface {
	SetComputeValidators(ctx context.Context, computeResults []keeper.ComputeResult, isTestnet bool) ([]types.Validator, error)
	GetAllValidators(ctx context.Context) (validators []types.Validator, err error)
}

// CollateralKeeper defines the expected interface for the Collateral module.
type CollateralKeeper interface {
	AdvanceEpoch(ctx context.Context, completedEpoch uint64)
	GetCollateral(ctx context.Context, participant sdk.AccAddress) (collateral sdk.Coin, found bool)
	Slash(ctx context.Context, participant sdk.AccAddress, slashFraction math.LegacyDec, reason string) (sdk.Coin, error)
}

// StreamVestingKeeper defines the expected interface for the StreamVesting module.
type StreamVestingKeeper interface {
	AddVestedRewards(ctx context.Context, participantAddress string, fundingModule string, amount sdk.Coins, vestingEpochs *uint64, memo string) error
	AdvanceEpoch(ctx context.Context, completedEpoch uint64) error
}

type ParticipantKeeper interface {
	GetParticipant(ctx context.Context, index string) (val Participant, found bool)
	SetParticipant(ctx context.Context, participant Participant) error
	RemoveParticipant(ctx context.Context, index string)
	GetAllParticipant(ctx context.Context) []Participant
	ParticipantAll(ctx context.Context, req *QueryAllParticipantRequest) (*QueryAllParticipantResponse, error)
}

type HardwareNodeKeeper interface {
	GetHardwareNodes(ctx context.Context, address string) (*HardwareNodes, bool)
}

type EpochGroupDataKeeper interface {
	SetEpochGroupData(ctx context.Context, epochGroupData EpochGroupData)
	GetEpochGroupData(ctx context.Context, epochIndex uint64, modelId string) (val EpochGroupData, found bool)
	RemoveEpochGroupData(ctx context.Context, epochIndex uint64, modelId string)
	GetAllEpochGroupData(ctx context.Context) []EpochGroupData
}

type BookkeepingBankKeeper interface {
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins, memo string) error
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error
	SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins, memo string) error
	MintCoins(ctx context.Context, moduleName string, amt sdk.Coins, memo string) error
	BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins, memo string) error
	// For logging transactions to tracking accounts, like vesting holds
	LogSubAccountTransaction(ctx context.Context, recipient string, sender string, subAccount string, amt sdk.Coin, memo string)
}

type ModelKeeper interface {
	GetGovernanceModel(ctx context.Context, id string) (val *Model, found bool)
	GetGovernanceModels(ctx context.Context) (list []*Model, err error)
}

type AuthzKeeper interface {
	GranterGrants(ctx context.Context, req *authztypes.QueryGranterGrantsRequest) (*authztypes.QueryGranterGrantsResponse, error)
}

// BlsKeeper defines the expected interface for the BLS module.
type BlsKeeper interface {
	// DKG methods
	InitiateKeyGenerationForEpoch(ctx sdk.Context, epochID uint64, finalizedParticipants []blstypes.ParticipantWithWeightAndKey) error
	GetEpochBLSData(ctx sdk.Context, epochID uint64) (blstypes.EpochBLSData, bool)
	SetActiveEpochID(ctx sdk.Context, epochID uint64)
	GetActiveEpochID(ctx sdk.Context) (uint64, bool)

	// Threshold signing methods
	RequestThresholdSignature(ctx sdk.Context, signingData blstypes.SigningData) error
	GetSigningStatus(ctx sdk.Context, requestID []byte) (*blstypes.ThresholdSigningRequest, error)
	ListActiveSigningRequests(ctx sdk.Context, currentEpochID uint64) ([]*blstypes.ThresholdSigningRequest, error)
}

// UpgradeKeeper defines the expected interface for the upgrade module.
type UpgradeKeeper interface {
	GetUpgradePlan(ctx context.Context) (plan upgradetypes.Plan, err error)
}
