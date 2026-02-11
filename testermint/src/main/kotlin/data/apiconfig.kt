package com.productscience.data

import com.fasterxml.jackson.annotation.JsonProperty

data class ApiConfig(
    val api: ApiSettings,
    val bandwidthParams: BandwidthParams,
    val chainNode: ChainNode,
    val currentHeight: Long,
    val lastProcessedHeight: Long,
    val currentNodeVersion: String,
    val lastUsedVersion: String?,
    val currentSeed: SeedInfo,
    val mergedNodeConfig: Boolean,
    val mlNodeKeyConfig: MlNodeKeyConfig,
    val nats: NatsConfig,
    @Deprecated("Use upgradePlan.nodeVersion instead")
    val nodeVersions: NodeVersions?,
    val nodes: List<NodeConfig>,
    val previousSeed: SeedInfo,
    val upcomingSeed: SeedInfo,
    val upgradePlan: UpgradePlan,
    val validationParams: ApiValidationParams
)

data class ApiSettings(
    val adminServerPort: Int,
    val mlGrpcCallbackAddress: String,
    val mlGrpcServerPort: Int,
    val mlServerPort: Int,
    val pocCallbackUrl: String,
    val port: Int,
    val publicServerPort: Int,
    val publicUrl: String,
    val testMode: Boolean
)

data class BandwidthParams(
    val estimatedLimitsPerBlockKb: Long,
    val kbPerInputToken: Double,
    val kbPerOutputToken: Double
)

data class ChainNode(

    @JsonProperty("KeyringPassword")
    val keyringPassword: String?,
    val accountPublicKey: String,
    val isGenesis: Boolean,
    val keyringBackend: String,
    val keyringDir: String,
    val seedApiUrl: String,
    val signerKeyName: String,
    val url: String
)

data class SeedInfo(
    val epochIndex: Long,
    val seed: Long,
    val signature: String,
    val claimed: Boolean,
)

data class MlNodeKeyConfig(
    val workerPrivate: String,
    val workerPublic: String
)

data class NatsConfig(
    val host: String,
    val port: Int
)

@Deprecated("Use upgradePlan.nodeVersion instead")
data class NodeVersions(
    val versions: List<String>
)

data class NodeConfig(
    val hardware: List<HardwareConfig>,
    val host: String,
    val id: String,
    val inferencePort: Int,
    val inferenceSegment: String,
    val maxConcurrent: Int,
    val models: Map<String, ApiModelConfig>,
    val pocPort: Int,
    val pocSegment: String,
    val version: String? = null
)

data class HardwareConfig(
    val type: String,
    val count: Int,
)

data class ApiModelConfig(
    @JsonProperty("Args")
    val args: List<String>
)

data class UpgradePlan(
    val binaries: Map<String, String>,
    val height: Long,
    val name: String,
    val nodeVersion: String = ""
)

data class ApiValidationParams(
    val expirationBlocks: Int,
    val timestampAdvance: Int,
    val timestampExpiration: Int
)