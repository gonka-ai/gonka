package com.productscience.data

data class InferencePayload(
    val index: String,
    val inferenceId: String,
    val promptHash: String,
    val promptPayload: String,  // Adjusted to String
    val responseHash: String?,
    val responsePayload: String?,
    val promptTokenCount: Int?,
    val completionTokenCount: Int?,
    val requestedBy: String?,
    val executedBy: String?,
    val status: Int,
    val startBlockHeight: Long,
    val endBlockHeight: Long?,
    val startBlockTimestamp: Long,
    val endBlockTimestamp: Long?,
    val model: String?,
    val maxTokens: Int,
    val actualCost: Long?,
    val escrowAmount: Long?,
    val assignedTo: String?,
    val validatedBy: List<String> = listOf(),
    val transferredBy: String? = null,
    val requestTimestamp: Long? = null,
    val transferSignature: String? = null,
    val executionSignature: String? = null,
    val perTokenPrice: Long? = null,
) {
    companion object {
        fun empty() = InferencePayload(
            index = "",
            inferenceId = "",
            promptHash = "",
            promptPayload = "",
            responseHash = null,
            responsePayload = null,
            promptTokenCount = null,
            completionTokenCount = null,
            requestedBy = "",
            executedBy = null,
            status = InferenceStatus.STARTED.value,
            startBlockHeight = 0L,
            endBlockHeight = null,
            startBlockTimestamp = 0L,
            endBlockTimestamp = null,
            model = "",
            maxTokens = 0,
            actualCost = null,
            escrowAmount = null,
            assignedTo = null,
            validatedBy = listOf(),
            perTokenPrice = null,
        )

    }

    fun checkComplete(): Boolean =
        !this.requestedBy.isNullOrEmpty() &&
            !this.executedBy.isNullOrEmpty() &&
            !this.model.isNullOrEmpty() &&
            this.status > 0
}

enum class InferenceStatus(val value: Int) {
    STARTED(0),
    FINISHED(1),
    VALIDATED(2),
    INVALIDATED(3),
    VOTING(4),
    EXPIRED(5),
}

data class InferencesWrapper(
    val inference: List<InferencePayload> = listOf()
)

data class InferenceWrapper(
    val inference: InferencePayload
)

data class InferenceTimeoutsWrapper(
    val inferenceTimeout: List<InferenceTimeout> = listOf()
)

data class InferenceTimeout(
    val expirationHeight: String,
    val inferenceId: String,
)

data class MsgStartInference(
    override val type: String = "/inference.inference.MsgStartInference",
    val creator: String = "",
    val inferenceId: String,
    val promptHash: String,
    val promptPayload: String,
    val model: String = "",
    val requestedBy: String = "",
    val assignedTo: String = "",
    val nodeVersion: String = "",
    val maxTokens: Long = 0,
    val promptTokenCount: Long = 0,
    val requestTimestamp: Long = 0,
    val transferSignature: String = "",
    val originalPrompt: String = promptPayload,
) : TxMessage

data class MsgFinishInference(
    override val type: String = "/inference.inference.MsgFinishInference",
    val creator: String = "",
    val inferenceId: String = "",
    val responseHash: String = "",
    val responsePayload: String = "",
    val promptTokenCount: Long = 0,
    val completionTokenCount: Long = 0,
    val executedBy: String = "",
    val transferredBy: String = "",
    val requestTimestamp: Long = 0,
    val transferSignature: String = "",
    val executorSignature: String = "",
    val requestedBy: String = "",
    val originalPrompt: String = "",
    val model: String = "",
) : TxMessage

