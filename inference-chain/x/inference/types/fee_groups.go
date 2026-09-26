package types

import (
	"reflect"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	blstypes "github.com/productscience/inference/x/bls/types"
	bookkeepertypes "github.com/productscience/inference/x/bookkeeper/types"
	collateraltypes "github.com/productscience/inference/x/collateral/types"
	genesistransfertypes "github.com/productscience/inference/x/genesistransfer/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
	streamvestingtypes "github.com/productscience/inference/x/streamvesting/types"
)

const (
	FeeGroupEpoch      = "epoch"
	FeeGroupBLS        = "bls"
	FeeGroupDevshard   = "devshard"
	FeeGroupBridge     = "bridge"
	FeeGroupWasm       = "wasm"
	FeeGroupCosmos     = "cosmos"
	FeeGroupIBC        = "ibc"
	FeeGroupOnboarding = "onboarding"
	FeeGroupGovernance = "governance"
)

// KnownFeeGroups is the compiled set of fee-group names. It is
// authoritative for both groups[].name and enabled_fee_groups; Validate
// rejects any name that is not in this set.
var KnownFeeGroups = map[string]struct{}{
	FeeGroupEpoch:      {},
	FeeGroupBLS:        {},
	FeeGroupDevshard:   {},
	FeeGroupBridge:     {},
	FeeGroupWasm:       {},
	FeeGroupCosmos:     {},
	FeeGroupIBC:        {},
	FeeGroupOnboarding: {},
	FeeGroupGovernance: {},
}

// MessageFeeGroups maps explicit gonka message types to a fee group.
// Unlisted types fall through to prefix matchers, then to "" (ungrouped).
// A group that is not in enabled_fee_groups stays free. The governance
// group ships disabled, so the governance route costs nothing until
// governance enables it.
var MessageFeeGroups = map[reflect.Type]string{
	// epoch
	reflect.TypeOf((*MsgSubmitHardwareDiff)(nil)):                 FeeGroupEpoch,
	reflect.TypeOf((*MsgSubmitSeed)(nil)):                         FeeGroupEpoch,
	reflect.TypeOf((*MsgPoCV2StoreCommit)(nil)):                   FeeGroupEpoch,
	reflect.TypeOf((*MsgCreatePoCChallenge)(nil)):                 FeeGroupEpoch,
	reflect.TypeOf((*MsgPoCChallengeStoreCommit)(nil)):            FeeGroupEpoch,
	reflect.TypeOf((*MsgSubmitPoCChallengeValidations)(nil)):      FeeGroupEpoch,
	reflect.TypeOf((*MsgMLNodeWeightDistribution)(nil)):           FeeGroupEpoch,
	reflect.TypeOf((*MsgSubmitPocValidationsV2)(nil)):             FeeGroupEpoch,
	reflect.TypeOf((*MsgClaimRewards)(nil)):                       FeeGroupEpoch,
	reflect.TypeOf((*MsgSetClaimRecipients)(nil)):                 FeeGroupEpoch,
	reflect.TypeOf((*MsgSubmitUnitOfComputePriceProposal)(nil)):   FeeGroupEpoch,
	reflect.TypeOf((*MsgDeclarePoCIntent)(nil)):                   FeeGroupEpoch,
	reflect.TypeOf((*MsgSetPoCDelegation)(nil)):                   FeeGroupEpoch,
	reflect.TypeOf((*MsgRefusePoCDelegation)(nil)):                FeeGroupEpoch,
	reflect.TypeOf((*collateraltypes.MsgDepositCollateral)(nil)):  FeeGroupEpoch,
	reflect.TypeOf((*collateraltypes.MsgWithdrawCollateral)(nil)): FeeGroupEpoch,

	// bls (omit deprecated MsgRequestThresholdSignature; it stays ungrouped)
	reflect.TypeOf((*blstypes.MsgSubmitDealerPart)(nil)):                  FeeGroupBLS,
	reflect.TypeOf((*blstypes.MsgSubmitVerificationVector)(nil)):          FeeGroupBLS,
	reflect.TypeOf((*blstypes.MsgRespondDealerComplaints)(nil)):           FeeGroupBLS,
	reflect.TypeOf((*blstypes.MsgSubmitGroupKeyValidationSignature)(nil)): FeeGroupBLS,
	reflect.TypeOf((*blstypes.MsgSubmitPartialSignature)(nil)):            FeeGroupBLS,

	// devshard
	reflect.TypeOf((*MsgCreateDevshardEscrow)(nil)):       FeeGroupDevshard,
	reflect.TypeOf((*MsgSettleDevshardEscrow)(nil)):       FeeGroupDevshard,
	reflect.TypeOf((*MsgSetDevshardRequestsEnabled)(nil)): FeeGroupDevshard,

	// bridge (authority-only siblings are in the governance group)
	reflect.TypeOf((*MsgBridgeExchange)(nil)):               FeeGroupBridge,
	reflect.TypeOf((*MsgRequestBridgeMint)(nil)):            FeeGroupBridge,
	reflect.TypeOf((*MsgRequestBridgeWithdrawal)(nil)):      FeeGroupBridge,
	reflect.TypeOf((*MsgCancelBridgeOperation)(nil)):        FeeGroupBridge,
	reflect.TypeOf((*MsgRegisterTokenMetadata)(nil)):        FeeGroupBridge,
	reflect.TypeOf((*MsgApproveBridgeTokenForTrading)(nil)): FeeGroupBridge,
	reflect.TypeOf((*MsgRegisterWrappedTokenContract)(nil)): FeeGroupBridge,
	reflect.TypeOf((*MsgRegisterIbcTokenMetadata)(nil)):     FeeGroupBridge,
	reflect.TypeOf((*MsgApproveIbcTokenForTrading)(nil)):    FeeGroupBridge,

	// onboarding
	reflect.TypeOf((*MsgSubmitNewParticipant)(nil)):         FeeGroupOnboarding,
	reflect.TypeOf((*MsgSubmitNewUnfundedParticipant)(nil)): FeeGroupOnboarding,

	// cosmos extras (gonka msgs, not SDK prefix)
	reflect.TypeOf((*streamvestingtypes.MsgTransferWithVesting)(nil)):      FeeGroupCosmos,
	reflect.TypeOf((*streamvestingtypes.MsgBatchTransferWithVesting)(nil)): FeeGroupCosmos,
	reflect.TypeOf((*restrictionstypes.MsgExecuteEmergencyTransfer)(nil)):  FeeGroupCosmos,
	reflect.TypeOf((*MsgScheduleMaintenance)(nil)):                         FeeGroupCosmos,
	reflect.TypeOf((*MsgCancelMaintenance)(nil)):                           FeeGroupCosmos,

	// governance: authority-gated. Fee-free while "governance" is not enabled.
	// MsgRequestThresholdSignature stays ungrouped on purpose.
	reflect.TypeOf((*MsgUpdateParams)(nil)):                      FeeGroupGovernance,
	reflect.TypeOf((*MsgRegisterModel)(nil)):                     FeeGroupGovernance,
	reflect.TypeOf((*MsgDeleteGovernanceModel)(nil)):             FeeGroupGovernance,
	reflect.TypeOf((*MsgPutDevshardApprovedVersion)(nil)):        FeeGroupGovernance,
	reflect.TypeOf((*MsgDeleteDevshardApprovedVersion)(nil)):     FeeGroupGovernance,
	reflect.TypeOf((*MsgCreatePartialUpgrade)(nil)):              FeeGroupGovernance,
	reflect.TypeOf((*MsgRegisterLiquidityPool)(nil)):             FeeGroupGovernance,
	reflect.TypeOf((*MsgGovernanceCancelBridgeOperation)(nil)):   FeeGroupGovernance,
	reflect.TypeOf((*MsgRegisterBridgeAddresses)(nil)):           FeeGroupGovernance,
	reflect.TypeOf((*MsgAddParticipantsToAllowList)(nil)):        FeeGroupGovernance,
	reflect.TypeOf((*MsgRemoveParticipantsFromAllowList)(nil)):   FeeGroupGovernance,
	reflect.TypeOf((*MsgMigrateAllWrappedTokens)(nil)):           FeeGroupGovernance,
	reflect.TypeOf((*blstypes.MsgUpdateParams)(nil)):             FeeGroupGovernance,
	reflect.TypeOf((*collateraltypes.MsgUpdateParams)(nil)):      FeeGroupGovernance,
	reflect.TypeOf((*restrictionstypes.MsgUpdateParams)(nil)):    FeeGroupGovernance,
	reflect.TypeOf((*streamvestingtypes.MsgUpdateParams)(nil)):   FeeGroupGovernance,
	reflect.TypeOf((*genesistransfertypes.MsgUpdateParams)(nil)): FeeGroupGovernance,
	reflect.TypeOf((*bookkeepertypes.MsgUpdateParams)(nil)):      FeeGroupGovernance,
}

var cosmosTypeURLPrefixes = []string{
	"/cosmos.bank.",
	"/cosmos.staking.",
	"/cosmos.distribution.",
	"/cosmos.slashing.",
	"/cosmos.authz.",
	"/cosmos.feegrant.",
	"/cosmos.group.",
	"/cosmos.vesting.",
	"/cosmos.auth.vesting.",
	"/cosmos.evidence.",
	"/cosmos.nft.",
	"/cosmos.circuit.",
	"/cosmos.crisis.",
}

// governanceTypeURLPrefixes are authority-module SDK messages that are not
// in cosmosTypeURLPrefixes. /cosmos.auth.v1beta1. is narrower than
// /cosmos.auth. so vesting accounts stay in the cosmos group.
var governanceTypeURLPrefixes = []string{
	"/cosmos.gov.",
	"/cosmos.upgrade.",
	"/cosmos.consensus.",
	"/cosmos.mint.",
	"/cosmos.auth.v1beta1.",
}

const (
	authzMsgExecTypeURL = "/cosmos.authz.v1beta1.MsgExec"
	ibcTypeURLPrefix    = "/ibc."
	wasmTypeURLPrefix   = "/cosmwasm.wasm."
)

// FeeGroupOf returns the compiled fee group for msg, or "" if ungrouped.
// MsgExec itself is never assigned a group; callers must unwrap one level
// and classify inners. Nested MsgExec is not recursed.
func FeeGroupOf(msg sdk.Msg) string {
	if msg == nil {
		return ""
	}
	if _, ok := msg.(*authztypes.MsgExec); ok {
		return ""
	}
	if g, ok := MessageFeeGroups[reflect.TypeOf(msg)]; ok {
		return g
	}
	return feeGroupByTypeURL(sdk.MsgTypeURL(msg))
}

// CompiledFeeGroupForTypeURL returns the compiled fee group for a type URL,
// including SDK/IBC/wasm prefix groups. Empty means the type is ungrouped.
func CompiledFeeGroupForTypeURL(typeURL string) string {
	if typeURL == "" {
		return ""
	}
	for typ, group := range MessageFeeGroups {
		if typ.Kind() != reflect.Ptr {
			continue
		}
		msg, ok := reflect.New(typ.Elem()).Interface().(sdk.Msg)
		if !ok {
			continue
		}
		if sdk.MsgTypeURL(msg) == typeURL {
			return group
		}
	}
	return feeGroupByTypeURL(typeURL)
}

func feeGroupByTypeURL(typeURL string) string {
	if typeURL == authzMsgExecTypeURL {
		return ""
	}
	for _, p := range governanceTypeURLPrefixes {
		if strings.HasPrefix(typeURL, p) {
			return FeeGroupGovernance
		}
	}
	for _, p := range cosmosTypeURLPrefixes {
		if strings.HasPrefix(typeURL, p) {
			return FeeGroupCosmos
		}
	}
	if strings.HasPrefix(typeURL, ibcTypeURLPrefix) {
		return FeeGroupIBC
	}
	if strings.HasPrefix(typeURL, wasmTypeURLPrefix) {
		return FeeGroupWasm
	}
	return ""
}

// IsKnownFeeGroup reports whether name is a compiled fee-group constant.
func IsKnownFeeGroup(name string) bool {
	_, ok := KnownFeeGroups[name]
	return ok
}

// IsNetworkDuty reports whether msg is a protocol obligation that ante
// exempts from fees. This set is compiled, not a gov param: enabling a fee
// group does not charge these types until a later upgrade removes them here.
// MsgPoCV2StoreCommit, HardwareDiff, and MsgRequestThresholdSignature are
// intentionally excluded.
func IsNetworkDuty(msg sdk.Msg) bool {
	switch msg.(type) {
	case *MsgSubmitPocBatch,
		*MsgSubmitPocValidationsV2,
		*MsgSubmitPoCChallengeValidations,
		*MsgMLNodeWeightDistribution,
		*MsgSubmitSeed,
		*MsgClaimRewards,
		*MsgSettleDevshardEscrow,
		*blstypes.MsgSubmitDealerPart,
		*blstypes.MsgSubmitVerificationVector,
		*blstypes.MsgRespondDealerComplaints,
		*blstypes.MsgSubmitGroupKeyValidationSignature,
		*blstypes.MsgSubmitPartialSignature:
		return true
	default:
		return false
	}
}
