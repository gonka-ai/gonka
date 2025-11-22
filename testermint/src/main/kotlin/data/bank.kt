package com.productscience.data

data class BalanceResponse(
    val balance: Balance
)

data class BalanceListResponse(
    val balances: List<BalanceItem>?
)

data class BalanceItem(
    val denom: String,
    val amount: String
)

data class Balance(
    val denom: String,
    val amount: Long
)
