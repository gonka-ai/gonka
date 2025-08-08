package com.productscience

import com.google.gson.JsonSyntaxException
import com.google.gson.reflect.TypeToken
import com.productscience.data.*
import com.productscience.data.toHexString
import org.tinylog.ThreadContext
import org.tinylog.kotlin.Logger
import java.io.Closeable
import java.time.Duration
import java.time.Instant

interface CliExecutor {
    fun exec(args: List<String>, stdin:String?): List<String>
    fun createContainer(doNotStartChain: Boolean = false)
    fun kill()
}

// Usage
data class ApplicationCLI(
    override val config: ApplicationConfig,
    val logOutput: LogOutput,
    val executor: CliExecutor,
    val retryRules: List<CliRetryRule>
) : HasConfig, Closeable {

    fun getGenesisState(): AppExport =
        wrapLog("getGenesisJson", false) {
            val filePath = "/root/.inference/config/genesis.json"
            val readFileCommand = listOf("cat", filePath)

            val output = exec(readFileCommand)
            val joined = output.joinToString("")
            cosmosJson.fromJson(joined, AppExport::class.java)
        }

    fun createContainer(doNotStartChain: Boolean = false) {
        wrapLog("createContainer", false) {
            this.executor.createContainer(doNotStartChain)
        }
    }

    override fun close() {
        this.killExecutor()
    }

    fun killExecutor() {
        wrapLog("killContainer", false) {
            this.executor.kill()
        }
    }

    fun waitFor(
        check: (ApplicationCLI) -> Boolean,
        description: String,
        timeout: Duration = Duration.ofSeconds(20),
        sleepTimeMillis: Long = 1000,
    ) {
        wrapLog("waitFor", false) {
            Logger.info("Waiting for: {}", description)
            val startTime = Instant.now()
            while (true) {
                if (check(this)) {
                    Logger.info("Check reached: $description")
                    break
                }
                if (Duration.between(startTime, Instant.now()) > timeout) {
                    Logger.error("Failed to wait for $description within $timeout")
                    error("Failed to wait for $description within $timeout")
                }
                Thread.sleep(sleepTimeMillis)
            }
        }
    }

    fun waitForState(
        description: String,
        staleTimeout: Duration = Duration.ofSeconds(20),
        check: (status: NodeInfoResponse) -> Boolean,
    ): NodeInfoResponse {
        return wrapLog("waitForState", false) {
            Logger.info("Waiting for state: {}", description)
            var timeout = Instant.now().plus(staleTimeout)
            var previousState: NodeInfoResponse? = null
            while (true) {
                val currentState = getStatus()
                if (check(currentState)) {
                    Logger.info("State reached: $description")
                    return@wrapLog currentState
                }
                if (previousState != currentState) {
                    timeout = Instant.now().plus(staleTimeout)
                }
                if (Instant.now().isAfter(timeout)) {
                    Logger.error("State is stale, was identical for {}. Wait failed for: {}", staleTimeout, description)
                    error("State is stale, was identical for $staleTimeout. Wait failed for: $description")
                }
                previousState = currentState
                Logger.debug(
                    "Current block is {}, continuing to wait for: {}",
                    currentState.syncInfo.latestBlockHeight,
                    description
                )
                Thread.sleep(1000)
            }
            // IDE says unreachable (and it's because of the timeout error in the while loop above,
            //   but if I remove this line then it complains about return being Unit)
            error("Unreachable code reached in waitForState")
        }
    }

    fun waitForMinimumBlock(minBlockHeight: Long, waitingFor: String = ""): Long {
        return wrapLog("waitForMinimumBlock", false) {
            waitForState(
                "$waitingFor:block height $minBlockHeight",
                check = { it.syncInfo.latestBlockHeight >= minBlockHeight }
            )
        }.syncInfo.latestBlockHeight
    }

    fun waitForNextBlock(blocksToWait: Int = 1) {
        wrapLog("waitForNextBlock", false) {
            val currentState = getStatus()
            waitForMinimumBlock(currentState.syncInfo.latestBlockHeight + blocksToWait, "$blocksToWait blocks")
        }
    }

    fun getInferences(): InferencesWrapper = wrapLog("getInferences", false) {
        execAndParse(listOf("query", "inference", "list-inference"))
    }

    fun getInference(inferenceId: String): InferenceWrapper? = wrapLog("getInference", false) {
        execAndParseNullable(listOf("query", "inference", "show-inference", inferenceId))
    }

    fun getInferenceTimeouts(): InferenceTimeoutsWrapper = wrapLog("getInferenceTimeouts", false) {
        execAndParse(listOf("query", "inference", "list-inference-timeout"))
    }

    fun getParticipantCurrentStats(): ParticipantStatsResponse = wrapLog("getParticipantCurrentStats", false) {
        execAndParse(listOf("query", "inference", "get-all-participant-current-stats"))
    }

    fun getMinimumValidationAverage(): MinimumValidationAverage = wrapLog("getMinimumValidationAverage", false) {
        execAndParse(listOf("query", "inference", "get-minimum-validation-average"))
    }

    fun getStatus(): NodeInfoResponse = wrapLog("getStatus", false) { execAndParse(listOf("status")) }

    fun getVersion(): String = wrapLog("getVersion", false) {
        exec(listOf(config.execName, "version")).first()
    }

    var coldAccountKey: Validator? = null
    var warmAccountKey: Validator? = null

    fun getColdAddress(): String = wrapLog("getColdAddress", false) {
        getColdAccountIfNeeded()
        coldAccountKey!!.address
    }

    fun getColdPubKey(): String = wrapLog("getColdPubKey", false) {
        getColdAccountIfNeeded()
        coldAccountKey!!.pubkey.key
    }

    fun getWarmAddress(): String = wrapLog("getWarmAddress", false) {
        getWarmAccountIfNeeded()
        warmAccountKey!!.address
    }

    private fun getColdAccountIfNeeded() {
        if (coldAccountKey == null) {
            val keys = getKeys()
            val coldAccountName = config.pairName.trimStart('/')
            coldAccountKey = (keys.firstOrNull { it.name == coldAccountName }
                ?: keys.firstOrNull { it.type == "local" && !it.name.startsWith("POOL") }
                ?: keys.first())
        }
    }

    private fun getWarmAccountIfNeeded() {
        if (warmAccountKey == null) {
            val keys = getKeys()
            val warmAccountName = config.pairName.trimStart('/') + "_WARM"
            warmAccountKey = (keys.firstOrNull { it.name == warmAccountName }
                ?: keys.firstOrNull { it.type == "local" && !it.name.startsWith("POOL") }
                ?: keys.first())
        }
    }

    fun getColdAccountName(): String = wrapLog("getColdAccountName", false) {
        getColdAccountIfNeeded()
        coldAccountKey!!.name
    }

    fun getWarmAccountName(): String = wrapLog("getWarmAccountName", false) {
        getWarmAccountIfNeeded()
        warmAccountKey!!.name
    }

    val keyringPassword = if (this.config.keyringBackend == "file") this.config.pairName.trimStart('/').padEnd(10, '0') else null
    // Use TypeToken to properly deserialize List<Validator>
    fun getKeys(): List<Validator> = wrapLog("getKeys", false) {
        execAndParseWithType(
            object : TypeToken<List<Validator>>() {},
            listOf("keys", "list") + config.keychainParams,
            password = keyringPassword
        )
    }

    fun createKey(keyName: String): Validator = wrapLog("createKey", false) {
        execAndParse(
            listOf(
                "keys",
                "add",
                keyName
            ) + config.keychainParams,
            password = keyringPassword
        )
    }

    fun getColdSelfBalance(denom: String = this.config.denom): Long = wrapLog("getColdSelfBalance", false) {
        val account = getColdAddress()
        val balance = getBalance(account, denom)
        balance.balance.amount
    }

    fun getWarmSelfBalance(denom: String = this.config.denom): Long = wrapLog("getWarmSelfBalance", false) {
        val account = getWarmAddress()
        val balance = getBalance(account, denom)
        balance.balance.amount
    }

    // Backward compatibility - defaults to cold account
    fun getSelfBalance(denom: String = this.config.denom): Long = getColdSelfBalance(denom)

    fun getBalance(address: String, denom: String): BalanceResponse = wrapLog("getBalance", false) {
        execAndParse(listOf("query", "bank", "balance", address, denom))
    }

    fun queryCollateral(address: String): Collateral = wrapLog("queryCollateral", false) {
        val output = execCli(listOf("query", "collateral", "show-collateral", address))

        if (output.contains("collateral not found")) {
            return@wrapLog Collateral(null, emptyList())
        }

        return@wrapLog cosmosJson.fromJson(output, Collateral::class.java)
    }

    fun queryUnbondingCollateral(address: String): UnbondingCollateralResponse =
        wrapLog("queryUnbondingCollateral", false) {
            execAndParse(listOf("query", "collateral", "show-unbonding-collateral", address))
        }

    fun queryCollateralParams(): CollateralParamsWrapper = wrapLog("queryCollateralParams", false) {
        execAndParse(listOf("query", "collateral", "params"))
    }

    fun queryVestingSchedule(address: String): VestingScheduleResponse = wrapLog("queryVestingSchedule", false) {
        try {
            execAndParse(listOf("query", "streamvesting", "vesting-schedule", address))
        } catch (e: Exception) {
            // Return empty schedule if not found
            VestingScheduleResponse(null)
        }
    }

    fun queryTotalVestingAmount(address: String): TotalVestingAmountResponse =
        wrapLog("queryTotalVestingAmount", false) {
            try {
                execAndParse(listOf("query", "streamvesting", "total-vesting", address))
            } catch (e: Exception) {
                // Return null amount if not found
                TotalVestingAmountResponse(null)
            }
        }

    fun queryStreamVestingParams(): StreamVestingParamsWrapper = wrapLog("queryStreamVestingParams", false) {
        execAndParse(listOf("query", "streamvesting", "params"))
    }

    fun getGovParams(): GovState = wrapLog("getGovParams", false) {
        execAndParse(listOf("query", "gov", "params"))
    }

    fun getGovVotes(proposalId: String): ProposalVotes = wrapLog("getGovVotes", false) {
        execAndParse(listOf("query", "gov", "votes", proposalId))
    }

    fun getInferenceParams(): InferenceParamsWrapper = wrapLog("getInferenceParams", false) {
        execAndParse(listOf("query", "inference", "params"))
    }

    fun getValidators(): ValidatorsResponse = wrapLog("getValidators", false) {
        execAndParse(listOf("query", "staking", "validators"))
    }

    fun getCometValidators(): CometValidatorsResponse = wrapLog("getCometValidators", false) {
        execAndParse(listOf("query", "comet-validator-set"))
    }

    data class TokenomicsWrapper(val tokenomicsData: TokenomicsData)

    fun getTokenomics(): TokenomicsWrapper = wrapLog("getTokenomics", false) {
        execAndParse(listOf("query", "inference", "show-tokenomics-data"))
    }

    fun getTopMiners(): TopMinersResponse = wrapLog("getTopMiners", false) {
        execAndParse(listOf("query", "inference", "list-top-miner"))
    }

    fun queryBLSEpochData(epochId: Long): EpochBLSDataWrapper = wrapLog("queryBLSEpochData", false) {
        execAndParse(listOf("query", "bls", "epoch-data", epochId.toString()))
    }

    fun queryBLSSigningStatus(requestId: String): SigningStatusWrapper = wrapLog("queryBLSSigningStatus", false) {
        execAndParse(listOf("query", "bls", "signing-status", requestId))
    }

    // Reified type parameter to abstract out exec and then json to a particular type
    inline fun <reified T> execAndParse(args: List<String>, includeOutputFlag: Boolean = true, password:String? = null): T {
        val output = execCli(args, includeOutputFlag, password)
        return cosmosJson.fromJson(output, T::class.java)
    }

    fun execCli(args: List<String>, includeOutputFlag: Boolean = true, password:String? = null): String {
        val argsWithJson = listOf(config.execName) +
                args + if (includeOutputFlag) listOf("--output", "json") else emptyList()
        Logger.debug("Executing command: {}", argsWithJson.joinToString(" "))
        val response = exec(argsWithJson, password)
        val output = response.joinToString("")
        Logger.debug("Output: {}", output)

        if (output.contains("inference is not ready; please wait for first block")) {
            throw NotReadyException()
        }
        // Extract JSON payload if output contains gas estimate
        return output.replace(Regex("^gas estimate: \\d+"), "")
    }

    inline fun <reified T> execAndParseNullable(args: List<String>, includeOutputFlag: Boolean = true): T? {
        return try {
            execAndParse(args, includeOutputFlag)
        } catch (e: JsonSyntaxException) {
            Logger.debug("Failed to parse response: {}", e.message)
            null
        }
    }

    // New function that allows using TypeToken for proper deserialization of generic types
    private fun <T> execAndParseWithType(typeToken: TypeToken<T>, args: List<String>, password:String? = null): T {
        val argsWithJson = (listOf(config.execName) + args + "--output" + "json")
        Logger.debug("Executing command: {}", argsWithJson.joinToString(" "))
        val response = exec(argsWithJson, password + "\n")
        val output = response.joinToString("\n")
        Logger.debug("Output: {}", output)
        return cosmosJson.fromJson(output, typeToken.type)
    }

    fun registerNewParticipant(nodeUrl: String, accountPubKey: String, consensusKey: String, nodeAddress: String) =
        wrapLog("registerNewParticipant", false) {
            exec(
                listOf(
                    config.execName,
                    "register-new-participant",
                    nodeUrl,
                    accountPubKey,
                    "--consensus-key",
                    consensusKey,
                    "--node-address",
                    nodeAddress
                )
            )
        }

    fun grantMlOpsPermissionsToWarmAccount() = wrapLog("grantMlOpsPermissions", false) {
        val coldAccountName = this.getColdAccountName()
        val operationAccountAddress = this.getWarmAddress()
        exec(
            listOf(
                config.execName,
                "tx",
                "inference",
                "grant-ml-ops-permissions",
                coldAccountName,
                operationAccountAddress,
                "--from",
                coldAccountName,
                "--gas",
                "2000000",
            )
        )
    }


    fun exec(args: List<String>, stdin: String? = null): List<String> {
        var retries = 0
        while (true) {
            val output = executor.exec(args, stdin)

            if (output.isNotEmpty() && output.first().startsWith("Usage:")) {
                val error = output.joinToString(separator = "").lines().last { it.isNotBlank() }
                throw getExecException(error)
            }
            val operation = ThreadContext.get("operation") ?: "unknown"
            val fullOutput = output.joinToString("")
            val retryWait = retryRules.firstNotNullOfOrNull { it.retryDuration(operation, fullOutput, retries) }
            if (retryWait != null) {
                retries++
                Thread.sleep(retryWait)
                continue
            }
            return output
        }
    }

    private fun extractSignature(response: List<String>): String {
        val signaturePattern = ".*Signature:\\s*([^,\\s]+).*".toRegex()
        return response.firstNotNullOfOrNull {
            signaturePattern.find(it)?.groupValues?.get(1)
        } ?: error("Could not extract signature from response: $response")
    }

    fun signPayload(
        payload: String,
        accountAddress: String? = null,
        timestamp: Long? = null,
        endpointAccount: String? = null
    ): String {
        val parameters = listOfNotNull(
            config.execName,
            "signature",
            "create",
            // Do we need single quotes here?
            payload,
            timestamp?.let { "--timestamp" }, timestamp?.toString(),
            endpointAccount?.let { "--endpoint-account" }, endpointAccount,
            accountAddress?.let { "--account-address" },
            accountAddress,
        ) + config.keychainParams
        return wrapLog("signPayload", true) {
            val response = this.exec(
                parameters
            )
            extractSignature(response).also {
                Logger.info("Signature created, signature={}", it)
            }
        }
    }

    fun getTxStatus(txHash: String): TxResponse = wrapLog("getTxStatus", false) {
        execAndParse(listOf("query", "tx", "--type=hash", txHash))
    }

    fun writeFileToContainer(content: String, fileName: String) = wrapLog("writeFileToContainer", false) {
        try {
            // Write content using echo command
            val writeCommand = listOf(
                "sh", "-c",
                "echo '$content' > $fileName"
            )
            val result = exec(writeCommand)

            // Verify file exists
            val checkCommand = listOf("test", "-f", fileName)
            exec(checkCommand)

        } catch (e: Exception) {
            throw IllegalStateException("Failed to write file to container: ${e.message}", e)
        }
    }

    fun getModuleAccount(accountName: String): AccountWrapper = wrapLog("getAccount", false) {
        execAndParse(listOf("query", "auth", "module-account", accountName))
    }


    fun getTransactionJson(args: List<String>): String {
        val from = this.getColdAccountName()
        Logger.info("Getting transaction json for account {}", from)
        val finalArgs = listOf(
            config.execName,
            "tx"
        ) + args + listOf(
            "--keyring-backend",
            "test",
            "--chain-id=${config.chainId}",
            "--keyring-dir=/root/${config.stateDirName}",
            "--yes",
            "--generate-only",
            "--from",
            from
        )
        return exec(finalArgs).joinToString("")
    }

    fun waitForTxProcessed(txHash: String, maxWait: Int = 10): TxResponse {
        var currentWait = 0
        while (currentWait < maxWait) {
            try {
                val response = this.getTxStatus(txHash)
                if (response.height != 0L) {
                    return response
                }
                Thread.sleep(500)
                currentWait++
            } catch (e: TxNotFoundException) {
                Logger.info("Tx not found (yet), waiting", txHash, e)
                Thread.sleep(1000)
                currentWait++
            }
        }
        error("Transaction not processed after $maxWait attempts")
    }

    fun getValidatorAddress(): String {
        return exec(listOf(config.execName, "comet", "show-address"))[0]
    }

    fun getValidatorInfo(): Pubkey2 = wrapLog("getValidatorInfo", infoLevel = false) {
        execAndParse(listOf("comet", "show-validator"), includeOutputFlag = false)
    }

    fun getGovernanceProposals(): GovernanceProposals = wrapLog("getGovernanceProposals", infoLevel = false) {
        execAndParse(listOf("query", "gov", "proposals"))
    }

    fun getModelPerTokenPrice(modelId: String): ModelPerTokenPriceResponse = wrapLog("getModelPerTokenPrice", false) {
        execAndParse(listOf("query", "inference", "model-per-token-price", modelId))
    }

    fun getPocBatchCount(epochStartHeight: Long): Long = wrapLog("getPocBatchCount", infoLevel = false) {
        execAndParse<Count>(
            listOf(
                "query",
                "inference",
                "count-po-c-batches-at-height",
                epochStartHeight.toString()
            )
        ).count
    }

    fun getPocValidationCount(epochStartHeight: Long): Long = wrapLog("getPocValidationCount", infoLevel = false) {
        execAndParse<Count>(
            listOf(
                "query",
                "inference",
                "count-po-c-validations-at-height",
                epochStartHeight.toString()
            )
        ).count
    }

    fun getColdPrivateKey(): String = wrapLog("getColdPrivateKey", infoLevel = false) {
        val accountName = this.getColdAccountName()
        exec(listOf(config.execName, "keys", "export", accountName, "--unsafe", "--yes", "--unarmored-hex"), keyringPassword).first()
    }

    fun getWarmPrivateKey(): String = wrapLog("getWarmPrivateKey", infoLevel = false) {
        val accountName = this.getWarmAccountName()
        exec(listOf(config.execName, "keys", "export", accountName, "--unsafe", "--yes", "--unarmored-hex"), keyringPassword).first()
    }

    fun requestThresholdSignature(
        currentEpochId: Long,
        chainId: String,
        requestId: String,
        data: List<String>
    ): TxResponse = wrapLog("requestThresholdSignature", true) {
        val from = this.getColdAccountName()
        val baseArgs = listOf(
            "tx", "bls", "request-threshold-signature",
            currentEpochId.toString(),
            chainId.toByteArray().toHexString(),
            requestId.toByteArray().toHexString(),
        ) + data.map { it.toByteArray().toHexString() }

        val finalArgs = baseArgs + listOf(
            "--from", from,
            "--keyring-backend", "test",
            "--chain-id", config.chainId,
            "--keyring-dir", "/root/${config.stateDirName}",
            "--yes"
        )
        
        execAndParse(finalArgs)
    }

    data class Count(
        val count: Long = 0
    )
}

val maxBlockWaitTime = Duration.ofSeconds(15)


private val SEQUENCE_MISMATCH_PATTERN = ".*expected (\\d+), got (\\d+).*".toRegex()
private val TX_NOT_FOUND_PATTERN = "tx \\(([A-F0-9]+)\\) not found".toRegex()
private const val NOT_READY_MESSAGE = "inference is not ready; please wait for first block"

private fun getExecException(error: String): Throwable {
    val sequenceMatch = SEQUENCE_MISMATCH_PATTERN.find(error)
    val txNotFoundMatch = if (sequenceMatch == null) TX_NOT_FOUND_PATTERN.find(error) else null

    return when {
        sequenceMatch != null -> {
            val expected = sequenceMatch.groupValues[1].toInt()
            val actual = sequenceMatch.groupValues[2].toInt()
            AccountSequenceMismatchException(expected, actual)
        }

        txNotFoundMatch != null -> {
            TxNotFoundException(txNotFoundMatch.groupValues[1])
        }

        error.contains(NOT_READY_MESSAGE) -> NotReadyException()
        else -> IllegalArgumentException("Invalid usage of command: $error")
    }
}


class NotReadyException : Exception("Inference is not ready; please wait for first block")

class AccountSequenceMismatchException(val expected: Int, val actual: Int) :
    Exception("Account sequence mismatch, expected $expected, got $actual")

class TxNotFoundException(val txHash: String) : Exception("Transaction not found: $txHash")

val k8sRetryRule = CliRetryRule(
    retries = 5,
    delay = Duration.ofSeconds(3),
    operationRegexes = listOf("^get.+"),
    responseRegexes = listOf("Unknown stream id.+discarding message", "Unable to connect to the server")
)

data class CliRetryRule(
    val retries: Int,
    val delay: Duration,
    val operationRegexes: List<String>,
    val responseRegexes: List<String>,
) {
    private fun matchesOperation(operation: String): Boolean =
        operationRegexes.isEmpty() || operationRegexes.any { it.toRegex().containsMatchIn(operation) }

    private fun matchesResponse(response: String): Boolean =
        responseRegexes.isEmpty() || responseRegexes.any { it.toRegex().containsMatchIn(response) }

    fun retryDuration(operation: String, response: String, retryCount: Int): Duration? {
        return if (retryCount < retries && matchesOperation(operation) && matchesResponse(response)) {
            delay
        } else {
            null
        }
    }
}
