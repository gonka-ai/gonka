package com.productscience.data

data class StartTrainingDto(
    val hardwareResources: List<HardwareResourcesDto>,
    val config: TrainingConfigDto
)

data class HardwareResourcesDto(
    val type: String,
    val count: UInt
)

data class TrainingConfigDto(
    val datasets: TrainingDatasetsDto,
    val numUocEstimationSteps: UInt
)

data class TrainingDatasetsDto(
    val train: String,
    val test: String
)

data class LockTrainingNodesDto(
    val trainingTaskId: ULong,
    val nodeIds: List<String>
)
