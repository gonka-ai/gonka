package com.productscience.data

data class UnitOfComputePriceProposalDto(
    val price: ULong,
    val denom: String,
)

data class GetUnitOfComputePriceProposalResponse(
    val proposal: Proposal?,
    val default: ULong,
) {
    data class Proposal(
        val price: ULong,
    )
}
