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

    fun getEscrowId(): Long? {
        val event = events.firstOrNull { it.type == "devshard_escrow_created" }
        return event?.attributes?.firstOrNull { it.key == "escrow_id" }?.value?.toLongOrNull()
    }

    /** Code id emitted by `tx wasm store`. */
    fun getCodeId(): Long? {
        val event = events.firstOrNull { it.type == "store_code" }
        return event?.attributes?.firstOrNull { it.key == "code_id" }?.value?.toLongOrNull()
    }

    /** Contract address emitted by `tx wasm instantiate`. */
    fun getContractAddress(): String? {
        val event = events.firstOrNull { it.type == "instantiate" }
        return event?.attributes?.firstOrNull { it.key == "_contract_address" }?.value
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
