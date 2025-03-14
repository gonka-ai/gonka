package com.productscience.data

import java.time.Instant

data class NodeInfoResponse(
    val nodeInfo: NodeInfo,
    val syncInfo: SyncInfo,
    val validatorInfo: ValidatorInfo
)

data class NodeInfo(
    val protocolVersion: ProtocolVersion,
    val id: String,
    val listenAddr: String,
    val network: String,
    val version: String,
    val channels: String,
    val moniker: String,
    val other: Other
)

data class ProtocolVersion(
    val p2p: String,
    val block: String,
    val app: String
)

data class Other(
    val txIndex: String,
    val rpcAddress: String
)

data class SyncInfo(
    val latestBlockHash: String,
    val latestAppHash: String,
    val latestBlockHeight: Long,
    val latestBlockTime: String,
    val earliestBlockHash: String,
    val earliestAppHash: String,
    val earliestBlockHeight: Long,
    val earliestBlockTime: Instant,
    val catchingUp: Boolean
)

data class ValidatorInfo(
    val address: String,
    val pubKey: PubKey,
    val votingPower: Int
)

public data class PubKey(
    val type: String,
    val value: String
)

data class MinimumValidationAverage(
    val trafficBasis: Long = 0,
    val minimumValidationAverage: Double,
    val blockHeight: Long,
)
