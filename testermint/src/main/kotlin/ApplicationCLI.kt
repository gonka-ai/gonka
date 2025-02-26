package com.productscience

import com.github.dockerjava.api.model.Volume
import com.github.dockerjava.core.DockerClientBuilder
import com.google.gson.reflect.TypeToken
import com.productscience.data.AppExport
import com.productscience.data.BalanceResponse
import com.productscience.data.InferenceParams
import com.productscience.data.InferenceTimeoutsWrapper
import com.productscience.data.InferencesWrapper
import com.productscience.data.NodeInfoResponse
import com.productscience.data.TokenomicsData
import com.productscience.data.TopMinersResponse
import com.productscience.data.TxResponse
import com.productscience.data.Validator
import com.productscience.data.parseProto
import org.tinylog.kotlin.Logger
import java.io.Closeable
import java.time.Duration
import java.time.Instant

// Usage
data class ApplicationCLI(
    val containerId: String,
    override val config: ApplicationConfig,
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

    fun waitForMinimumBlock(minBlockHeight: Long) {
        wrapLog("waitForMinimumBlock", false) {
            waitForState({ it.syncInfo.latestBlockHeight >= minBlockHeight }, "block height $minBlockHeight")
        }
    }

    fun waitForNextBlock(blocksToWait: Int = 1) {
        wrapLog("waitForNextBlock", false) {
            val currentState = getStatus()
            waitForMinimumBlock(currentState.syncInfo.latestBlockHeight + blocksToWait)
        }
    }
    
    fun getInferences(): InferencesWrapper = wrapLog("getInferences", false) {
        execAndParse(listOf("query", "inference", "list-inference"))
    }

    fun getInferenceTimeouts(): InferenceTimeoutsWrapper = wrapLog("getInferenceTimeouts", false) {
        execAndParse(listOf("query", "inference", "list-inference-timeout"))
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

    fun getSelfBalance(denom: String): Long = wrapLog("getSelfBalance", false) {
        val account = getAddress()
        val balance = getBalance(account, denom)
        balance.balance.amount
    }

    fun getBalance(address: String, denom: String): BalanceResponse = wrapLog("getBalance", false) {
        execAndParse(listOf("query", "bank", "balance", address, denom))
    }

    fun getInferenceParams(): InferenceParams = wrapLog("getInferenceParams", false) {
        // At present, there is a bug in Cosmos that causes this to fail, but it gives us something we can parse anyhow
        val response = exec(listOf(config.appName) + listOf("query", "inference", "params"))
        val protoText = """\{.*\}""".toRegex().find(response.first())?.value
        parseProto(protoText!!)
    }

    data class TokenomicsWrapper(val tokenomicsData: TokenomicsData)

    fun getTokenomics(): TokenomicsWrapper = wrapLog("getTokenomics", false) {
        execAndParse(listOf("query", "inference", "show-tokenomics-data"))
    }

    fun getTopMiners(): TopMinersResponse = wrapLog("getTopMiners", false) {
        execAndParse(listOf("query", "inference", "list-top-miner"))
    }

    // Reified type parameter to abstract out exec and then json to a particular type
    private inline fun <reified T> execAndParse(args: List<String>, includeOutputFlag: Boolean = true): T {
        val argsWithJson = listOf(config.appName) +
                args + if (includeOutputFlag) listOf("--output", "json") else emptyList()
        Logger.debug("Executing command: {}", argsWithJson.joinToString(" "))
        val response = exec(argsWithJson)
        val output = response.joinToString("")
        Logger.debug("Output: {}", output)
        return cosmosJson.fromJson(output, T::class.java)
    }

    // New function that allows using TypeToken for proper deserialization of generic types
    private fun <T> execAndParseWithType(typeToken: TypeToken<T>, args: List<String>): T {
        val argsWithJson = (listOf(config.appName) + args + "--output" + "json")
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
        if (output.output.first().startsWith("Usage:")) {
            val error = output.output.last().lines().last()
            Logger.error(
                "Invalid usage of command: command='{}' error='{}'",
                args.joinToString(" "), error
            )
            throw IllegalArgumentException("Invalid usage of command: $error")
        }
        return output.output
    }

    fun signPayload(payload: String, accountAddress: String? = null): String {
        val parameters = listOfNotNull(
            config.appName,
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

            // so hacky
            response[1].dropWhile { it != ' ' }.drop(1).also {
                Logger.info("Signature created, signature={}", it)
            }
        }
    }

    fun transferMoneyTo(destinationNode: ApplicationCLI, amount: Long): TxResponse = wrapLog("transferMoneyTo", true) {
        val sourceAccount = this.getKeys()[0].address
        val destAccount = destinationNode.getKeys()[0].address
        val response = this.submitTransaction(
            listOf(
                "bank",
                "send",
                sourceAccount,
                destAccount,
                "$amount${config.denom}",
            )
        )
        response
    }

    fun getTxStatus(txHash: String): TxResponse = wrapLog("getTxStatus", false) {
        execAndParse(listOf("query", "tx", "--type=hash", txHash))
    }

    fun submitUpgradeProposal(
        title: String,
        description: String,
        binaryPath: String,
        apiBinaryPath: String,
        height: Long,
    ): TxResponse = wrapLog("submitUpgradeProposal", true) {
        val proposer = this.getKeys()[0].address
        val binariesJson =
            """{"binaries":{"linux/amd64":"$binaryPath"},"api_binaries":{"linux/amd64":"$apiBinaryPath"}}"""
        this.submitTransaction(
            listOf(
                "upgrade",
                "software-upgrade",
                title,
                "--title",
                title,
                "--upgrade-height",
                "$height",
                "--upgrade-info",
                binariesJson,
                "--summary",
                description,
                "--deposit",
                // TODO: Denom and amount should not be hardcoded
                "100000nicoin",
                "--from",
                proposer,
            )
        )
    }

    fun makeGovernanceDeposit(proposalId: String, amount: Long): TxResponse = wrapLog("makeGovernanceDeposit", true) {
        val depositor = this.getKeys()[0].address
        this.submitTransaction(
            listOf(
                "gov",
                "deposit",
                proposalId,
                "$amount${config.denom}",
                "--from",
                depositor,
            )
        )
    }

    fun voteOnProposal(proposalId: String, option: String): TxResponse = wrapLog("voteOnProposal", true) {
        val voter = this.getKeys()[0].address
        this.submitTransaction(
            listOf(
                "gov",
                "vote",
                proposalId,
                option,
                "--from",
                voter,
            )
        )
    }

    fun submitTransaction(args: List<String>): TxResponse {
        val finalArgs =
            listOf("tx") + args + listOf(
                "--keyring-backend",
                "test",
                "--chain-id=${config.chainId}",
                "--keyring-dir=/root/${config.stateDirName}",
                "--yes",
                "--broadcast-mode",
                "sync"
            )
        val response = sendTransaction(finalArgs)
        if (response.height == 0L) {
            Thread.sleep(1000)
            val newResponse = this.waitForTxProcessed(response.txhash)
            check(newResponse.code == 0) {
                "Transaction failed: ${newResponse.rawLog}"
            }
            return newResponse
        }
        return response

    }

    private fun sendTransaction(finalArgs: List<String>): TxResponse {
        var response = this.execAndParse<TxResponse>(finalArgs)
        while (response.code == 32) {
            Logger.warn("Transaction account sequence mismatch, retrying")
            Thread.sleep(1000)
            response = this.execAndParse(finalArgs)
        }
        check(response.code == 0) { "Transaction failed: code=${response.code} log=${response.rawLog}" }
        return response
    }

    fun waitForTxProcessed(txHash: String, maxWait: Int = 10): TxResponse {
        var currentWait = 0
        while (true) {
            try {
                val response = this.getTxStatus(txHash)
                if (response.height != 0L) {
                    return response
                }
                Thread.sleep(500)
                currentWait++
                check(currentWait < maxWait) {
                    "Transaction not processed after $maxWait seconds"
                }
            } catch (e: IllegalArgumentException) {
                Logger.warn("Unable to find transaction with hash: {}. Exception: {}", txHash, e)
                currentWait++
            }
        }

    }

}

val maxBlockWaitTime = Duration.ofSeconds(15)

