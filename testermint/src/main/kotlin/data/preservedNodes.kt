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
    @SerializedName("participants")
    val participants: List<ParticipantPreservedNodes> = emptyList(),
)

data class ParticipantPreservedNodes(
    @SerializedName("participant_id")
    val participantId: String,
    @SerializedName("node_ids")
    val nodeIds: List<String> = emptyList(),
)
