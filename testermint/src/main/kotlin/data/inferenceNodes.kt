package com.productscience.data

data class NodeResponse(val node:InferenceNode, val state: NodeState)

data class InferenceNode(
    val url: String,
    val models: List<String>,
    val id: String,
    val maxConcurrent: Int
)

data class NodeState(
    val lockCount: Int,
    val operational: Boolean,
    val failureReason: String
)
