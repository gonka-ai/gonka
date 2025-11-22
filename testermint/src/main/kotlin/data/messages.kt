package com.productscience.data

import java.math.BigInteger
import java.time.Instant

interface TxMessage {
    val type: String
}

data class MsgSubmitNewParticipant(
    override val type: String = "/inference.inference.MsgSubmitNewParticipant",
    val creator: String = "",
    val url: String = "",
    val validatorKey: String = "",
    val workerKey: String = "",
) : TxMessage

interface GovernanceMessage : TxMessage {
    override val type: String
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

data class UpdateRestrictionsParams(
    val authority: String = "",
    val params: RestrictionsParams,
) : GovernanceMessage {
    override val type: String = "/inference.restrictions.MsgUpdateParams"
    override fun withAuthority(authority: String): GovernanceMessage {
        return this.copy(authority = authority)
    }
}

data class MsgAddUserToTrainingAllowList(
    val authority: String = "",
    val address: String,
    val role: Int
) : GovernanceMessage {
    override val type: String = "/inference.inference.MsgAddUserToTrainingAllowList"
    override fun withAuthority(authority: String): GovernanceMessage {
        return this.copy(authority = authority)
    }
}

data class MsgRemoveUserFromTrainingAllowList(
    val authority: String = "",
    val address: String,
    val role: Int
) : GovernanceMessage {
    override val type: String = "/inference.inference.MsgRemoveUserFromTrainingAllowList"
    override fun withAuthority(authority: String): GovernanceMessage {
        return this.copy(authority = authority)
    }
}

const val ROLE_EXEC = 0;
const val ROLE_START = 1;

data class MsgSetTrainingAllowList(
    val authority: String = "",
    val addresses: List<String>,
    val role: Int
) : GovernanceMessage {
    override val type: String = "/inference.inference.MsgSetTrainingAllowList"
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

data class ProposalVoteOption(
    val option: Int,
    val weight: String
)

data class ProposalVote(
    val proposal_id: String,
    val voter: String,
    val options: List<ProposalVoteOption>
)

data class ProposalVotePagination(
    val total: String
)

data class ProposalVotes(
    val votes: List<ProposalVote>,
    val pagination: ProposalVotePagination
)

data class Transaction(
    val body: TransactionBody,
)

data class TransactionBody(
    val messages: List<TxMessage>,
    val memo: String,
    val timeoutHeight: Long,
)
