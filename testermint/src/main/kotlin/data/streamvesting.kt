package com.productscience.data

import com.google.gson.annotations.SerializedName

data class VestingSchedule(
    @SerializedName("participant_address")
    val participantAddress: String,
    @SerializedName("epoch_amounts")
    val epochAmounts: List<EpochCoins>
)

data class EpochCoins(
    val coins: List<Coin>
)

data class VestingScheduleResponse(
    @SerializedName("vesting_schedule")
    val vestingSchedule: VestingSchedule?
)

data class TotalVestingAmountResponse(
    @SerializedName("total_amount")
    val totalAmount: Coin?
)

data class StreamVestingParams(
    @SerializedName("reward_vesting_period")
    val rewardVestingPeriod: String
)

data class StreamVestingParamsWrapper(
    val params: StreamVestingParams
)

 