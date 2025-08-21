package com.productscience.data

import java.math.BigInteger
import java.time.Instant

interface TxMessage {
    val type: String
}

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
    val status: Any,  // Changed from Int to Any to handle both Int and String
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
) {
    // Helper function to get status as integer, handling both Int and String values
    fun getStatusAsInt(): Int {
        return when (status) {
            is Int -> status
            is String -> {
                when (status) {
                    "PROPOSAL_STATUS_UNSPECIFIED" -> 0
                    "PROPOSAL_STATUS_DEPOSIT_PERIOD" -> 1
                    "PROPOSAL_STATUS_VOTING_PERIOD" -> 2
                    "PROPOSAL_STATUS_PASSED" -> 3
                    "PROPOSAL_STATUS_REJECTED" -> 4
                    "PROPOSAL_STATUS_FAILED" -> 5
                    else -> {
                        // Try to parse as number if it's a numeric string
                        status.toIntOrNull() ?: 0
                    }
                }
            }
            else -> 0
        }
    }
}

data class GovernanceProposals(
    val proposals: List<GovernanceProposalResponse>,
)

data class ProposalVoteOption(
    val option: Any,  // Changed from Int to Any to handle both Int and String
    val weight: String
) {
    // Helper function to get option as integer, handling both Int and String values
    fun getOptionAsInt(): Int {
        return when (option) {
            is Int -> option
            is String -> {
                when (option) {
                    "VOTE_OPTION_UNSPECIFIED" -> 0
                    "VOTE_OPTION_YES" -> 1
                    "VOTE_OPTION_ABSTAIN" -> 2
                    "VOTE_OPTION_NO" -> 3
                    "VOTE_OPTION_NO_WITH_VETO" -> 4
                    else -> {
                        // Try to parse as number if it's a numeric string
                        option.toIntOrNull() ?: 0
                    }
                }
            }
            else -> 0
        }
    }
}

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

