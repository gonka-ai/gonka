package simulation

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/types/kv"
	"github.com/cosmos/gogoproto/proto"

	"github.com/productscience/inference/x/inference/types"
)

// NewDecodeStore returns the x/inference store decoder used by
// simtestutil.GetSimulationLog on AppHash divergence. The simsx happy-path
// never invokes it; the decoder feeds (a) TestAppImportExport_Postrun's
// existing diff path and (b) the TestAppStateDeterminism divergence dump
// added alongside this decoder.
//
// Coverage scope is the ~39 collections actively written by the current
// simsx factories and EndBlocker paths. Bridge / devshard / liquidity /
// legacy training / cleared-by-upgrade prefixes are excluded — those
// collections are never produced by the sim and decoding them would be
// dead code. Unknown prefixes fall through to a labelled hex dump so a
// future Phase 4 collection does not silently break divergence dumps.
func NewDecodeStore(cdc codec.BinaryCodec) func(kvA, kvB kv.Pair) string {
	return func(kvA, kvB kv.Pair) string {
		switch {

		// === Core inference state ===
		case bytes.HasPrefix(kvA.Key, types.ParticipantsPrefix):
			return decodeProto(cdc, kvA, kvB, &types.Participant{}, &types.Participant{}, "Participant")
		case bytes.HasPrefix(kvA.Key, types.InferencesPrefix):
			return decodeProto(cdc, kvA, kvB, &types.Inference{}, &types.Inference{}, "Inference")
		case bytes.HasPrefix(kvA.Key, types.InferenceTimeoutPrefix):
			return decodeProto(cdc, kvA, kvB, &types.InferenceTimeout{}, &types.InferenceTimeout{}, "InferenceTimeout")
		case bytes.HasPrefix(kvA.Key, types.InferenceValidationDetailsPrefix):
			return decodeProto(cdc, kvA, kvB, &types.InferenceValidationDetails{}, &types.InferenceValidationDetails{}, "InferenceValidationDetails")
		case bytes.HasPrefix(kvA.Key, types.ModelsPrefix):
			return decodeProto(cdc, kvA, kvB, &types.Model{}, &types.Model{}, "Model")

		// === Epoch state ===
		case bytes.HasPrefix(kvA.Key, types.EpochsPrefix):
			return decodeProto(cdc, kvA, kvB, &types.Epoch{}, &types.Epoch{}, "Epoch")
		case bytes.HasPrefix(kvA.Key, types.EpochGroupDataPrefix):
			return decodeProto(cdc, kvA, kvB, &types.EpochGroupData{}, &types.EpochGroupData{}, "EpochGroupData")
		case bytes.HasPrefix(kvA.Key, types.EpochGroupValidationsPrefix):
			return decodeProto(cdc, kvA, kvB, &types.EpochGroupValidations{}, &types.EpochGroupValidations{}, "EpochGroupValidations")
		case bytes.HasPrefix(kvA.Key, types.EpochPerformanceSummaryPrefix):
			return decodeProto(cdc, kvA, kvB, &types.EpochPerformanceSummary{}, &types.EpochPerformanceSummary{}, "EpochPerformanceSummary")

		// === PoC v1 (still active via PoCBatch factory) ===
		case bytes.HasPrefix(kvA.Key, types.RandomSeedPrefix):
			return decodeProto(cdc, kvA, kvB, &types.RandomSeed{}, &types.RandomSeed{}, "RandomSeed")
		case bytes.HasPrefix(kvA.Key, types.PoCBatchPrefix):
			return decodeProto(cdc, kvA, kvB, &types.PoCBatch{}, &types.PoCBatch{}, "PoCBatch")
		case bytes.HasPrefix(kvA.Key, types.PoCValidationPref):
			return decodeProto(cdc, kvA, kvB, &types.PoCValidation{}, &types.PoCValidation{}, "PoCValidation")
		case bytes.HasPrefix(kvA.Key, types.PoCValidationSnapshotPrefix):
			return decodeProto(cdc, kvA, kvB, &types.PoCValidationSnapshot{}, &types.PoCValidationSnapshot{}, "PoCValidationSnapshot")

		// === PoC v2 (exercised by 3 V2 factories) ===
		case bytes.HasPrefix(kvA.Key, types.PoCV2StoreCommitPrefix):
			return decodeProto(cdc, kvA, kvB, &types.PoCV2StoreCommit{}, &types.PoCV2StoreCommit{}, "PoCV2StoreCommit")
		case bytes.HasPrefix(kvA.Key, types.PoCValidationV2Prefix):
			return decodeProto(cdc, kvA, kvB, &types.PoCValidationV2{}, &types.PoCValidationV2{}, "PoCValidationV2")
		case bytes.HasPrefix(kvA.Key, types.MLNodeWeightDistributionPrefix):
			return decodeProto(cdc, kvA, kvB, &types.MLNodeWeightDistribution{}, &types.MLNodeWeightDistribution{}, "MLNodeWeightDistribution")

		// === Pricing ===
		case bytes.HasPrefix(kvA.Key, types.ModelLoadRollingWindowPrefix),
			bytes.HasPrefix(kvA.Key, types.ModelInferenceCountRollingWindowPrefix):
			return decodeProto(cdc, kvA, kvB, &types.RollingWindowState{}, &types.RollingWindowState{}, "RollingWindowState")
		case bytes.HasPrefix(kvA.Key, types.UnitOfComputePriceProposalPrefix):
			return decodeProto(cdc, kvA, kvB, &types.UnitOfComputePriceProposal{}, &types.UnitOfComputePriceProposal{}, "UnitOfComputePriceProposal")

		// === Invalidation lifecycle ===
		case bytes.HasPrefix(kvA.Key, types.ExcludedParticipantsPrefix):
			return decodeProto(cdc, kvA, kvB, &types.ExcludedParticipant{}, &types.ExcludedParticipant{}, "ExcludedParticipant")
		case bytes.HasPrefix(kvA.Key, types.ConfirmationPoCEventsPrefix),
			bytes.HasPrefix(kvA.Key, types.ActiveConfirmationPoCEventPrefix):
			return decodeProto(cdc, kvA, kvB, &types.ConfirmationPoCEvent{}, &types.ConfirmationPoCEvent{}, "ConfirmationPoCEvent")

		// === Settlement & rewards ===
		case bytes.HasPrefix(kvA.Key, types.SettleAmountPrefix):
			return decodeProto(cdc, kvA, kvB, &types.SettleAmount{}, &types.SettleAmount{}, "SettleAmount")
		case bytes.HasPrefix(kvA.Key, types.TopMinerPrefix):
			return decodeProto(cdc, kvA, kvB, &types.TopMiner{}, &types.TopMiner{}, "TopMiner")

		// === Upgrade & pruning ===
		case bytes.HasPrefix(kvA.Key, types.PartialUpgradePrefix):
			return decodeProto(cdc, kvA, kvB, &types.PartialUpgrade{}, &types.PartialUpgrade{}, "PartialUpgrade")
		case bytes.HasPrefix(kvA.Key, types.PruningStatePrefix):
			return decodeProto(cdc, kvA, kvB, &types.PruningState{}, &types.PruningState{}, "PruningState")
		case bytes.HasPrefix(kvA.Key, types.PunishmentGraceEpochsPrefix):
			return decodeProto(cdc, kvA, kvB, &types.GraceEpochParams{}, &types.GraceEpochParams{}, "GraceEpochParams")
		case bytes.HasPrefix(kvA.Key, types.PreservedNodesSnapshotPrefix):
			return decodeProto(cdc, kvA, kvB, &types.PreservedNodesSnapshot{}, &types.PreservedNodesSnapshot{}, "PreservedNodesSnapshot")

		// === KeySets (no value bytes) ===
		case bytes.HasPrefix(kvA.Key, types.ActiveInvalidationsPrefix),
			bytes.HasPrefix(kvA.Key, types.ParticipantAllowListPrefix),
			bytes.HasPrefix(kvA.Key, types.EpochGroupValidationEntryPrefix),
			bytes.HasPrefix(kvA.Key, types.ActiveParticipantsCachePrefix),
			bytes.HasPrefix(kvA.Key, types.InferencesToPrunePrefix):
			return fmt.Sprintf("KeySet entry\nA key: %X\nB key: %X\n", kvA.Key, kvB.Key)

		// === Raw uint64 Items / Maps ===
		case bytes.HasPrefix(kvA.Key, types.EffectiveEpochIndexPrefix),
			bytes.HasPrefix(kvA.Key, types.PocV2EnabledEpochPrefix),
			bytes.HasPrefix(kvA.Key, types.DynamicPricingCurrentPrefix),
			bytes.HasPrefix(kvA.Key, types.DynamicPricingCapacityPrefix):
			return fmt.Sprintf("uint64\nA: %d\nB: %d\n",
				binary.BigEndian.Uint64(kvA.Value), binary.BigEndian.Uint64(kvB.Value))
		case bytes.HasPrefix(kvA.Key, types.LastUpgradeHeightPrefix):
			return fmt.Sprintf("int64\nA: %d\nB: %d\n",
				int64(binary.BigEndian.Uint64(kvA.Value)), int64(binary.BigEndian.Uint64(kvB.Value)))

		// === Legacy params (raw store.Set with string key, not collections) ===
		case bytes.Equal(kvA.Key, types.ParamsKey):
			return decodeProto(cdc, kvA, kvB, &types.Params{}, &types.Params{}, "Params")

		default:
			return fmt.Sprintf("unhandled inference prefix\nA key: %X => %X\nB key: %X => %X\n",
				kvA.Key, kvA.Value, kvB.Key, kvB.Value)
		}
	}
}

// decodeProto unmarshals both halves of the kv pair into the supplied proto
// targets and returns a side-by-side dump. The two distinct pointer args
// avoid aliasing across kvA and kvB.
func decodeProto(cdc codec.BinaryCodec, kvA, kvB kv.Pair, a, b proto.Message, label string) string {
	cdc.MustUnmarshal(kvA.Value, a)
	cdc.MustUnmarshal(kvB.Value, b)
	return fmt.Sprintf("%s\nA: %v\nB: %v\n", label, a, b)
}
