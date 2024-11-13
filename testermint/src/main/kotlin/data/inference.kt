package com.productscience.data

data class InferencePayload(
    val index: String,
    val inferenceId: String,
    val promptHash: String,
    val promptPayload: String,  // Adjusted to String
    val responseHash: String,
    val responsePayload: String,
    val promptTokenCount: Int,
    val completionTokenCount: Int,
    val requestedBy: String,
    val executedBy: String?,
    val status: Int,
    val startBlockHeight: Long,
    val endBlockHeight: Long,
    val startBlockTimestamp: Long,
    val endBlockTimestamp: Long,
    val model: String,
    val maxTokens: Int,
    val actualCost: Long,
    val escrowAmount: Long
)

enum class InferenceStatus(val value: Int) {
    STARTED(0),
    FINISHED(1),
    VALIDATED(2),
    INVALIDATED(3),
    VOTING(4)
}
