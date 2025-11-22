package com.productscience.data

data class NodeResponse(val node: InferenceNode, val state: NodeState)

data class InferenceNode(
    val host: String,
    val inferenceSegment: String = "",
    val inferencePort: Int,
    val pocSegment: String = "",
    val pocPort: Int,
    val models: Map<String, ModelConfig>,
    val id: String,
    val maxConcurrent: Int,
    val nodeNum: Long? = null,
    val hardware: List<Hardware>? = null,
    val version: String? = null,
)

data class Hardware(
    val type: String,
    val count: Int
)

data class NodeState(
    val intendedStatus: String,
    val currentStatus: String,
    val pocIntendedStatus: String,
    val pocCurrentStatus: String,
    val lockCount: Int,
    val failureReason: String,
    val statusTimestamp: String,
    val adminState: AdminState? = null,
    val epochModels: Map<String, EpochModel>?,
    val epochMlNodes: Map<String, EpochMlNode>?,
)

data class AdminState(
    val enabled: Boolean,
    val epoch: Long
)

data class ModelConfig(
    val args: List<String>
)

data class EpochModel(
    val proposedBy: String,
    val id: String,
    val unitsOfComputePerToken: Long,
    val hfRepo: String,
    val hfCommit: String,
    val modelArgs: List<String>,
    val vRam: Int,
    val throughputPerNonce: Long
)

data class EpochMlNode(
    val nodeId: String,
    val pocWeight: Int,
    val timeslotAllocation: List<Boolean>
)

data class NodeAdminStateResponse(
    val message: String,
    val nodeId: String
)

data class MlNodeVersionQueryResponse(
    val mlnodeVersion: MlNodeVersion
)

data class MlNodeVersion(
    val currentVersion: String,
)