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
    val dkgPhase: String,
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
)

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
    val status: String,
    @SerializedName("partial_signatures")
    val partialSignatures: List<PartialSignature>?,
    @SerializedName("final_signature")
    val finalSignature: String?,
    @SerializedName("created_block_height")
    val createdBlockHeight: Long,
    @SerializedName("deadline_block_height")
    val deadlineBlockHeight: Long
)

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