package com.productscience.data

import com.google.gson.JsonElement
import com.google.gson.annotations.SerializedName
import java.time.Instant

data class TxResponse(
    val height: Long,
    val txhash: String,
    val codespace: String,
    val code: Int,
    @SerializedName("data") val transactionData: String,  // Use SerializedName to map "data" to "transactionData"
    val rawLog: String,
//    val logs: List<Log>, Don't have a good idea of this structure
    val info: String,
    val gasWanted: Long,
    val gasUsed: Long,
    val tx: JsonElement?,  // Capture raw JSON for the "tx" field
    val timestamp: Instant?,
    val events: List<Event>,
) {
    fun getProposalId(): String? {
        val proposalEvent = events.firstOrNull { it.type == "submit_proposal" }
        val proposalId = proposalEvent?.attributes?.firstOrNull { it.key == "proposal_id" }
        return proposalId?.value
    }
}

data class Event(
    val type: String,
    val attributes: List<Attribute>,
)

data class Attribute(
    val key: String,
    val value: String,
    val index: Boolean,
)
