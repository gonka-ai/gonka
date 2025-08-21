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
    val epochParams: EpochParams
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
