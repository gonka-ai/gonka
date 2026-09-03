package types_test

import (
	"reflect"
	"strings"
	"testing"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	blstypes "github.com/productscience/inference/x/bls/types"
	bookkeepertypes "github.com/productscience/inference/x/bookkeeper/types"
	collateraltypes "github.com/productscience/inference/x/collateral/types"
	genesistransfertypes "github.com/productscience/inference/x/genesistransfer/types"
	"github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
	streamvestingtypes "github.com/productscience/inference/x/streamvesting/types"
)

// intentionallyUngrouped is the fee-decision ledger for gonka messages that
// are neither in a fee group nor ante-exempt network duties. A new Msg must
// be added to MessageFeeGroups, IsNetworkDuty, or this map.
func intentionallyUngrouped() map[string]string {
	return map[string]string{
		sdk.MsgTypeURL(&types.MsgUpdateParams{}):                     "governance params; authority-gated",
		sdk.MsgTypeURL(&types.MsgRegisterModel{}):                    "governance model registry",
		sdk.MsgTypeURL(&types.MsgDeleteGovernanceModel{}):            "governance model registry",
		sdk.MsgTypeURL(&types.MsgCreatePartialUpgrade{}):             "governance upgrade",
		sdk.MsgTypeURL(&types.MsgRegisterLiquidityPool{}):            "governance liquidity allowlist",
		sdk.MsgTypeURL(&types.MsgGovernanceCancelBridgeOperation{}):  "governance bridge cancel",
		sdk.MsgTypeURL(&types.MsgRegisterBridgeAddresses{}):          "governance bridge addresses",
		sdk.MsgTypeURL(&types.MsgAddParticipantsToAllowList{}):       "governance allowlist",
		sdk.MsgTypeURL(&types.MsgRemoveParticipantsFromAllowList{}):  "governance allowlist",
		sdk.MsgTypeURL(&types.MsgMigrateAllWrappedTokens{}):          "governance wrapped-token migration",
		sdk.MsgTypeURL(&types.MsgSubmitUnitOfComputePriceProposal{}): "optional price proposal; not epoch-metered",
		sdk.MsgTypeURL(&types.MsgSetClaimRecipients{}):               "optional claim-recipient override; not epoch-metered",
		sdk.MsgTypeURL(&types.MsgCreateTrainshardProposal{}):         "trainshard v0; shard lifecycle is not metered yet",
		sdk.MsgTypeURL(&types.MsgAssembleTrainshard{}):               "trainshard v0; shard lifecycle is not metered yet",
		sdk.MsgTypeURL(&types.MsgSettleTrainshard{}):                 "trainshard v0; shard lifecycle is not metered yet",
		sdk.MsgTypeURL(&types.MsgSetTrainingNodeOptIn{}):             "trainshard v0; host upkeep is not metered yet",
		sdk.MsgTypeURL(&types.MsgRefreshTrainingNodeOptIn{}):         "trainshard v0; host upkeep is not metered yet",
		sdk.MsgTypeURL(&types.MsgAutokickTrainshardNode{}):           "trainshard v0; host upkeep is not metered yet",
		sdk.MsgTypeURL(&blstypes.MsgUpdateParams{}):                  "governance params; authority-gated",
		sdk.MsgTypeURL(&blstypes.MsgRequestThresholdSignature{}):     "omitted from bls group until gateway simulation exists",
		sdk.MsgTypeURL(&collateraltypes.MsgUpdateParams{}):           "governance params; authority-gated",
		sdk.MsgTypeURL(&restrictionstypes.MsgUpdateParams{}):         "governance params; authority-gated",
		sdk.MsgTypeURL(&streamvestingtypes.MsgUpdateParams{}):        "governance params; authority-gated",
		sdk.MsgTypeURL(&genesistransfertypes.MsgUpdateParams{}):      "governance params; authority-gated",
		sdk.MsgTypeURL(&genesistransfertypes.MsgTransferOwnership{}): "one-shot genesis transfer",
		sdk.MsgTypeURL(&bookkeepertypes.MsgUpdateParams{}):           "governance params; authority-gated",
	}
}

func TestEveryRegisteredGonkaMsgHasFeeDecision(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(registry)
	blstypes.RegisterInterfaces(registry)
	collateraltypes.RegisterInterfaces(registry)
	restrictionstypes.RegisterInterfaces(registry)
	streamvestingtypes.RegisterInterfaces(registry)
	genesistransfertypes.RegisterInterfaces(registry)
	bookkeepertypes.RegisterInterfaces(registry)

	ungrouped := intentionallyUngrouped()
	typeURLs := collectFeeDecisionSubjects(registry, ungrouped)
	require.NotEmpty(t, typeURLs, "no message types found to classify")

	for _, typeURL := range typeURLs {
		msg, err := instantiateRegisteredMsg(typeURL)
		require.NoError(t, err, "cannot instantiate %s", typeURL)

		grouped := types.FeeGroupOf(msg) != ""
		duty := types.IsNetworkDuty(msg)
		reason, listed := ungrouped[typeURL]

		switch {
		case listed && reason == "":
			t.Errorf("%s is in intentionallyUngrouped with an empty reason", typeURL)
		case listed && (grouped || duty):
			t.Errorf("%s is listed ungrouped but classified as group=%q duty=%v", typeURL, types.FeeGroupOf(msg), duty)
		case !listed && !grouped && !duty:
			t.Errorf("%s has no fee decision: add it to MessageFeeGroups, IsNetworkDuty, or intentionallyUngrouped", typeURL)
		}
	}
}

func collectFeeDecisionSubjects(registry codectypes.InterfaceRegistry, ungrouped map[string]string) []string {
	seen := make(map[string]struct{})
	for _, iface := range []string{"cosmos.base.v1beta1.Msg", "cosmos.msg.v1.Msg"} {
		for _, typeURL := range registry.ListImplementations(iface) {
			seen[typeURL] = struct{}{}
		}
	}
	for typ := range types.MessageFeeGroups {
		if typ.Kind() != reflect.Ptr {
			continue
		}
		msg, ok := reflect.New(typ.Elem()).Interface().(sdk.Msg)
		if !ok {
			continue
		}
		seen[sdk.MsgTypeURL(msg)] = struct{}{}
	}
	for _, msg := range []sdk.Msg{
		&types.MsgSubmitPocBatch{},
		&blstypes.MsgRequestThresholdSignature{},
	} {
		seen[sdk.MsgTypeURL(msg)] = struct{}{}
	}
	for typeURL := range ungrouped {
		seen[typeURL] = struct{}{}
	}
	for _, method := range types.Msg_serviceDesc.Methods {
		seen["/inference.inference.Msg"+method.MethodName] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for typeURL := range seen {
		out = append(out, typeURL)
	}
	return out
}

func instantiateRegisteredMsg(typeURL string) (sdk.Msg, error) {
	name := strings.TrimPrefix(typeURL, "/")
	msgType := proto.MessageType(name)
	if msgType == nil {
		return nil, protoTypeError("no proto type for " + typeURL)
	}
	v := reflect.New(msgType.Elem()).Interface()
	msg, ok := v.(sdk.Msg)
	if !ok {
		return nil, protoTypeError("not sdk.Msg: " + typeURL)
	}
	return msg, nil
}

type protoTypeError string

func (e protoTypeError) Error() string { return string(e) }
