package com.productscience.data

data class BalanceResponse(
    val balance: Balance
)

data class Balance(
    val denom: String,
    val amount: Long
)
