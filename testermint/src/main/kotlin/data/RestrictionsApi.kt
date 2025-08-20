package com.productscience.data

import com.google.gson.annotations.SerializedName

// Query DTOs
data class TransferRestrictionStatusDto(
    @SerializedName("is_active") val isActive: Boolean,
    @SerializedName("restriction_end_block") val restrictionEndBlock: Long,
    @SerializedName("current_block_height") val currentBlockHeight: Long,
    @SerializedName("remaining_blocks") val remainingBlocks: Long,
)

data class TransferExemptionsDto(
    val exemptions: List<EmergencyTransferExemption>? = emptyList(),
) {
    fun getExemptionsSafe(): List<EmergencyTransferExemption> = exemptions ?: emptyList()
}

data class ExemptionUsageDto(
    @SerializedName("usage_entries") val usageEntries: List<ExemptionUsageEntry> = emptyList(),
)

// Request DTOs
data class UpdateRestrictionsParamsDto(
    @SerializedName("restriction_end_block") val restrictionEndBlock: ULong,
    @SerializedName("emergency_transfer_exemptions") val emergencyTransferExemptions: List<EmergencyTransferExemption>,
    @SerializedName("exemption_usage_tracking") val exemptionUsageTracking: List<ExemptionUsageEntry>,
)

data class EmergencyTransferDto(
    @SerializedName("exemption_id") val exemptionId: String,
    @SerializedName("from_address") val fromAddress: String,
    @SerializedName("to_address") val toAddress: String,
    val amount: String,
    val denom: String,
)


