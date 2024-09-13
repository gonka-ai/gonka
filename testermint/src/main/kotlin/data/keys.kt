package com.productscience.data

import com.google.gson.annotations.SerializedName

data class Validator(
    val name: String,
    val type: String,
    val address: String,
    val pubkey: String
)

data class Pubkey(
    @field:SerializedName("@type") val type: String,
    val key: String
)
