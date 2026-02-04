package com.productscience.data

import com.google.gson.annotations.SerializedName

/**
 * Request body for POST /v1/poc/proofs
 * Used by validators to request artifact proofs from participants.
 */
data class PocProofsRequest(
    @SerializedName("poc_stage_start_block_height")
    val pocStageStartBlockHeight: Long,
    @SerializedName("root_hash")
    val rootHash: String,  // base64-encoded 32 bytes
    val count: Long,
    @SerializedName("leaf_indices")
    val leafIndices: List<Long>,
    @SerializedName("validator_address")
    val validatorAddress: String,  // validator's cold key (for authz lookup)
    @SerializedName("validator_signer_address")
    val validatorSignerAddress: String,  // actual signer (cold or warm key)
    val timestamp: Long,  // unix nanoseconds
    val signature: String  // base64-encoded signature
)

/**
 * Single proof item in the response
 */
data class PocProofItem(
    @SerializedName("leaf_index")
    val leafIndex: Long,
    @SerializedName("nonce_value")
    val nonceValue: Int,
    @SerializedName("vector_bytes")
    val vectorBytes: String,  // base64-encoded
    val proof: List<String>  // base64-encoded hashes
)

/**
 * Response body for POST /v1/poc/proofs
 */
data class PocProofsResponse(
    val proofs: List<PocProofItem>
)

/**
 * Response body for GET /v1/poc/artifacts/state
 * Returns current artifact store state for a given epoch.
 */
data class PocArtifactsStateResponse(
    @SerializedName("poc_stage_start_block_height")
    val pocStageStartBlockHeight: Long,
    val count: Long,
    @SerializedName("root_hash")
    val rootHash: String  // base64-encoded 32 bytes, empty if count=0
)
