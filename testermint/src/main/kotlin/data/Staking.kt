package com.productscience.data

import java.math.BigInteger
import java.time.Instant
import java.time.LocalTime

data class ValidatorsResponse(
    val validators: List<StakeValidator>,
    val pagination: Pagination,
)

data class StakeValidator(
    val operatorAddress: String,
    val consensusPubkey: ConsensusPubkey,
    val status: Any,  // Changed from Int to Any to handle both Int and String
    val tokens: Long,
    val delegatorShares: Double,
    val description: ValidatorDescription,
    val unbondingTime: Instant,
    val commission: Commission,
    val minSelfDelegation: String
) {
    // Helper function to get status as integer, handling Int, Double, and String values
    fun getStatusAsInt(): Int {
        return when (status) {
            is Int -> status
            is Double -> status.toInt()
            is Float -> status.toInt()
            is Number -> status.toInt()
            is String -> {
                when (status) {
                    "BOND_STATUS_UNSPECIFIED" -> 0
                    "BOND_STATUS_UNBONDED" -> 1
                    "BOND_STATUS_UNBONDING" -> 2
                    "BOND_STATUS_BONDED" -> 3
                    else -> {
                        // Try to parse as number if it's a numeric string
                        status.toIntOrNull() ?: 0
                    }
                }
            }
            else -> 0
        }
    }
}

enum class StakeValidatorStatus(val value: Int) {
    UNBONDING(2),
    BONDED(3),
}

data class ConsensusPubkey(
    val type: String,
    val value: String
)

data class ValidatorDescription(
    val moniker: String,
    val details: String? = null
)

data class Commission(
    val commissionRates: CommissionRates,
    val updateTime: Instant
)

data class CommissionRates(
    val rate: Double,
    val maxRate: Double,
    val maxChangeRate: Double
)

data class CometValidatorsResponse(
    val blockHeight: String,
    val validators: List<CometValidator>,
    val pagination: CometPagination
)

data class CometValidator(
    val address: String,
    val pubKey: CometPubKey,
    val votingPower: String,
    val proposerPriority: String
)

data class CometPubKey(
    val type: String,
    val key: String
)

data class CometPagination(
    val nextKey: String?,
    val total: String
)

