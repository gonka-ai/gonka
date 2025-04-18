package com.productscience.data

import java.math.BigInteger
import java.time.Instant

interface GovernanceMessage {
    val type: String
    fun withAuthority(authority: String): GovernanceMessage
}

data class CreatePartialUpgrade(
    val height: String,
    val nodeVersion: String,
    val apiBinariesJson: String,
    val authority: String = "",
) : GovernanceMessage {
    override val type: String = "/inference.inference.MsgCreatePartialUpgrade"
    override fun withAuthority(authority: String): GovernanceMessage {
        return this.copy(authority = authority)
    }
}

data class GovernanceProposal(
    val metadata: String,
    val deposit: String,
    val title: String,
    val summary: String,
    val expedited: Boolean,
    val messages: List<GovernanceMessage>,
)

data class UpdateParams(
    val authority: String = "",
    val params: InferenceParams,
) : GovernanceMessage {
    override val type: String = "/inference.inference.MsgUpdateParams"
    override fun withAuthority(authority: String): GovernanceMessage {
        return this.copy(authority = authority)
    }
}

data class DepositorAmount(
    val denom: String,
    val amount: BigInteger
)

data class FinalTallyResult(
    val yesCount: Long,
    val abstainCount: Long,
    val noCount: Long,
    val noWithVetoCount: Long
)

data class GovernanceProposalResponse(
    val id: String,
    val status: Int,
    val finalTallyResult: FinalTallyResult,
    val submitTime: Instant,
    val depositEndTime: Instant,
    val totalDeposit: List<DepositorAmount>,
    val votingStartTime: Instant,
    val votingEndTime: Instant,
    val metadata: String,
    val title: String,
    val summary: String,
    val proposer: String,
    val failedReason: String
)

data class GovernanceProposals(
    val proposals: List<GovernanceProposalResponse>,
)

