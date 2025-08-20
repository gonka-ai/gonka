package com.productscience.data

// Genesis Transfer query response types for CLI integration

data class GenesisTransferStatusResponse(
    val transferRecord: GenesisTransferRecord?
)

data class GenesisTransferRecord(
    val genesisAddress: String,
    val recipientAddress: String,
    val transferHeight: String,
    val completed: Boolean,
    val transferredDenoms: List<String>?,
    val transferAmount: String
)

data class GenesisTransferHistoryResponse(
    val transferRecords: List<GenesisTransferRecord>?,
    val pagination: CometPagination?
)

data class GenesisTransferEligibilityResponse(
    val eligible: Boolean,
    val reason: String?
)

data class GenesisTransferParamsWrapper(
    val params: GenesisTransferParams
)

data class GenesisTransferParams(
    val allowedAccounts: List<String>?,
    val restrictToList: Boolean?
)

data class GenesisTransferAllowedAccountsResponse(
    val allowedAccounts: List<String>?
)
