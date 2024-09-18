package com.productscience.data

data class ParticipantsResponse(
    val participants: List<Participant>
)

data class Participant(
    val id: String,
    val url: String,
    val models: List<String>,
    val coinsOwed: Long,
    val refundsOwed: Long,
    val balance: Long,
    val votingPower: Int
)

data class InferenceParticipant(
    val url: String,
    val models: List<String>,
    val validatorKey: String,
)
