package types

import (
	"bytes"
	"sort"
)

// StatePrefix describes one logical group of keys stored under the inference
// module KV store. It maps a human-readable name to the raw byte prefix the
// keys live under so offline tooling (see the `inferenced state-stats`
// command) can attribute on-disk state size back to the feature that produced
// it.
//
// Legacy marks prefixes whose data is no longer read by live code and is only
// kept around for upgrade-time cleanup (see app/upgrades/v0_2_14). These are
// the prime candidates when answering "what can we remove to shrink state".
type StatePrefix struct {
	Name   string
	Bytes  []byte
	Legacy bool
}

// StatePrefixCatalog returns the full list of known inference module store
// prefixes. It is the single source of truth that pairs every prefix declared
// in keys.go with a readable label; keep it in sync when adding or retiring a
// prefix.
//
// Both collection prefixes (single-byte, from collections.NewPrefix) and the
// legacy raw string-key prefixes are included so a state dump can be fully
// attributed rather than leaving large "unknown" buckets.
func StatePrefixCatalog() []StatePrefix {
	cat := []StatePrefix{
		{Name: "Participants", Bytes: []byte(ParticipantsPrefix)},
		{Name: "RandomSeed", Bytes: []byte(RandomSeedPrefix)},
		{Name: "PoCBatch", Bytes: []byte(PoCBatchPrefix)},
		{Name: "PoCValidation", Bytes: []byte(PoCValidationPref)},
		{Name: "DynamicPricingCurrent", Bytes: []byte(DynamicPricingCurrentPrefix)},
		{Name: "DynamicPricingCapacity", Bytes: []byte(DynamicPricingCapacityPrefix)},
		{Name: "Models", Bytes: []byte(ModelsPrefix)},
		{Name: "InferenceTimeout", Bytes: []byte(InferenceTimeoutPrefix)},
		{Name: "InferenceValidationDetails", Bytes: []byte(InferenceValidationDetailsPrefix)},
		{Name: "UnitOfComputePriceProposal", Bytes: []byte(UnitOfComputePriceProposalPrefix)},
		{Name: "EpochGroupData", Bytes: []byte(EpochGroupDataPrefix)},
		{Name: "Epochs", Bytes: []byte(EpochsPrefix)},
		{Name: "EffectiveEpochIndex", Bytes: []byte(EffectiveEpochIndexPrefix)},
		{Name: "EpochGroupValidations", Bytes: []byte(EpochGroupValidationsPrefix), Legacy: true},
		{Name: "Inferences", Bytes: []byte(InferencesPrefix)},
		{Name: "SettleAmount", Bytes: []byte(SettleAmountPrefix)},
		{Name: "TopMiner", Bytes: []byte(TopMinerPrefix), Legacy: true},
		{Name: "PartialUpgrade", Bytes: []byte(PartialUpgradePrefix)},
		{Name: "EpochPerformanceSummary", Bytes: []byte(EpochPerformanceSummaryPrefix)},
		{Name: "TrainingExecAllowList", Bytes: []byte(TrainingExecAllowListPrefix), Legacy: true},
		{Name: "TrainingStartAllowList", Bytes: []byte(TrainingStartAllowListPrefix), Legacy: true},
		{Name: "PruningState", Bytes: []byte(PruningStatePrefix)},
		{Name: "InferencesToPrune", Bytes: []byte(InferencesToPrunePrefix)},
		{Name: "ActiveInvalidations", Bytes: []byte(ActiveInvalidationsPrefix)},
		{Name: "ExcludedParticipants", Bytes: []byte(ExcludedParticipantsPrefix)},
		{Name: "ConfirmationPoCEvents", Bytes: []byte(ConfirmationPoCEventsPrefix)},
		{Name: "ActiveConfirmationPoCEvent", Bytes: []byte(ActiveConfirmationPoCEventPrefix)},
		{Name: "LastUpgradeHeight", Bytes: []byte(LastUpgradeHeightPrefix)},
		{Name: "BridgeContractAddresses", Bytes: []byte(BridgeContractAddressesPrefix)},
		{Name: "BridgeTransactions", Bytes: []byte(BridgeTransactionsPrefix)},
		{Name: "WrappedTokenCodeID", Bytes: []byte(WrappedTokenCodeIDPrefix)},
		{Name: "WrappedTokenMetadata", Bytes: []byte(WrappedTokenMetadataPrefix)},
		{Name: "WrappedTokenContracts", Bytes: []byte(WrappedTokenContractsPrefix)},
		{Name: "WrappedContractReverseIndex", Bytes: []byte(WrappedContractReverseIndexPrefix)},
		{Name: "LiquidityPool", Bytes: []byte(LiquidityPoolPrefix)},
		{Name: "LiquidityPoolApprovedTokens", Bytes: []byte(LiquidityPoolApprovedTokensPrefix)},
		{Name: "ParticipantAllowList", Bytes: []byte(ParticipantAllowListPrefix)},
		{Name: "LegacyPoCValidationV2", Bytes: []byte(LegacyPoCValidationV2Prefix), Legacy: true},
		{Name: "LegacyPoCV2StoreCommit", Bytes: []byte(LegacyPoCV2StoreCommitPrefix), Legacy: true},
		{Name: "LegacyMLNodeWeightDistribution", Bytes: []byte(LegacyMLNodeWeightDistributionPrefix), Legacy: true},
		{Name: "PocV2EnabledEpoch", Bytes: []byte(PocV2EnabledEpochPrefix)},
		{Name: "PoCValidationSnapshot", Bytes: []byte(PoCValidationSnapshotPrefix)},
		{Name: "PunishmentGraceEpochs", Bytes: []byte(PunishmentGraceEpochsPrefix)},
		{Name: "ActiveParticipantsCache", Bytes: []byte(ActiveParticipantsCachePrefix)},
		{Name: "ModelLoadRollingWindow", Bytes: []byte(ModelLoadRollingWindowPrefix)},
		{Name: "ModelInferenceCountRollingWindow", Bytes: []byte(ModelInferenceCountRollingWindowPrefix)},
		{Name: "EpochGroupValidationEntry", Bytes: []byte(EpochGroupValidationEntryPrefix)},
		{Name: "DevshardEscrows", Bytes: []byte(DevshardEscrowsPrefix)},
		{Name: "DevshardEscrowCounter", Bytes: []byte(DevshardEscrowCounterPrefix)},
		{Name: "DevshardEscrowEpochCount", Bytes: []byte(DevshardEscrowEpochCountPrefix)},
		{Name: "DevshardHostEpochStats", Bytes: []byte(DevshardHostEpochStatsPrefix)},
		{Name: "DevshardEscrowsByEpoch", Bytes: []byte(DevshardEscrowsByEpochPrefix)},
		{Name: "PoCDelegation", Bytes: []byte(PoCDelegationPrefix)},
		{Name: "PoCRefusal", Bytes: []byte(PoCRefusalPrefix)},
		{Name: "PoCDirectIntent", Bytes: []byte(PoCDirectIntentPrefix)},
		{Name: "DelegationSnapshot", Bytes: []byte(DelegationSnapshotPrefix)},
		{Name: "BootstrapDelegationSnapshot", Bytes: []byte(BootstrapDelegationSnapshotPrefix)},
		{Name: "PoCValidationV2", Bytes: []byte(PoCValidationV2Prefix)},
		{Name: "PoCV2StoreCommit", Bytes: []byte(PoCV2StoreCommitPrefix)},
		{Name: "MLNodeWeightDistribution", Bytes: []byte(MLNodeWeightDistributionPrefix)},
		{Name: "BridgeMintRefunds", Bytes: []byte(BridgeMintRefundsPrefix)},
		{Name: "BridgeWithdrawalRefunds", Bytes: []byte(BridgeWithdrawalRefundsPrefix)},
		{Name: "BridgeWithdrawalTokenRefs", Bytes: []byte(BridgeWithdrawalTokenRefsPrefix)},
		{Name: "BridgeTransactionValidators", Bytes: []byte(BridgeTransactionValidatorsPrefix)},
		{Name: "PreservedNodesSnapshot", Bytes: []byte(PreservedNodesSnapshotPrefix)},

		// Params and other raw string-key state.
		{Name: "Params", Bytes: ParamsKey},
		{Name: "TokenomicsData", Bytes: []byte(TokenomicsDataKey)},
		{Name: "GenesisOnlyData", Bytes: []byte(GenesisOnlyDataKey)},
		{Name: "MLNodeVersion", Bytes: []byte(MLNodeVersionKey)},

		// Developer inference statistics indexes. String literals mirror the
		// constants in keeper/developer_stats_store.go (referenced here directly
		// to avoid a types->keeper import cycle). These indexes are written per
		// inference and are NOT pruned, so they dominate state size on busy
		// chains; keep them labeled so state-stats attributes them precisely.
		{Name: "DeveloperStatsByInference", Bytes: []byte("stats/developers/inference")},
		{Name: "DeveloperStatsByEpoch", Bytes: []byte("stats/developers/epoch")},
		{Name: "DeveloperStatsByTime", Bytes: []byte("stats/developers/time")},
		{Name: "DeveloperStatsByModel", Bytes: []byte("stats/model/inference")},

		// Hardware node registrations. Literal mirrors
		// keeper.HardwareNodesKeysPrefix (same import-cycle reason as above).
		{Name: "HardwareNodes", Bytes: []byte("HardwareNodesValues/value/")},

		// Removed training feature: raw string-key prefixes.
		{Name: "TrainingTask", Bytes: []byte(TrainingTaskKeyPrefix), Legacy: true},
		{Name: "TrainingTaskSequence", Bytes: []byte(TrainingTaskSequenceKey), Legacy: true},
		{Name: "QueuedTrainingTask", Bytes: []byte(QueuedTrainingTaskKeyPrefix), Legacy: true},
		{Name: "InProgressTrainingTask", Bytes: []byte(InProgressTrainingTaskKeyPrefix), Legacy: true},
		{Name: "TrainingTaskKvRecord", Bytes: []byte(TrainingTaskKvRecordKeyPrefix), Legacy: true},
		{Name: "TrainingTaskSync", Bytes: []byte("TrainingTask/sync/"), Legacy: true},

		// Legacy CosmWasm bridge artifacts removed in v0.2.5
		// (see keeper/migrations_bridge.go). Kept here so a state dump can flag
		// them if any chain still carries the bytes.
		{Name: "LegacyTokenCodeID", Bytes: []byte("TokenCodeID"), Legacy: true},
		{Name: "LegacyContractsParams", Bytes: []byte("contracts_params"), Legacy: true},
	}

	// Longest prefix first so MatchStatePrefix attributes a key to the most
	// specific bucket (e.g. "TrainingTask/sync/" before "TrainingTask/value/").
	sort.SliceStable(cat, func(i, j int) bool {
		return len(cat[i].Bytes) > len(cat[j].Bytes)
	})
	return cat
}

// MatchStatePrefix returns the catalog entry whose byte prefix is the longest
// match for key, or nil if none matches. The catalog passed in must already be
// sorted longest-first (StatePrefixCatalog guarantees this).
func MatchStatePrefix(catalog []StatePrefix, key []byte) *StatePrefix {
	for i := range catalog {
		if bytes.HasPrefix(key, catalog[i].Bytes) {
			return &catalog[i]
		}
	}
	return nil
}
