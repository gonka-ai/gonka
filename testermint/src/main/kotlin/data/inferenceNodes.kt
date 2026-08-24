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
) {
    val pocHost: String
        get() = "$host:$pocPort"
    val inferenceHost: String
        get() = "$host:$inferencePort"
}

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
    val preservedModels: Map<String, Boolean>? = null,
)

data class AdminState(
    val enabled: Boolean,
    val epoch: Long
)

// Chain-side view of a participant's registered hardware inventory, as returned
// by `query inference hardware-nodes-all`. Distinct from the DAPI-local
// InferenceNode: this is what epoch formation reads when seating models.
data class HardwareNodesAllResponse(
    val nodes: List<ChainHardwareNodes> = emptyList()
)

data class ChainHardwareNodes(
    val participant: String = "",
    @com.google.gson.annotations.SerializedName("hardware_nodes")
    val hardwareNodes: List<ChainHardwareNode> = emptyList()
)

data class ChainHardwareNode(
    @com.google.gson.annotations.SerializedName("local_id")
    val localId: String = "",
    val status: String = "",
    val models: List<String> = emptyList(),
    val host: String = "",
    val port: String = "",
    val version: String = "",
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

data class LastUpgradeHeightQueryResponse(
    val lastUpgradeHeight: Long,
    val found: Boolean,
)

data class MlNodeVersion(
    val currentVersion: String,
)
