package com.productscience.data

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
)

data class InferenceParamsWrapper(
    val params: InferenceParams,
)

data class InferenceParams(
    val epochParams: EpochParams,
    val validationParams: ValidationParams,
    val pocParams: PocParams,
    val tokenomicsParams: TokenomicsParams,
)

data class TokenomicsParams(
    val subsidyReductionInterval: Decimal,
    val subsidyReductionAmount: Decimal,
    val currentSubsidyPercentage: Decimal,
    val topRewardAllowedFailure: Decimal,
    val topMinerPocQualification: Long,
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
    val inferencePruningEpochThreshold: Long
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
)

data class PocParams(
    val defaultDifficulty: Int,
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
)
