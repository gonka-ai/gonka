package com.productscience.data

import com.google.gson.annotations.SerializedName

data class PreservedNodesSnapshotQueryResponse(
    val snapshot: PreservedNodesSnapshot? = null,
    val found: Boolean = false,
)

data class PreservedNodesSnapshot(
    @SerializedName("episode_anchor_height")
    val episodeAnchorHeight: Long,
    @SerializedName("model_preserved_nodes")
    val modelPreservedNodes: List<ModelPreservedNodes> = emptyList(),
)

data class ModelPreservedNodes(
    @SerializedName("model_id")
    val modelId: String,
    @SerializedName("preserved_node_ids")
    val preservedNodeIds: List<String> = emptyList(),
)
