package com.productscience.data

data class ParticipantsResponse(
    val participants: List<Participant>,
)

data class ParticipantStatsResponse(
    val participantCurrentStats: List<ParticipantStats> = listOf(),
    val blockHeight: Long,
    val epochId: Long?,
)

data class ParticipantStats(
    val participantId: String,
    val weight: Long = 0,
    val reputation: Int = 0,
)

data class Participant(
    val id: String,
    val url: String,
    val models: List<String>? = listOf(),
    val coinsOwed: Long,
    val refundsOwed: Long,
    val balance: Long,
    val votingPower: Int,
    val reputation: Double
)

data class ActiveParticipantsResponse(
    val activeParticipants: ActiveParticipants,
    val addresses: List<String>,
    val activeParticipantsBytes: String,
    val proofOps: ProofOps? = null,
    val validators: List<ResponseValidator>? = null,
    val block: List<Block>? = null
)

data class ActiveParticipants(
    val participants: List<ActiveParticipant>,
    val epochGroupId: Long,
    val pocStartBlockHeight: Long,
    val createdAtBlockHeight: Long
)

data class ActiveParticipant(
    val index: String,
    val validatorKey: String,
    val weight: Long,
    val inferenceUrl: String,
    val models: List<String>,
    val seed: RandomSeed,
)

data class RandomSeed(
    val participant: String,
    val blockHeight: Long,
    val signature: String,
)

data class ProofOps(
    val ops: List<ProofOp>? = null
)

data class ProofOp(
    val type: String? = null,
    val key: String? = null,
    val data: String? = null
)

data class ResponseValidator(
    val address: String? = null,
    val pubKey: String? = null,
    val votingPower: Long? = null,
    val proposerPriority: Long? = null
)

data class Block(
    val header: BlockHeader? = null,
    val data: BlockData? = null,
    val evidence: BlockEvidence? = null,
    val lastCommit: LastCommit? = null
)

data class BlockHeader(
    val version: BlockVersion? = null,
    val chainId: String? = null,
    val height: Long? = null,
    val time: String? = null,
    val lastBlockId: BlockId? = null,
    val lastCommitHash: String? = null,
    val dataHash: String? = null,
    val validatorsHash: String? = null,
    val nextValidatorsHash: String? = null,
    val consensusHash: String? = null,
    val appHash: String? = null,
    val lastResultsHash: String? = null,
    val evidenceHash: String? = null,
    val proposerAddress: String? = null
)

data class BlockVersion(
    val block: Long? = null
)

data class BlockId(
    val hash: String? = null,
    val parts: BlockParts? = null
)

data class BlockParts(
    val total: Int? = null,
    val hash: String? = null
)

data class BlockData(
    val txs: List<String>? = null
)

data class BlockEvidence(
    val evidence: List<String>? = null
)

data class LastCommit(
    val height: Long? = null,
    val round: Int? = null,
    val blockId: BlockId? = null,
    val signatures: List<Signature>? = null
)

data class Signature(
    val blockIdFlag: Int? = null,
    val validatorAddress: String? = null,
    val timestamp: String? = null,
    val signature: String? = null
)

data class InferenceParticipant(
    val url: String,
    val models: List<String>? = listOf(),
    val validatorKey: String,
)

data class UnfundedInferenceParticipant(
    val url: String,
    val models: List<String>? = listOf(),
    val validatorKey: String,
    val pubKey: String,
    val address: String
)
