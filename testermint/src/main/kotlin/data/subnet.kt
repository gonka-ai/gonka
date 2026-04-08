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

data class SubnetInferencePayload(
    val status: SubnetInferenceStatus,
    @SerializedName("executor_slot")
    val executorSlot: Int,
    val model: String,
    @SerializedName("prompt_hash")
    val promptHash: String,
    @SerializedName("response_hash")
    val responseHash: String?,
    @SerializedName("input_length")
    val inputLength: Long,
    @SerializedName("max_tokens")
    val maxTokens: Long,
    @SerializedName("input_tokens")
    val inputTokens: Long?,
    @SerializedName("output_tokens")
    val outputTokens: Long?,
    @SerializedName("reserved_cost")
    val reservedCost: Long,
    @SerializedName("actual_cost")
    val actualCost: Long?,
    @SerializedName("started_at")
    val startedAt: Long,
    @SerializedName("confirmed_at")
    val confirmedAt: Long?,
    @SerializedName("votes_valid")
    val votesValid: Int?,
    @SerializedName("votes_invalid")
    val votesInvalid: Int?,
    @SerializedName("validated_by")
    val validatedBy: Array<Long>?,
) {
    val statusEnum: SubnetInferenceStatus
        get() = status
}

enum class SubnetInferenceStatus(val value: Int) {
    PENDING(0),
    STARTED(1),
    FINISHED(2),
    CHALLENGED(3),
    VALIDATED(4),
    INVALIDATED(5),
    TIMED_OUT(6),
    UNSPECIFIED(7);

    companion object {
        fun fromValue(value: Int): SubnetInferenceStatus =
            values().find { it.value == value } ?: UNSPECIFIED

        fun fromAny(value: Any?): SubnetInferenceStatus {
            return when (value) {
                is Number -> fromValue(value.toInt())
                else -> UNSPECIFIED
            }
        }
    }
}

data class SubnetChallengeReceiptRequest(
    @SerializedName("inference_id")
    val inferenceID: Long,
    val payload: SubnetPayloadJSON,
    val diffs: List<SubnetDiffJSON>,
)

data class SubnetChallengeReceiptResponse(
    val receipt: List<String>,
)

data class SubnetDiffJSON(
    val nonce: Long,
    val txs: String,
    @SerializedName("user_sig")
    val userSig: String,
    @SerializedName("post_state_root")
    val postStateRoot: String,
)

data class SubnetPayloadJSON(
    val prompt: String,
    val model: String,
    @SerializedName("input_length")
    val inputLength: Long,
    @SerializedName("max_tokens")
    val maxTokens: Long,
    @SerializedName("started_at")
    val startedAt: Long,
)
