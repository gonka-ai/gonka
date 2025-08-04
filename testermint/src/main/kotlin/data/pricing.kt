package com.productscience.data

data class UnitOfComputePriceProposalDto(
    val price: ULong,
    val denom: String,
)

data class GetUnitOfComputePriceProposalDto(
    val proposal: Proposal?,
    val default: ULong,
) {
    data class Proposal(
        val price: ULong,
    )
}

data class GetPricingDto(
    val price: ULong,
    val models: List<ModelPriceDto>,
)

data class ModelPriceDto(
    val id: String,
    val unitsOfComputePerToken: ULong,
    val pricePerToken: ULong,
)

data class RegisterModelDto(
    val id: String,
    val unitsOfComputePerToken: ULong,
)

/**
 * Response data class for model per token price queries
 */
data class ModelPerTokenPriceResponse(
    val price: String,
    val found: Boolean
)
