package com.productscience.data


data class TopMinersResponse(
    val topMiner: List<TopMiner> = listOf(),
    val pagination: Pagination
)

data class TopMiner(
    val address: String,
    val lastQualifiedStarted: Long?,
    val qualifiedPeriods: Int?,
    val qualifiedTime: Long?, // Seconds
    val lastUpdatedTime: Long,
    val firstQualifiedStarted: Long,
)

data class Pagination(val total: String)
