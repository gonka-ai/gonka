package com.productscience.data

import com.google.gson.annotations.SerializedName

data class EpochBLSDataWrapper(
    @SerializedName("epoch_data")
    val epochData: EpochBLSData
)

data class SigningStatusWrapper(
    @SerializedName("signing_request")
    val signingRequest: ThresholdSigningRequest
)

data class EpochBLSData(
    @SerializedName("epoch_id")
    val epochId: Long,
    @SerializedName("i_total_slots")
    val iTotalSlots: Int,
    @SerializedName("t_slots_degree")
    val tSlotsDegree: Int,
    val participants: List<BLSParticipantInfo>,
    @SerializedName("dkg_phase")
    val dkgPhase: Any,  // Changed from String to Any to handle both String and Int
    @SerializedName("dealing_phase_deadline_block")
    val dealingPhaseDeadlineBlock: Long,
    @SerializedName("verifying_phase_deadline_block")
    val verifyingPhaseDeadlineBlock: Long,
    @SerializedName("group_public_key")
    val groupPublicKey: String?,
    @SerializedName("dealer_parts")
    val dealerParts: List<DealerPartStorage>?,
    @SerializedName("verification_submissions")
    val verificationSubmissions: List<VerificationVectorSubmission>?,
    @SerializedName("valid_dealers")
    val validDealers: List<Boolean>?,
    @SerializedName("validation_signature")
    val validationSignature: String?
) {
    // Helper function to get DKG phase as enum, handling Int, Double, and String values
    fun getDkgPhaseAsEnum(): DKGPhase {
        return when (dkgPhase) {
            is Int -> DKGPhase.values().find { it.value == dkgPhase } ?: DKGPhase.UNDEFINED
            is Double -> DKGPhase.values().find { it.value == dkgPhase.toInt() } ?: DKGPhase.UNDEFINED
            is Float -> DKGPhase.values().find { it.value == dkgPhase.toInt() } ?: DKGPhase.UNDEFINED
            is Number -> DKGPhase.values().find { it.value == dkgPhase.toInt() } ?: DKGPhase.UNDEFINED
            is String -> {
                when (dkgPhase) {
                    "DKG_PHASE_UNDEFINED" -> DKGPhase.UNDEFINED
                    "DKG_PHASE_DEALING" -> DKGPhase.DEALING
                    "DKG_PHASE_VERIFYING" -> DKGPhase.VERIFYING
                    "DKG_PHASE_COMPLETED" -> DKGPhase.COMPLETED
                    "DKG_PHASE_FAILED" -> DKGPhase.FAILED
                    "DKG_PHASE_SIGNED" -> DKGPhase.SIGNED
                    else -> {
                        // Try to parse as number if it's a numeric string
                        val intValue = dkgPhase.toString().toIntOrNull()
                        if (intValue != null) {
                            DKGPhase.values().find { it.value == intValue } ?: DKGPhase.UNDEFINED
                        } else {
                            DKGPhase.UNDEFINED
                        }
                    }
                }
            }
            else -> DKGPhase.UNDEFINED
        }
    }
}

data class BLSParticipantInfo(
    val address: String,
    @SerializedName("percentage_weight")
    val percentageWeight: String,
    @SerializedName("secp256k1_public_key")
    val secp256k1PublicKey: String,
    @SerializedName("slot_start_index")
    val slotStartIndex: Int,
    @SerializedName("slot_end_index")
    val slotEndIndex: Int
)

data class DealerPartStorage(
    @SerializedName("dealer_address")
    val dealerAddress: String,
    val commitments: List<String>?,
    @SerializedName("participant_shares")
    val participantShares: List<EncryptedSharesForParticipant>?
)

data class EncryptedSharesForParticipant(
    @SerializedName("encrypted_shares")
    val encryptedShares: List<String>?
)

data class VerificationVectorSubmission(
    @SerializedName("participant_address")
    val participantAddress: String,
    @SerializedName("dealer_validity")
    val dealerValidity: List<Boolean>?
)

data class ThresholdSigningRequest(
    @SerializedName("request_id")
    val requestId: String,
    @SerializedName("current_epoch_id")
    val currentEpochId: Long,
    @SerializedName("chain_id")
    val chainId: String,
    val data: List<String>?,
    @SerializedName("encoded_data")
    val encodedData: String,
    @SerializedName("message_hash")
    val messageHash: String,
    val status: Any,  // Changed from String to Any to handle both String and Int
    @SerializedName("partial_signatures")
    val partialSignatures: List<PartialSignature>?,
    @SerializedName("final_signature")
    val finalSignature: String?,
    @SerializedName("created_block_height")
    val createdBlockHeight: Long,
    @SerializedName("deadline_block_height")
    val deadlineBlockHeight: Long
) {
    // Helper function to get status as enum, handling Int, Double, and String values
    fun getStatusAsEnum(): ThresholdSigningStatus {
        return when (status) {
            is Int -> ThresholdSigningStatus.values().find { it.value == status } ?: ThresholdSigningStatus.UNSPECIFIED
            is Double -> ThresholdSigningStatus.values().find { it.value == status.toInt() } ?: ThresholdSigningStatus.UNSPECIFIED
            is Float -> ThresholdSigningStatus.values().find { it.value == status.toInt() } ?: ThresholdSigningStatus.UNSPECIFIED
            is Number -> ThresholdSigningStatus.values().find { it.value == status.toInt() } ?: ThresholdSigningStatus.UNSPECIFIED
            is String -> {
                when (status) {
                    "THRESHOLD_SIGNING_STATUS_UNSPECIFIED" -> ThresholdSigningStatus.UNSPECIFIED
                    "THRESHOLD_SIGNING_STATUS_PENDING" -> ThresholdSigningStatus.PENDING
                    "THRESHOLD_SIGNING_STATUS_COMPLETED" -> ThresholdSigningStatus.COMPLETED
                    "THRESHOLD_SIGNING_STATUS_FAILED" -> ThresholdSigningStatus.FAILED
                    "THRESHOLD_SIGNING_STATUS_EXPIRED" -> ThresholdSigningStatus.EXPIRED
                    else -> {
                        // Try to parse as number if it's a numeric string
                        val intValue = status.toString().toIntOrNull()
                        if (intValue != null) {
                            ThresholdSigningStatus.values().find { it.value == intValue } ?: ThresholdSigningStatus.UNSPECIFIED
                        } else {
                            ThresholdSigningStatus.UNSPECIFIED
                        }
                    }
                }
            }
            else -> ThresholdSigningStatus.UNSPECIFIED
        }
    }
}

data class PartialSignature(
    @SerializedName("participant_address")
    val participantAddress: String,
    @SerializedName("slot_indices")
    val slotIndices: List<Int>?,
    val signature: String
)

data class RequestThresholdSignatureDto(
    @SerializedName("current_epoch_id")
    val currentEpochId: ULong,
    @SerializedName("chain_id")
    val chainId: ByteArray,
    @SerializedName("request_id")
    val requestId: ByteArray,
    val data: List<ByteArray>
)

enum class DKGPhase(val value: Int) {
    UNDEFINED(0),
    DEALING(1),
    VERIFYING(2), 
    COMPLETED(3),
    FAILED(4),
    SIGNED(5)
}

enum class ThresholdSigningStatus(val value: Int) {
    UNSPECIFIED(0),
    PENDING(1),
    COMPLETED(2),
    FAILED(3),
    EXPIRED(4)
}