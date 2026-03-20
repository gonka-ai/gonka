package keeper

import (
	"context"
	"reflect"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type Permission string

type permissionChecker func(k msgServer, ctx context.Context, signer sdk.AccAddress) error

var permissionCheckers = map[Permission]permissionChecker{
	GovernancePermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return k.checkGovernancePermission(ctx, signer)
	},
	AccountPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return k.checkAccountPermission(ctx, signer)
	},
	ParticipantPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return k.checkParticipantPermission(ctx, signer)
	},
	ActiveParticipantPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return k.checkActiveParticipantPermission(ctx, signer, 0)
	},
	PreviousActiveParticipantPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return k.checkActiveParticipantPermission(ctx, signer, 1)
	},
	TrainingExecPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return k.checkTrainingExecPermission(ctx, signer)
	},
	TrainingStartPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return k.checkTrainingStartPermission(ctx, signer)
	},
	CurrentActiveParticipantPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return k.checkCurrentActiveParticipantPermission(ctx, signer)
	},
	ContractPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return k.checkContractPermission(ctx, signer)
	},
	OpenRegistrationPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		if k.IsNewParticipantRegistrationClosed(ctx, sdkCtx.BlockHeight()) {
			return types.ErrNewParticipantRegistrationClosed
		}
		return nil
	},
	EscrowAllowListPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return k.checkEscrowAllowListPermission(ctx, signer)
	},
	NoPermission: func(k msgServer, ctx context.Context, signer sdk.AccAddress) error {
		return nil
	},
}

const (
	// GovernancePermission allows only the module authority signer.
	GovernancePermission Permission = "governance"
	// TrainingExecPermission allows users in the training-exec allow list.
	TrainingExecPermission Permission = "training_execution"
	// TrainingStartPermission allows users in the training-start allow list.
	TrainingStartPermission Permission = "training_start"
	// ParticipantPermission allows registered participants.
	ParticipantPermission Permission = "participant"
	// ActiveParticipantPermission allows participants active in the current epoch.
	ActiveParticipantPermission Permission = "active_participant"
	// AccountPermission allows any existing account.
	AccountPermission Permission = "account"
	// CurrentActiveParticipantPermission allows non-excluded active participants.
	CurrentActiveParticipantPermission Permission = "current_active_participant"
	// ContractPermission allows only wasm contract addresses.
	ContractPermission Permission = "contract"
	// NoPermission unconditionally authorizes the message signer.
	NoPermission Permission = "none"
	// PreviousActiveParticipantPermission allows participants active in the previous epoch.
	PreviousActiveParticipantPermission Permission = "previous_active_participant"
	// OpenRegistrationPermission allows only when new participant registration is open.
	OpenRegistrationPermission Permission = "open_registration"
	// Escrow allow list only
	EscrowAllowListPermission Permission = "escrow_allow_list"
)

// This is no longer "operational" at runtime, but it is still used in the unit test, allowing us to trust
// this entire list as a source of truth for message permissions.
var MessagePermissions = map[reflect.Type][]Permission{
	reflect.TypeOf((*types.MsgUpdateParams)(nil)):                    {GovernancePermission},
	reflect.TypeOf((*types.MsgSetTrainingAllowList)(nil)):            {GovernancePermission},
	reflect.TypeOf((*types.MsgAddUserToTrainingAllowList)(nil)):      {GovernancePermission},
	reflect.TypeOf((*types.MsgRemoveUserFromTrainingAllowList)(nil)): {GovernancePermission},
	reflect.TypeOf((*types.MsgAddParticipantsToAllowList)(nil)):      {GovernancePermission},
	reflect.TypeOf((*types.MsgRemoveParticipantsFromAllowList)(nil)): {GovernancePermission},
	reflect.TypeOf((*types.MsgApproveBridgeTokenForTrading)(nil)):    {GovernancePermission},
	reflect.TypeOf((*types.MsgApproveIbcTokenForTrading)(nil)):       {GovernancePermission},
	reflect.TypeOf((*types.MsgCreatePartialUpgrade)(nil)):            {GovernancePermission},
	reflect.TypeOf((*types.MsgMigrateAllWrappedTokens)(nil)):         {GovernancePermission},
	reflect.TypeOf((*types.MsgRegisterBridgeAddresses)(nil)):         {GovernancePermission},
	reflect.TypeOf((*types.MsgDeleteGovernanceModel)(nil)):           {GovernancePermission},
	reflect.TypeOf((*types.MsgRegisterLiquidityPool)(nil)):           {GovernancePermission},
	reflect.TypeOf((*types.MsgRegisterModel)(nil)):                   {GovernancePermission},
	reflect.TypeOf((*types.MsgRegisterTokenMetadata)(nil)):           {GovernancePermission},
	reflect.TypeOf((*types.MsgRegisterIbcTokenMetadata)(nil)):        {GovernancePermission},
	reflect.TypeOf((*types.MsgRegisterWrappedTokenContract)(nil)):    {GovernancePermission},

	reflect.TypeOf((*types.MsgBridgeExchange)(nil)):    {ActiveParticipantPermission, PreviousActiveParticipantPermission},
	reflect.TypeOf((*types.MsgRequestBridgeMint)(nil)): {AccountPermission},

	reflect.TypeOf((*types.MsgRequestBridgeWithdrawal)(nil)): {ContractPermission},

	reflect.TypeOf((*types.MsgSubmitNewParticipant)(nil)):         {OpenRegistrationPermission},
	reflect.TypeOf((*types.MsgSubmitNewUnfundedParticipant)(nil)): {OpenRegistrationPermission},

	// These are special cases authorized by GroupPolicy
	reflect.TypeOf((*types.MsgInvalidateInference)(nil)): {NoPermission},
	reflect.TypeOf((*types.MsgRevalidateInference)(nil)): {NoPermission},

	reflect.TypeOf((*types.MsgClaimRewards)(nil)):                     {ActiveParticipantPermission, PreviousActiveParticipantPermission},
	reflect.TypeOf((*types.MsgSubmitHardwareDiff)(nil)):               {ParticipantPermission},
	reflect.TypeOf((*types.MsgSubmitPocBatch)(nil)):                   {ParticipantPermission},
	reflect.TypeOf((*types.MsgSubmitPocValidation)(nil)):              {ParticipantPermission},
	reflect.TypeOf((*types.MsgSubmitPocValidationsV2)(nil)):           {NoPermission},
	reflect.TypeOf((*types.MsgPoCV2StoreCommit)(nil)):                 {NoPermission},
	reflect.TypeOf((*types.MsgMLNodeWeightDistribution)(nil)):         {NoPermission},
	reflect.TypeOf((*types.MsgSubmitSeed)(nil)):                       {ParticipantPermission},
	reflect.TypeOf((*types.MsgSubmitUnitOfComputePriceProposal)(nil)): {ActiveParticipantPermission},

	reflect.TypeOf((*types.MsgSubmitTrainingKvRecord)(nil)):         {TrainingExecPermission},
	reflect.TypeOf((*types.MsgJoinTraining)(nil)):                   {TrainingExecPermission},
	reflect.TypeOf((*types.MsgJoinTrainingStatus)(nil)):             {TrainingExecPermission},
	reflect.TypeOf((*types.MsgSetBarrier)(nil)):                     {TrainingExecPermission},
	reflect.TypeOf((*types.MsgTrainingHeartbeat)(nil)):              {TrainingExecPermission},
	reflect.TypeOf((*types.MsgAssignTrainingTask)(nil)):             {TrainingStartPermission},
	reflect.TypeOf((*types.MsgClaimTrainingTaskForAssignment)(nil)): {TrainingStartPermission},
	reflect.TypeOf((*types.MsgCreateDummyTrainingTask)(nil)):        {TrainingStartPermission},
	reflect.TypeOf((*types.MsgCreateTrainingTask)(nil)):             {TrainingStartPermission},

	reflect.TypeOf((*types.MsgStartInference)(nil)): {ActiveParticipantPermission},
	// Finish could happen after a new epoch has started
	reflect.TypeOf((*types.MsgFinishInference)(nil)): {ActiveParticipantPermission, PreviousActiveParticipantPermission},
	reflect.TypeOf((*types.MsgValidation)(nil)):      {ActiveParticipantPermission, PreviousActiveParticipantPermission},

	reflect.TypeOf((*types.MsgCreateSubnetEscrow)(nil)): {EscrowAllowListPermission},
	reflect.TypeOf((*types.MsgSettleSubnetEscrow)(nil)): {EscrowAllowListPermission},
}

type HasSigners interface {
	GetSignersStrings() []string
}

// CheckPermission verifies that at least one signer on msg has one of the
// declared permissions for the message type and that local/global declarations match.
// At least one permission argument is required by signature.
func (k msgServer) CheckPermission(ctx context.Context, msg HasSigners, permission Permission, permissions ...Permission) error {
	signers := msg.GetSignersStrings()
	var err error
	for _, signer := range signers {
		err = k.checkPermissions(ctx, signer, permission, permissions)
		if err == nil {
			return nil
		}
	}
	return err
}

func (k msgServer) checkPermissions(ctx context.Context, signer string, requiredPermission Permission, additionalPermissions []Permission) error {
	signerAddr, err := sdk.AccAddressFromBech32(signer)
	if err != nil {
		return err
	}
	var lastErr error
	if lastErr = k.checkSinglePermission(ctx, requiredPermission, signerAddr); lastErr == nil {
		return nil
	}
	for _, perm := range additionalPermissions {
		err := k.checkSinglePermission(ctx, perm, signerAddr)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

func (k msgServer) checkSinglePermission(ctx context.Context, perm Permission, signerAddr sdk.AccAddress) error {
	checker, ok := permissionCheckers[perm]
	if !ok {
		return types.ErrInvalidPermission
	}
	return checker(k, ctx, signerAddr)
}

func (k msgServer) checkAccountPermission(ctx context.Context, signer sdk.AccAddress) error {
	if !k.AccountKeeper.HasAccount(ctx, signer) {
		return types.ErrAccountNotFound
	}
	return nil
}

func (k msgServer) checkParticipantPermission(ctx context.Context, signer sdk.AccAddress) error {
	found, err := k.Participants.Has(ctx, signer)
	if err != nil || !found {
		return types.ErrParticipantNotFound
	}
	return nil
}

func (k msgServer) checkActiveParticipantPermission(ctx context.Context, signer sdk.AccAddress, epochOffset uint64) error {
	currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		return err
	}
	if currentEpoch < epochOffset {
		return types.ErrActiveParticipantNotFound
	}
	found, err := k.ActiveParticipantsSet.Has(ctx, collections.Join(currentEpoch-epochOffset, signer))
	if err != nil {
		return err
	}
	if !found {
		return types.ErrActiveParticipantNotFound
	}
	return nil
}

func (k msgServer) checkCurrentActiveParticipantPermission(ctx context.Context, signer sdk.AccAddress) error {
	err := k.checkActiveParticipantPermission(ctx, signer, 0)
	if err != nil {
		return err
	}
	currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		return err
	}
	has, err := k.ExcludedParticipantsMap.Has(ctx, collections.Join(currentEpoch, signer))
	if err != nil {
		return err
	}
	if has {
		return types.ErrParticipantNotFound
	}
	return nil
}

func (k msgServer) checkTrainingExecPermission(ctx context.Context, signer sdk.AccAddress) error {
	allowed, err := k.TrainingExecAllowListSet.Has(ctx, signer)
	if err != nil {
		return err
	}
	if !allowed {
		return types.ErrTrainingNotAllowed
	}
	return nil
}

func (k msgServer) checkTrainingStartPermission(ctx context.Context, signer sdk.AccAddress) error {
	allowed, err := k.TrainingStartAllowListSet.Has(ctx, signer)
	if err != nil {
		return err
	}
	if !allowed {
		return types.ErrTrainingNotAllowed
	}
	return nil
}

func (k msgServer) checkGovernancePermission(ctx context.Context, signer sdk.AccAddress) error {
	if k.GetAuthority() != signer.String() {
		return types.ErrInvalidSigner
	}
	return nil
}

func (k msgServer) checkContractPermission(ctx context.Context, signer sdk.AccAddress) error {
	if k.wasmKeeper == nil {
		return types.ErrNotSupported
	}
	contractInfo := k.wasmKeeper.GetContractInfo(ctx, signer)
	if contractInfo == nil {
		return types.ErrNotAContractAddress
	}
	return nil
}

func (k msgServer) checkEscrowAllowListPermission(ctx context.Context, signer sdk.AccAddress) error {
	allowed := k.IsAllowedEscrowCreator(ctx, signer.String())
	if !allowed {
		return types.ErrNotAllowedEscrowCreator
	}
	return nil
}
