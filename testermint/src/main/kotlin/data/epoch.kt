package com.productscience.data

import com.google.gson.annotations.SerializedName

data class EpochResponse(
    @SerializedName("block_height")
    val blockHeight: Long,
    @SerializedName("latest_epoch")
    val latestEpoch: LatestEpochDto,
    val phase: EpochPhase,
    @SerializedName("epoch_stages")
    val epochStages: EpochStages,
    @SerializedName("next_epoch_stages")
    val nextEpochStages: EpochStages,
    @SerializedName("epoch_params")
    val epochParams: EpochParams,
    @SerializedName("is_confirmation_poc_active")
    val isConfirmationPocActive: Boolean = false,
    @SerializedName("active_confirmation_poc_event")
    val activeConfirmationPocEvent: ConfirmationPoCEvent? = null
) {
    val safeForInference: Boolean
        get() = if (phase == EpochPhase.Inference) {
            nextEpochStages.pocStart - blockHeight > INFERENCE_STAGE_SLACK_BLOCKS
        } else {
            false
        }

    fun findStageSafeInferenceBlock(
        earliestBlock: Long,
        minimumSlackBeforeNextPoc: Long = INFERENCE_STAGE_SLACK_BLOCKS,
    ): StageSafeInferenceBlock? {
        require(minimumSlackBeforeNextPoc >= 0) { "minimumSlackBeforeNextPoc must be non-negative" }

        val epochLength = nextEpochStages.pocStart - epochStages.pocStart
        require(epochLength > 0) { "epoch stages must advance across epochs" }

        val firstInferenceWindowStart = epochStages.claimMoney + 1
        val firstInferenceWindowNextPoc = epochStages.nextPocStart
        val firstCandidateWindowIndex = maxOf(0L, (earliestBlock - firstInferenceWindowStart) / epochLength)

        for (windowIndex in firstCandidateWindowIndex..firstCandidateWindowIndex + 1) {
            val windowStart = firstInferenceWindowStart + windowIndex * epochLength
            val nextPocStart = firstInferenceWindowNextPoc + windowIndex * epochLength
            val candidateBlock = maxOf(blockHeight + 1, earliestBlock, windowStart)
            val latestSafeBlock = nextPocStart - minimumSlackBeforeNextPoc - 1
            if (candidateBlock <= latestSafeBlock) {
                return StageSafeInferenceBlock(
                    block = candidateBlock,
                    inferenceWindowStart = windowStart,
                    nextPocStart = nextPocStart,
                )
            }
        }

        return null
    }
}

const val INFERENCE_STAGE_SLACK_BLOCKS = 3L

data class StageSafeInferenceBlock(
    val block: Long,
    val inferenceWindowStart: Long,
    val nextPocStart: Long,
)

data class LatestEpochDto(
    val index: Long,
    @SerializedName("poc_start_block_height")
    val pocStartBlockHeight: Long
)

enum class EpochPhase {
    PoCGenerate,
    PoCGenerateWindDown,
    PoCValidate,
    PoCValidateWindDown,
    Inference
}

data class EpochStages(
    @SerializedName("epoch_index")
    val epochIndex: Long,
    @SerializedName("poc_start")
    val pocStart: Long,
    @SerializedName("poc_generation_wind_down")
    val pocGenerationWindDown: Long,
    @SerializedName("poc_generation_end")
    val pocGenerationEnd: Long,
    @SerializedName("poc_validation_start")
    val pocValidationStart: Long,
    @SerializedName("poc_validation_wind_down")
    val pocValidationWindDown: Long,
    @SerializedName("poc_validation_end")
    val pocValidationEnd: Long,
    @SerializedName("set_new_validators")
    val setNewValidators: Long,
    @SerializedName("claim_money")
    val claimMoney: Long,
    @SerializedName("next_poc_start")
    val nextPocStart: Long,
    @SerializedName("poc_exchange_window")
    val pocExchangeWindow: EpochExchangeWindow,
    @SerializedName("poc_validation_exchange_window")
    val pocValExchangeWindow: EpochExchangeWindow
)

data class EpochExchangeWindow(
    val start: Long,
    val end: Long
)

data class ConfirmationPoCEvent(
    @SerializedName("epoch_index")
    val epochIndex: Long,
    @SerializedName("event_sequence")
    val eventSequence: Long,
    @SerializedName("trigger_height")
    val triggerHeight: Long,
    @SerializedName("generation_start_height")
    val generationStartHeight: Long,
    val phase: ConfirmationPoCPhase,
    @SerializedName("poc_seed_block_hash")
    val pocSeedBlockHash: String = ""
)

enum class ConfirmationPoCPhase(val value: Int) {
    CONFIRMATION_POC_INACTIVE(0),
    CONFIRMATION_POC_GRACE_PERIOD(1),
    CONFIRMATION_POC_GENERATION(2),
    CONFIRMATION_POC_VALIDATION(3),
    CONFIRMATION_POC_COMPLETED(4)
}

data class ConfirmationPoCEventsResponse(
    val events: List<ConfirmationPoCEvent> = emptyList()
)

data class EpochGroupDataResponse(
    @SerializedName("epoch_group_data")
    val epochGroupData: EpochGroupData
)

data class EpochGroupData(
    @SerializedName("epoch_index")
    val epochIndex: Long = 0,
    @SerializedName("poc_start_block_height")
    val pocStartBlockHeight: Long = 0,
    @SerializedName("model_id")
    val modelId: String = "",
    @SerializedName("validation_weights")
    val validationWeights: List<ValidationWeight> = emptyList()
)

data class ValidationWeight(
    @SerializedName("member_address")
    val memberAddress: String,
    val weight: Long = 0,
    @SerializedName("confirmation_weight")
    val confirmationWeight: Long = 0,
    @SerializedName("ml_nodes")
    val mlNodes: List<MLNodeInfo> = emptyList()
)

data class MLNodeInfo(
    @SerializedName("node_id")
    val nodeId: String,
    @SerializedName("poc_weight")
    val pocWeight: Long = 0
)
