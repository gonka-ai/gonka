package com.productscience.data

data class PoCDelegationResponse(
    val delegations: List<PoCDelegationEntry> = emptyList(),
    val refusals: List<PoCRefusalEntry> = emptyList(),
    val intents: List<PoCDirectIntentEntry> = emptyList(),
)

data class PoCDelegationEntry(
    val modelId: String,
    val delegator: String,
    val delegateTo: String,
)

data class PoCRefusalEntry(
    val modelId: String,
    val participant: String,
)

data class PoCDirectIntentEntry(
    val modelId: String,
    val participant: String,
)
