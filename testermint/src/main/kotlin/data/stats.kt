package com.productscience.data

data class StatsModelsResponse(
    val statsModels: List<StatsModelDto>,
)

data class StatsModelDto(
    val model: String,
    val aiTokens: Long,
    val inferences: Int,
)

data class StatsSummaryResponse(
    val aiTokens: Long,
    val inferences: Int,
    val actualInferencesCost: Long,
)

data class DeveloperInferencesResponse(
    val stats: List<DeveloperStatsByTimeDto>,
)

data class DeveloperStatsByTimeDto(
    val epochId: Long,
    val timestamp: Long,
    val inference: InferenceStatsDto,
)

data class InferenceStatsDto(
    val inferenceId: String,
    val epochId: Long,
    val status: String,
    val totalTokenCount: Long,
    val model: String,
    val actualCostInCoins: Long,
)

data class DebugStatsResponse(
    val statsByTime: List<DebugTimeStatDto>,
    val statsByEpoch: List<DebugEpochStatDto>,
)

data class DebugTimeStatDto(
    val developer: String,
    val stats: List<DeveloperStatsByTimeDto>,
)

data class DebugEpochStatDto(
    val developer: String,
    val stats: List<DeveloperStatsByEpochDto>,
)

data class DeveloperStatsByEpochDto(
    val epochId: Long,
    val inferenceIds: List<String>,
)
