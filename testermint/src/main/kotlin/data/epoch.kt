package com.productscience.data

import com.google.gson.annotations.SerializedName

data class EpochResponse(
    @SerializedName("block_height")
    val blockHeight: Long,
    @SerializedName("latest_epoch")
    val latestEpoch: LatestEpochDto,
    val phase: Any,  // Changed from EpochPhase to Any to handle both String and enum
    @SerializedName("epoch_stages")
    val epochStages: EpochStages,
    @SerializedName("next_epoch_stages")
    val nextEpochStages: EpochStages,
    @SerializedName("epoch_params")
    val epochParams: EpochParams
) {
    // Helper function to get phase as enum, handling Int, Double, String, and enum values
    fun getPhaseAsEnum(): EpochPhase {
        return when (phase) {
            is EpochPhase -> phase
            is Int -> EpochPhase.values().find { it.value == phase } ?: EpochPhase.Inference
            is Double -> EpochPhase.values().find { it.value == phase.toInt() } ?: EpochPhase.Inference
            is Float -> EpochPhase.values().find { it.value == phase.toInt() } ?: EpochPhase.Inference
            is Number -> EpochPhase.values().find { it.value == phase.toInt() } ?: EpochPhase.Inference
            is String -> {
                when (phase) {
                    "POC_GENERATE" -> EpochPhase.PoCGenerate
                    "POC_GENERATE_WIND_DOWN" -> EpochPhase.PoCGenerateWindDown
                    "POC_VALIDATE" -> EpochPhase.PoCValidate
                    "POC_VALIDATE_WIND_DOWN" -> EpochPhase.PoCValidateWindDown
                    "INFERENCE" -> EpochPhase.Inference
                    else -> EpochPhase.Inference // Default fallback
                }
            }
            else -> EpochPhase.Inference
        }
    }
}

data class LatestEpochDto(
    val index: Long,
    @SerializedName("poc_start_block_height")
    val pocStartBlockHeight: Long
)

enum class EpochPhase(val value: Int) {
    PoCGenerate(0),
    PoCGenerateWindDown(1),
    PoCValidate(2),
    PoCValidateWindDown(3),
    Inference(4)
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
