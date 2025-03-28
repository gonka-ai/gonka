package com.productscience.data

data class NodeResponse(val node:InferenceNode, val state: NodeState)

data class InferenceNode(
    val host: String,
    val inferenceSegment: String = "",
    val inferencePort: Int,
    val pocSegment: String = "",
    val pocPort: Int,
    val models: List<String>,
    val id: String,
    val maxConcurrent: Int,
    val version: String = "",
)

data class NodeState(
    val lockCount: Int,
    val operational: Boolean,
    val failureReason: String
)
