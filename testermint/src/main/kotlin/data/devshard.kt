package com.productscience.data

import com.google.gson.annotations.SerializedName

data class DevshardEscrowResponse(
    val escrow: DevshardEscrow?,
    val found: Boolean
)

data class DevshardEscrow(
    val id: String,
    val creator: String,
    val amount: String,
    val slots: List<String>,
    @SerializedName("model_id")
    val modelId: String,
    @SerializedName("epoch_index")
    val epochIndex: String,
    @SerializedName("app_hash")
    val appHash: String,
    val settled: Boolean
)

data class DevshardMempoolResponse(
    val txs: List<Any>?
)

data class DevshardProxyStatus(
    @SerializedName("escrow_id")
    val escrowId: String,
    val nonce: Long,
    val phase: String,
    val balance: Long,
    val config: DevshardSessionConfig
)

data class DevshardSessionConfig(
    @SerializedName("refusal_timeout")
    val refusalTimeout: Long,
    @SerializedName("execution_timeout")
    val executionTimeout: Long,
    @SerializedName("token_price")
    val tokenPrice: Long,
    @SerializedName("create_devshard_fee")
    val createDevshardFee: Long,
    @SerializedName("fee_per_nonce")
    val feePerNonce: Long,
    @SerializedName("vote_threshold")
    val voteThreshold: Int,
    @SerializedName("validation_rate")
    val validationRate: Int
)

data class DevshardSettlementData(
    @SerializedName("escrow_id")
    val escrowId: String,
    val version: String,
    @SerializedName("state_root")
    val stateRoot: String,
    val nonce: Long,
    @SerializedName("rest_hash")
    val restHash: String,
    val fees: Long,
    @SerializedName("host_stats")
    val hostStats: List<DevshardHostStatsEntry>,
    val signatures: List<DevshardSlotSignatureEntry>
)

data class DevshardHostStatsEntry(
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

data class DevshardSlotSignatureEntry(
    @SerializedName("slot_id")
    val slotId: Int,
    val signature: String
)

data class DevshardInferencePayload(
    val status: DevshardInferenceStatus,
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
    val statusEnum: DevshardInferenceStatus
        get() = status
}

enum class DevshardInferenceStatus(val value: Int) {
    PENDING(0),
    STARTED(1),
    FINISHED(2),
    CHALLENGED(3),
    VALIDATED(4),
    INVALIDATED(5),
    TIMED_OUT(6),
    UNSPECIFIED(7);

    companion object {
        fun fromValue(value: Int): DevshardInferenceStatus =
            values().find { it.value == value } ?: UNSPECIFIED

        fun fromAny(value: Any?): DevshardInferenceStatus {
            return when (value) {
                is Number -> fromValue(value.toInt())
                // /v1/state serializes status as a lowercase name (e.g. "finished",
                // "timed_out"); /v1/inference used the numeric code. Accept both.
                is String -> values().find { it.name.equals(value, ignoreCase = true) }
                    ?: value.toIntOrNull()?.let { fromValue(it) }
                    ?: UNSPECIFIED
                else -> UNSPECIFIED
            }
        }
    }
}

data class DevshardChallengeReceiptRequest(
    @SerializedName("inference_id")
    val inferenceID: Long,
    val payload: DevshardPayloadJSON,
    val diffs: List<DevshardDiffJSON>,
)

data class DevshardChallengeReceiptResponse(
    val receipt: List<String>,
)

data class DevshardDiffJSON(
    val nonce: Long,
    val txs: String,
    @SerializedName("user_sig")
    val userSig: String,
    @SerializedName("post_state_root")
    val postStateRoot: String,
)

data class DevshardPayloadJSON(
    val prompt: String,
    val model: String,
    @SerializedName("input_length")
    val inputLength: Long,
    @SerializedName("max_tokens")
    val maxTokens: Long,
    @SerializedName("started_at")
    val startedAt: Long,
)
