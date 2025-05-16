package com.productscience

import com.github.dockerjava.api.model.Volume
import com.github.dockerjava.core.DockerClientBuilder
import com.google.gson.reflect.TypeToken
import com.productscience.data.*
import org.tinylog.kotlin.Logger
import java.io.Closeable
import java.time.Duration
import java.time.Instant

// Usage
data class ApplicationCLI(
    val containerId: String,
    override val config: ApplicationConfig,
    val logOutput: LogOutput
) : HasConfig, Closeable {
    private val dockerClient = DockerClientBuilder.getInstance()
        .build()

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
            this.killNameConflicts()
            Logger.info("Creating container,  id={}", containerId)
            var createCmd = dockerClient.createContainerCmd(config.nodeImageName)
                .withName(containerId)
                .withVolumes(Volume(config.mountDir))
            if (doNotStartChain) {
                createCmd = createCmd.withCmd("tail", "-f", "/dev/null")
            }
            createCmd.exec()
            dockerClient.startContainerCmd(containerId).exec()
        }
    }

    private fun killNameConflicts() {
        wrapLog("killNameConflicts", false) {
            val containers = dockerClient.listContainersCmd().exec()
            containers.forEach {
                if (it.names.contains("/$containerId")) {
                    Logger.info("Killing conflicting container, id={}", it.id)
                    dockerClient.killContainerCmd(it.id).exec()
                    dockerClient.removeContainerCmd(it.id).exec()
                }
            }
        }
    }

    override fun close() {
        this.killContainer()
    }

    fun killContainer() {
        wrapLog("killContainer", false) {
            Logger.info("Killing container, id={}", containerId)
            dockerClient.killContainerCmd(containerId).exec()
            dockerClient.removeContainerCmd(containerId).exec()
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
        check: (status: NodeInfoResponse) -> Boolean,
        description: String,
        staleTimeout: Duration = Duration.ofSeconds(20),
    ) {
        wrapLog("waitForState", false) {
            Logger.info("Waiting for state: {}", description)
            var timeout = Instant.now().plus(staleTimeout)
            var previousState: NodeInfoResponse? = null
            while (true) {
                val currentState = getStatus()
                if (check(currentState)) {
                    Logger.info("State reached: $description")
                    break
                }
                if (previousState != currentState) {
                    timeout = Instant.now().plus(staleTimeout)
                }
                if (Instant.now().isAfter(timeout)) {
                    Logger.error("State is stale, was identical for {}", staleTimeout)
                    error("State is stale, was identical for $staleTimeout")
                }
                previousState = currentState
                Logger.debug("Current block is {}, waiting...", currentState.syncInfo.latestBlockHeight)
                Thread.sleep(1000)
            }
        }
    }

    fun waitForMinimumBlock(minBlockHeight: Long, waitingFor: String = "") {
        wrapLog("waitForMinimumBlock", false) {
            waitForState(
                { it.syncInfo.latestBlockHeight >= minBlockHeight },
                "$waitingFor:block height $minBlockHeight"
            )
        }
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

    var addresss: String? = null
    fun getAddress(): String = wrapLog("getAddress", false) {
        if (addresss == null) {
            val keys = getKeys()
            addresss = (keys.firstOrNull { it.name == this.config.pairName.drop(1) } ?: keys.first()).address
        }
        addresss!!
    }

    // Use TypeToken to properly deserialize List<Validator>
    fun getKeys(): List<Validator> = wrapLog("getKeys", false) {
        execAndParseWithType(
            object : TypeToken<List<Validator>>() {},
            listOf("keys", "list") + config.keychainParams
        )
    }

    fun createKey(keyName: String): Validator = wrapLog("createKey", false) {
        execAndParse(
            listOf(
                "keys",
                "add",
                keyName
            ) + config.keychainParams
        )
    }

    fun getSelfBalance(denom: String = this.config.denom): Long = wrapLog("getSelfBalance", false) {
        val account = getAddress()
        val balance = getBalance(account, denom)
        balance.balance.amount
    }

    fun getBalance(address: String, denom: String): BalanceResponse = wrapLog("getBalance", false) {
        execAndParse(listOf("query", "bank", "balance", address, denom))
    }

    fun getGovParams(): GovState = wrapLog("getGovParams", false) {
        execAndParse(listOf("query", "gov", "params"))
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

    // Reified type parameter to abstract out exec and then json to a particular type
    inline fun <reified T> execAndParse(args: List<String>, includeOutputFlag: Boolean = true): T {
        val argsWithJson = listOf(config.execName) +
                args + if (includeOutputFlag) listOf("--output", "json") else emptyList()
        Logger.debug("Executing command: {}", argsWithJson.joinToString(" "))
        val response = exec(argsWithJson)
        val output = response.joinToString("")
        Logger.debug("Output: {}", output)
        if (output.contains("inference is not ready; please wait for first block")) {
            throw NotReadyException()
        }
        // Extract JSON payload if output contains gas estimate
        val jsonOutput = output.replace(Regex("^gas estimate: \\d+"), "")
        return cosmosJson.fromJson(jsonOutput, T::class.java)
    }

    // New function that allows using TypeToken for proper deserialization of generic types
    private fun <T> execAndParseWithType(typeToken: TypeToken<T>, args: List<String>): T {
        val argsWithJson = (listOf(config.execName) + args + "--output" + "json")
        Logger.debug("Executing command: {}", argsWithJson.joinToString(" "))
        val response = exec(argsWithJson)
        val output = response.joinToString("\n")
        Logger.debug("Output: {}", output)
        return cosmosJson.fromJson(output, typeToken.type)
    }


    fun exec(args: List<String>): List<String> {
        val execCreateCmdResponse = dockerClient.execCreateCmd(containerId)
            .withAttachStdout(true)
            .withAttachStderr(true)
            .withTty(true)
            .withCmd(*args.toTypedArray())
            .exec()

        val output = ExecCaptureOutput()
        Logger.trace("Executing command: {}", args.joinToString(" "))
        val execResponse = dockerClient.execStartCmd(execCreateCmdResponse.id).exec(output)
        execResponse.awaitCompletion()
        Logger.trace("Command complete: output={}", output.output)
        if (output.output.isNotEmpty() && output.output.first().startsWith("Usage:")) {
            val error = output.output.joinToString(separator = "").lines().last { it.isNotBlank() }
            throw getExecException(error)
        }
        return output.output
    }

    private fun extractSignature(response: List<String>): String {
        val signaturePattern = ".*Signature:\\s*([^,\\s]+).*".toRegex()
        return response.firstNotNullOfOrNull {
            signaturePattern.find(it)?.groupValues?.get(1)
        } ?: error("Could not extract signature from response: $response")
    }

    fun signPayload(payload: String, accountAddress: String? = null): String {
        val parameters = listOfNotNull(
            config.execName,
            "signature",
            "create",
            // Do we need single quotes here?
            payload,
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
            this.config.pairName.trimStart('/')
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