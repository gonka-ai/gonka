package com.productscience.data

import com.google.gson.annotations.SerializedName
import com.productscience.data.Coin

data class Collateral(
    val amount: Coin?,
    val unbonding: List<Any>
)

data class CollateralParams(
    val slashFractionInvalid: Decimal,
    val slashFractionDowntime: Decimal,
    val downtimeMissedPercentageThreshold: Decimal,
    val gracePeriodEndEpoch: Long,
    val baseWeightRatio: Decimal,
    val collateralPerWeightUnit: Decimal
)

data class CollateralParamsWrapper(
    val params: CollateralModuleParams
)

data class CollateralModuleParams(
    @SerializedName("unbonding_period_epochs")
    val unbondingPeriodEpochs: String
)

data class UnbondingCollateralEntry(
    val participant: String,
    val amount: Coin,
    @SerializedName("completion_epoch")
    val completionEpoch: String
)

data class UnbondingCollateralResponse(
    @SerializedName("unbondings")
    val unbondings: List<UnbondingCollateralEntry>?
) 