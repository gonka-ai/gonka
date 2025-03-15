package com.productscience.data

data class AccountWrapper(
    val account: Account
)

data class Account(
    val type: String,
    val value: AccountValue
)

data class AccountValue(
    val address: String,
    val publicKey: String,
    val accountNumber: Long,
    val sequence: Long,
    val name: String,
    val permissions: List<String>
)

