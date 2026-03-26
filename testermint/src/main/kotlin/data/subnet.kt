package com.productscience.data

import com.google.gson.annotations.SerializedName

data class SubnetEscrowResponse(
    val escrow: SubnetEscrow?,
    val found: Boolean
)

data class SubnetEscrow(
    val id: String,
    val creator: String,
    val amount: String,
    val slots: List<String>,
    @SerializedName("epoch_index")
    val epochIndex: String,
    @SerializedName("app_hash")
    val appHash: String,
    val settled: Boolean
)

data class SubnetMempoolResponse(
    val txs: List<Any>?
)

data class SubnetProxyStatus(
    @SerializedName("escrow_id")
    val escrowId: String,
    val nonce: Long,
    val phase: String,
    val balance: Long,
    val config: SubnetSessionConfig
)

data class SubnetSessionConfig(
    @SerializedName("refusal_timeout")
    val refusalTimeout: Long,
    @SerializedName("execution_timeout")
    val executionTimeout: Long,
    @SerializedName("token_price")
    val tokenPrice: Long,
    @SerializedName("create_subnet_fee")
    val createSubnetFee: Long,
    @SerializedName("fee_per_nonce")
    val feePerNonce: Long,
    @SerializedName("vote_threshold")
    val voteThreshold: Int,
    @SerializedName("validation_rate")
    val validationRate: Int
)

data class SubnetSettlementData(
    @SerializedName("escrow_id")
    val escrowId: String,
    @SerializedName("state_root")
    val stateRoot: String,
    val nonce: Long,
    @SerializedName("rest_hash")
    val restHash: String,
    val fees: Long,
    @SerializedName("host_stats")
    val hostStats: List<SubnetHostStatsEntry>,
    val signatures: List<SubnetSlotSignatureEntry>
)

data class SubnetHostStatsEntry(
    @SerializedName("slot_id")
    val slotId: Int,
    val missed: Int,
    val invalid: Int,
    val cost: Long,
    @SerializedName("required_validations")
    val requiredValidations: Int,
    @SerializedName("completed_validations")
    val completedValidations: Int
)

data class SubnetSlotSignatureEntry(
    @SerializedName("slot_id")
    val slotId: Int,
    val signature: String
)
