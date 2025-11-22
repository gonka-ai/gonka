package com.productscience.data

import com.google.gson.annotations.SerializedName

data class AccountWrapper(
    val account: Account
)

data class Account(
    @SerializedName("@type")
    val type: String,
    val value: Any  // Can be AccountValue or VestingAccountValue
)

data class AccountValue(
    val address: String,
    val publicKey: String?,
    val accountNumber: Long,
    val sequence: Long,
    val name: String?,
    val permissions: List<String>?
)

// Vesting account data structures for Cosmos SDK vesting accounts
data class VestingAccountValue(
    val address: String,
    val publicKey: String?,
    val accountNumber: Long,
    val sequence: Long,
    val baseVestingAccount: BaseVestingAccount?
)

data class BaseVestingAccount(
    val originalVesting: List<VestingCoin>?,
    val delegatedFree: List<VestingCoin>?,
    val delegatedVesting: List<VestingCoin>?,
    val endTime: String?
)

// Coin for vesting accounts (amount is string in JSON)
data class VestingCoin(
    val denom: String,
    val amount: String
)

