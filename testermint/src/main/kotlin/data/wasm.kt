package com.productscience.data

/**
 * `query wasm contract-state smart` wraps the contract's own response in a
 * `data` envelope. One wrapper per contract response type, since Gson needs a
 * concrete type at runtime.
 */
data class SplitQueryResponse(
    val data: Split = Split()
)

/** Response of the reward-splitter `{"split":{}}` query. */
data class Split(
    val payees: List<Payee> = emptyList(),
    val totalShares: Long = 0
)

data class Payee(
    val address: String = "",
    val shares: Long = 0
)
