package com.productscience.data

import com.productscience.LocalInferencePair

data class ParticipantsResponse(
    val participants: List<Participant>,
)

data class ParticipantStatsResponse(
    val participantCurrentStats: List<ParticipantStats>? = listOf(),
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

data class ActiveParticipantsResponse(
    val activeParticipants: ActiveParticipants,
    val addresses: List<String>,
    val validators: List<ActiveValidator>,
    val excludedParticipants: List<ExcludedParticipant>
) : HasParticipants<ActiveParticipant> {
    override fun getParticipantList(): List<ActiveParticipant> = activeParticipants.participants
}

data class ExcludedParticipant(
    val address: String,
    val reason: String,
    val exclusionBlockHeight: Long,
)

data class ActiveParticipants(
    val participants: List<ActiveParticipant>,
    val epochGroupId: Long,
    val pocStartBlockHeight: Long,
    val effectiveBlockHeight: Long,
    val createdAtBlockHeight: Long,
    val epochId: Long,
) : HasParticipants<ActiveParticipant> {
    override fun getParticipantList(): List<ActiveParticipant> = participants
}

data class ActiveParticipant(
    override val index: String,
    val validatorKey: String,
    val weight: Long,
    val inferenceUrl: String,
    val models: List<String>,
    val seed: Seed,
    val mlNodes: List<MlNodes>,
) : ParticipantInfo

data class Seed(
    val participant: String,
    val epochIndex: Long,
    val signature: String,
)

data class MlNodes(
    val mlNodes: List<MlNode>,
)

data class MlNode(
    val nodeId: String,
    val pocWeight: Long,
    val timeslotAllocation: List<Boolean>,
)

data class ActiveValidator(
    val address: String,
    val pubKey: String,
    val votingPower: Long,
    val proposerPriority: Long,
)

data class RawParticipant(
    override val index: String,
    val address: String,
    val weight: Long,
    val joinTime: Long,
    val joinHeight: Long,
    val inferenceUrl: String,
    val status: Int,
    val epochsCompleted: Long,
) : ParticipantInfo

data class RawParticipantWrapper(
    val participant: List<RawParticipant>
) : HasParticipants<RawParticipant> {
    override fun getParticipantList(): List<RawParticipant> = participant
}

interface ParticipantInfo {
    val index: String
}

interface HasParticipants<T : ParticipantInfo> {
    fun getParticipantList(): Iterable<T>
}

inline fun <reified T : ParticipantInfo> Iterable<T>.getParticipant(pair: LocalInferencePair): T? =
    this.firstOrNull { it.index == pair.node.getColdAddress() }

inline fun <reified T: ParticipantInfo> HasParticipants<T>.getParticipant(pair: LocalInferencePair): T? =
    this.getParticipantList().getParticipant(pair)