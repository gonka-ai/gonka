package com.productscience.data

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

