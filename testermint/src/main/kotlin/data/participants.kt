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
    val reputation: Double,
    val status: Any? = null  // Added missing status field to handle both String and Int
) {
    // Helper function to get status as integer, handling both Int and String values
    fun getStatusAsInt(): Int {
        return when (status) {
            is Int -> status
            is String -> {
                when (status) {
                    "PARTICIPANT_STATUS_UNSPECIFIED" -> 0
                    "PARTICIPANT_STATUS_ACTIVE" -> 1
                    "PARTICIPANT_STATUS_INACTIVE" -> 2
                    "PARTICIPANT_STATUS_INVALID" -> 3
                    "PARTICIPANT_STATUS_RAMPING" -> 4
                    else -> {
                        // Try to parse as number if it's a numeric string
                        status.toIntOrNull() ?: 0
                    }
                }
            }
            null -> 0  // Default to UNSPECIFIED if not provided
            else -> 0
        }
    }
}

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

enum class ParticipantStatus(val value: Int) {
    UNSPECIFIED(0),
    ACTIVE(1),
    INACTIVE(2),
    INVALID(3),
    RAMPING(4)
}

