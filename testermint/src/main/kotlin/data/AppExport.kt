package com.productscience.data

import com.google.gson.annotations.SerializedName
import java.math.BigDecimal
import java.time.Duration
import java.time.Instant

// We can add any internal state that we need to verify here,
// but let's only add what we need
data class AppExport(
    val appName: String,
    val appVersion: String,
    val genesisTime: Instant?,
    val initialHeight: Int,
    val appHash: String,
    val appState: AppState,
)

data class AppState(
    val bank: BankState,
    val gov: GovState,
    val inference: InferenceState,
    val restrictions: RestrictionsState,
)

data class InferenceState(
    val params: InferenceParams,
    val genesisOnlyParams: GenesisOnlyParams,
    val tokenomicsData: TokenomicsData,
    val modelList: List<ModelListItem>,
)

data class TokenomicsData(
    val totalFees: Long,
    val totalSubsidies: Long,
    val totalRefunded: Long,
    val totalBurned: Long,
)

data class GenesisOnlyParams(
    val totalSupply: Long,
    val originatorSupply: Long,
    val topRewardAmount: Long,
    val standardRewardAmount: Long,
    val preProgrammedSaleAmount: Long,
    val topRewards: Int,
    val supplyDenom: String,
    val topRewardPeriod: Long,
    val topRewardPayouts: Long,
    val topRewardPayoutsPerMiner: Long,
    val topRewardMaxDuration: Long,
    val maxIndividualPowerPercentage: Decimal?,
    val genesisGuardianEnabled: Boolean,
    val genesisGuardianNetworkMaturityThreshold: Long,
    val genesisGuardianMultiplier: Decimal?,
    val genesisGuardianAddresses: List<String>,
)

data class InferenceParamsWrapper(
    val params: InferenceParams,
)

data class InferenceParams(
    val epochParams: EpochParams,
    val validationParams: ValidationParams,
    val pocParams: PocParams,
    val tokenomicsParams: TokenomicsParams,
    val collateralParams: CollateralParams,
    @SerializedName("bitcoin_reward_params")
    val bitcoinRewardParams: BitcoinRewardParams? = null,
    @SerializedName("dynamic_pricing_params")
    val dynamicPricingParams: DynamicPricingParams? = null,
    @SerializedName("bandwidth_limits_params")
    val bandwidthLimitsParams: BandwidthLimitsParams? = null,
    @SerializedName("confirmation_poc_params")
    val confirmationPocParams: ConfirmationPoCParams? = null,
)

data class TokenomicsParams(
    val subsidyReductionInterval: Decimal,
    val subsidyReductionAmount: Decimal,
    val currentSubsidyPercentage: Decimal,
    val topRewardAllowedFailure: Decimal,
    val topMinerPocQualification: Long,
    @SerializedName("work_vesting_period")
    val workVestingPeriod: Long? = null,
    @SerializedName("reward_vesting_period") 
    val rewardVestingPeriod: Long? = null,
    @SerializedName("top_miner_vesting_period")
    val topMinerVestingPeriod: Long? = null,
)

data class BitcoinRewardParams(
    @SerializedName("use_bitcoin_rewards")
    val useBitcoinRewards: Boolean,
    @SerializedName("initial_epoch_reward")
    val initialEpochReward: Long,
    @SerializedName("decay_rate")
    val decayRate: Decimal,
    @SerializedName("genesis_epoch")
    val genesisEpoch: Long,
    @SerializedName("utilization_bonus_factor")
    val utilizationBonusFactor: Decimal,
    @SerializedName("full_coverage_bonus_factor") 
    val fullCoverageBonusFactor: Decimal,
    @SerializedName("partial_coverage_bonus_factor")
    val partialCoverageBonusFactor: Decimal
)

data class DynamicPricingParams(
    @SerializedName("stability_zone_lower_bound")
    val stabilityZoneLowerBound: Decimal,
    @SerializedName("stability_zone_upper_bound")
    val stabilityZoneUpperBound: Decimal,
    @SerializedName("price_elasticity")
    val priceElasticity: Decimal,
    @SerializedName("utilization_window_duration")
    val utilizationWindowDuration: Long,
    @SerializedName("min_per_token_price")
    val minPerTokenPrice: Long,
    @SerializedName("base_per_token_price")
    val basePerTokenPrice: Long,
    @SerializedName("grace_period_end_epoch")
    val gracePeriodEndEpoch: Long,
    @SerializedName("grace_period_per_token_price")
    val gracePeriodPerTokenPrice: Long,
)

data class EpochParams(
    val epochLength: Long,
    val epochMultiplier: Int,
    val epochShift: Int,
    val defaultUnitOfComputePrice: Long,
    val pocStageDuration: Long,
    val pocExchangeDuration: Long,
    val pocValidationDelay: Long,
    val pocValidationDuration: Long,
    val setNewValidatorsDelay: Long,
    val inferenceValidationCutoff: Long,
    val inferencePruningEpochThreshold: Long,
    val inferencePruningMax: Long,
    val pocPruningMax: Long,
    @SerializedName("poc_slot_allocation")
    val pocSlotAllocation: Decimal?,
)

data class Decimal(
    val value: Long,
    val exponent: Int,
) {
    fun toDouble(): Double {
        return value * Math.pow(10.0, exponent.toDouble())
    }

    override fun equals(other: Any?): Boolean {
        return this.toDouble() == (other as? Decimal)?.toDouble()
    }

    companion object {
        private fun fromNumber(number: Number): Decimal {
            val strValue = number.toString().replace(".0$".toRegex(), "")
            val decimalPos = strValue.indexOf('.')
            val exponent = if (decimalPos != -1) strValue.length - decimalPos - 1 else 0
            val scaleFactor = Math.pow(10.0, exponent.toDouble())
            val longValue = (number.toDouble() * scaleFactor).toLong()
            return Decimal(longValue, -exponent)
        }

        fun fromFloat(float: Float): Decimal = fromNumber(float)

        fun fromDouble(double: Double): Decimal = fromNumber(double)
    }
}

data class ValidationParams(
    val falsePositiveRate: Decimal,
    val minRampUpMeasurements: Int,
    val passValue: Decimal,
    val minValidationAverage: Decimal,
    val maxValidationAverage: Decimal,
    val expirationBlocks: Long,
    val epochsToMax: Long,
    val fullValidationTrafficCutoff: Long,
    val minValidationHalfway: Decimal,
    val minValidationTrafficCutoff: Long,
    val missPercentageCutoff: Decimal,
    val missRequestsPenalty: Decimal,
    val timestampExpiration: Long,
    val timestampAdvance: Long,
    @SerializedName("estimated_limits_per_block_kb")
    val estimatedLimitsPerBlockKb: Long,
    @SerializedName("invalid_reputation_preserve")
    val invalidReputationPreserve: Decimal?,
    @SerializedName("bad_participant_invalidation_rate")
    val badParticipantInvalidationRate: Decimal?,
    @SerializedName("invalidation_h_threshold")
    val invalidationHThreshold: Decimal?,
    @SerializedName("downtime_good_percentage")
    val downtimeGoodPercentage: Decimal?,
    @SerializedName("downtime_bad_percentage")
    val downtimeBadPercentage: Decimal?,
    @SerializedName("downtime_h_threshold")
    val downtimeHThreshold: Decimal?,
    @SerializedName("downtime_reputation_preserve")
    val downtimeReputationPreserve: Decimal?,
    @SerializedName("quick_failure_threshold")
    val quickFailureThreshold: Decimal?,
    @SerializedName("binom_test_p0")
    val binomTestP0: Decimal?,
)

data class BandwidthLimitsParams(
    @SerializedName("estimated_limits_per_block_kb")
    val estimatedLimitsPerBlockKb: Long,
    @SerializedName("kb_per_input_token")
    val kbPerInputToken: Decimal,
    @SerializedName("kb_per_output_token")
    val kbPerOutputToken: Decimal,
    @SerializedName("invalidations_limit")
    val invalidationsLimit: Long,
    @SerializedName("invalidations_sample_period")
    val invalidationsSamplePeriod: Long,
    @SerializedName("invalidations_limit_curve")
    val invalidationsLimitCurve: Long,
    @SerializedName("minimum_concurrent_invalidations")
    val minimumConcurrentInvalidations: Long,
    @SerializedName("max_inferences_per_block")
    val maxInferencesPerBlock: Long? = null,
)

data class ConfirmationPoCParams(
    @SerializedName("expected_confirmations_per_epoch")
    val expectedConfirmationsPerEpoch: Long = 0,
    @SerializedName("alpha_threshold")
    val alphaThreshold: Decimal = Decimal(70, -2),  // 0.70
    @SerializedName("slash_fraction")
    val slashFraction: Decimal = Decimal(10, -2),  // 0.10
    @SerializedName("upgrade_protection_window")
    val upgradeProtectionWindow: Long = 2,  // Default: 500 blocks
)

data class PocParams(
    val defaultDifficulty: Int,
    val validationSampleSize: Int,
    @SerializedName("poc_data_pruning_epoch_threshold")
    val pocDataPruningEpochThreshold: Long,
    @SerializedName("weight_scale_factor")
    val weightScaleFactor: Decimal? = null,
    @SerializedName("model_params")
    val modelParams: PoCModelParams? = null,
)

data class PoCModelParams(
    val dim: Int,
    @SerializedName("n_layers")
    val nLayers: Int,
    @SerializedName("n_heads")
    val nHeads: Int,
    @SerializedName("n_kv_heads")
    val nKvHeads: Int,
    @SerializedName("vocab_size")
    val vocabSize: Int,
    @SerializedName("ffn_dim_multiplier")
    val ffnDimMultiplier: Decimal,
    @SerializedName("multiple_of")
    val multipleOf: Int,
    @SerializedName("norm_eps")
    val normEps: Decimal,
    @SerializedName("rope_theta")
    val ropeTheta: Int,
    @SerializedName("use_scaled_rope")
    val useScaledRope: Boolean,
    @SerializedName("seq_len")
    val seqLen: Int,
    @SerializedName("r_target")
    val rTarget: Decimal,
)

data class GovState(
    val params: GovParams,
)

data class GovParams(
    val minDeposit: List<Coin>,
    val maxDepositPeriod: Duration,
    val votingPeriod: Duration,
    val quorum: Double,
    val threshold: Double,
    val vetoThreshold: Double,
    val minInitialDepositRatio: Double,
    val proposalCancelRatio: Double,
    val proposalCancelDest: String,
    val expeditedVotingPeriod: Duration,
    val expeditedThreshold: Double,
    val expeditedMinDeposit: List<Coin>,
    val burnVoteQuorum: Boolean,
    val burnProposalDepositPrevote: Boolean,
    val burnVoteVeto: Boolean,
    val minDepositRatio: Double,
)

data class BankState(
    val balances: List<BankBalance>,
    val supply: List<Coin>,
    val denomMetadata: List<DenomMetadata>,
)

data class BankBalance(
    val address: String,
    val coins: List<Coin>,
)

data class Coin(
    val denom: String,
    val amount: Long,
)

data class DenomMetadata(
    val description: String,
    val base: String,
    val display: String,
    val name: String,
    val symbol: String,
    val denomUnits: List<DenomUnit>,
) {
    fun convertAmount(
        amount: Long,
        fromDenom: String,
        toDenom: String? = null,
    ): Long {
        val finalToDenom = toDenom ?: this.base
        val fromUnit = this.denomUnits.find { it.denom == fromDenom }
            ?: throw IllegalArgumentException("Invalid 'from' denomination: $fromDenom")
        val toUnit = this.denomUnits.find { it.denom == finalToDenom }
            ?: throw IllegalArgumentException("Invalid 'to' denomination: $finalToDenom")

        val exponentDiff = fromUnit.exponent - toUnit.exponent
        val conversionFactor = BigDecimal.TEN.pow(exponentDiff)
        return conversionFactor.multiply(BigDecimal(amount)).toLong()
    }

}

data class DenomUnit(
    val denom: String,
    val exponent: Int,
)

data class ModelListItem(
    val proposedBy: String,
    val id: String,
    val unitsOfComputePerToken: String,
    val hfRepo: String,
    val hfCommit: String,
    val modelArgs: List<String>,
    val vRam: String,
    val throughputPerNonce: String,
    val validationThreshold: Decimal,
)

// -----------------------
// Restrictions Module (AppState wiring for E2E DSL)
// -----------------------

data class RestrictionsState(
    val params: RestrictionsParams,
)

data class RestrictionsParams(
    @SerializedName("restriction_end_block")
    val restrictionEndBlock: Long,
    @SerializedName("emergency_transfer_exemptions")
    val emergencyTransferExemptions: List<EmergencyTransferExemption> = emptyList(),
    @SerializedName("exemption_usage_tracking")
    val exemptionUsageTracking: List<ExemptionUsageEntry> = emptyList(),
)

data class EmergencyTransferExemption(
    @SerializedName("exemption_id")
    val exemptionId: String,
    @SerializedName("from_address")
    val fromAddress: String,
    @SerializedName("to_address")
    val toAddress: String,
    // String amount for consistency with on-chain proto/json (e.g., "1000")
    @SerializedName("max_amount")
    val maxAmount: String,
    @SerializedName("usage_limit")
    val usageLimit: Long,
    @SerializedName("expiry_block")
    val expiryBlock: Long,
    val justification: String,
)

data class ExemptionUsageEntry(
    @SerializedName("exemption_id")
    val exemptionId: String,
    @SerializedName("account_address")
    val accountAddress: String,
    @SerializedName("usage_count")
    val usageCount: Long,
)