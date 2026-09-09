package com.productscience.data

import com.google.gson.annotations.SerializedName

/**
 * Bridge admin/public JSON uses camelCase (DAPI echo binders), unlike cosmos
 * snake_case. Always set SerializedName explicitly so cosmosJson's
 * LOWER_CASE_WITH_UNDERSCORES policy does not rewrite these fields.
 */

/** POST /admin/v1/bridge/block body (matches DAPI BridgeBlock). */
data class BridgeBlockRequest(
    @SerializedName("blockNumber")
    val blockNumber: String,
    @SerializedName("originChain")
    val originChain: String,
    @SerializedName("receiptsRoot")
    val receiptsRoot: String,
    val receipts: List<BridgeReceiptRequest> = emptyList(),
)

data class BridgeReceiptRequest(
    @SerializedName("contract")
    val contractAddress: String,
    @SerializedName("owner")
    val ownerAddress: String,
    @SerializedName("publicKey")
    val ownerPubKey: String,
    val amount: String,
    @SerializedName("receiptIndex")
    val receiptIndex: String,
)

data class BridgePostBlockResponse(
    val status: String = "",
    val message: String = "",
    @SerializedName("blockNumber")
    val blockNumber: String = "",
    @SerializedName("receiptsCount")
    val receiptsCount: Int = 0,
    @SerializedName("queueSize")
    val queueSize: Int = 0,
)

/** GET /v1/bridge/block/latest?chain=… */
data class LatestBridgeBlockResponse(
    @SerializedName("chainId")
    val chainId: String = "",
    @SerializedName("blockNumber")
    val blockNumber: Long? = null,
)

data class BridgeTransactionQueryResponse(
    @SerializedName("bridgeTransactions")
    val bridgeTransactions: List<BridgeTransactionDto> = emptyList(),
)

data class BridgeTransactionDto(
    val id: String = "",
    @SerializedName("chainId")
    val chainId: String = "",
    @SerializedName("contractAddress")
    val contractAddress: String = "",
    @SerializedName("ownerAddress")
    val ownerAddress: String = "",
    val amount: String = "",
    val status: String = "",
    @SerializedName("blockNumber")
    val blockNumber: String = "",
    @SerializedName("receiptIndex")
    val receiptIndex: String = "",
    @SerializedName("receiptsRoot")
    val receiptsRoot: String = "",
    val validators: List<String> = emptyList(),
    @SerializedName("epochIndex")
    val epochIndex: Long = 0,
)

data class GroupMembersQueryResponse(
    val members: List<GroupMemberDto> = emptyList(),
)

data class GroupMemberDto(
    @SerializedName("group_id")
    val groupId: Long = 0,
    val member: GroupMemberInfo? = null,
)

data class GroupMemberInfo(
    val address: String = "",
    val weight: String = "",
)
