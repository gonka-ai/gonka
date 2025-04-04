package com.productscience.data

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