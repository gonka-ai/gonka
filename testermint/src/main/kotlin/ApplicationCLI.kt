package com.productscience

import com.github.dockerjava.api.async.ResultCallback
import com.github.dockerjava.api.model.Frame
import com.github.dockerjava.api.model.Volume
import com.github.dockerjava.core.DockerClientBuilder
import com.google.gson.reflect.TypeToken
import com.productscience.data.BalanceResponse
import com.productscience.data.NodeInfoResponse
import com.productscience.data.TxResponse
import com.productscience.data.Validator
import org.tinylog.kotlin.Logger
import java.io.Closeable
import java.time.Instant
import java.time.format.DateTimeParseException

// Usage
data class ApplicationCLI(val containerId: String, override val config: ApplicationConfig) : HasConfig, Closeable {
    private val dockerClient = DockerClientBuilder.getInstance()
        .build()

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

    fun waitForMinimumBlock(minBlockHeight: Long) {
        wrapLog("waitForMinimumBlock", false) {
            Logger.info("Waiting for block height to reach {}", minBlockHeight)
            while (true) {
                val currentState = getStatus()
                val currentBlock = currentState.syncInfo.latestBlockHeight
                if (currentBlock >= minBlockHeight) {
                    Logger.info("Block height reached {}", currentBlock)
                    break
                }
                Logger.debug("Current block height is {}, waiting...", currentBlock)
                Thread.sleep(1000)
            }
        }
    }

    fun waitForNextBlock(blocksToWait: Int = 1) {
        wrapLog("waitForNextBlock", false) {
            val currentState = getStatus()
            val currentBlock = currentState.syncInfo.latestBlockHeight
            Logger.info("Waiting for block {} after {}", blocksToWait, currentBlock)
            while (true) {
                val newState = getStatus()
                val newBlock = newState.syncInfo.latestBlockHeight
                if (newBlock >= currentBlock + blocksToWait) {
                    Logger.info("Block height reached {}", newBlock)
                    break
                }
                Logger.debug("Current block height is {}, waiting...", newBlock)
                Thread.sleep(1000)
            }
        }
    }

    fun getStatus(): NodeInfoResponse = wrapLog("getStatus", false) { execAndParse(listOf("status")) }

    var addresss: String? = null
    fun getAddress(): String = wrapLog("getAddress", false) {
        if (addresss == null) {
            addresss = getKeys()[0].address
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

    // Reified type parameter to abstract out exec and then json to a particular type
    private inline fun <reified T> execAndParse(args: List<String>): T {
        val argsWithJson = listOf(config.appName) + args + "--output" + "json"
        Logger.debug("Executing command: {}", argsWithJson.joinToString(" "))
        val response = exec(argsWithJson)
        val output = response.joinToString("\n")
        Logger.debug("Output: {}", output)
        return gsonSnakeCase.fromJson(output, T::class.java)
    }

    // New function that allows using TypeToken for proper deserialization of generic types
    private fun <T> execAndParseWithType(typeToken: TypeToken<T>, args: List<String>): T {
        val argsWithJson = (listOf(config.appName) + args + "--output" + "json")
        Logger.debug("Executing command: {}", argsWithJson.joinToString(" "))
        val response = exec(argsWithJson)
        val output = response.joinToString("\n")
        Logger.debug("Output: {}", output)
        return gsonSnakeCase.fromJson(output, typeToken.type)
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
                "100000icoin",
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

class ExecCaptureOutput : ResultCallback.Adapter<Frame>() {
    val output = mutableListOf<String>()
    override fun onNext(frame: Frame) {
        output.add(String(frame.payload).trim())
    }
}

val timestampPattern = "^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}\\.\\d{9}Z".toRegex()

fun extractTimestamp(entireLine: String): Instant? {
    val matchResult = timestampPattern.find(entireLine)
    return if (matchResult != null) {
        try {
            Instant.parse(matchResult.value)
        } catch (e: DateTimeParseException) {
            null
        }
    } else {
        null
    }
}

class LogOutput(val name: String, val type: String) : ResultCallback.Adapter<Frame>() {
    var currentHeight = 0L
    val currentMessage = StringBuilder()
    val currentTimestamp: Instant? = null

    override fun onNext(frame: Frame) = logContext(
        mapOf(
            "operation" to type,
            "pair" to name,
            "source" to "container",
            "blockHeight" to currentHeight.toString()
        )
    ) {
        val logEntry = String(frame.payload).trim()
        val timestamp = extractTimestamp(logEntry)
        if (timestamp != null) {
            val entryWithoutTimestamp = logEntry.replaceFirst(timestampPattern, "").trim()
            if (currentMessage.isNotEmpty()) {
                log(currentMessage.toString())
                currentMessage.clear()
            }
            if (frame.payload.size < 1000) {
                log(entryWithoutTimestamp)
            } else {
                currentMessage.append(entryWithoutTimestamp)
            }
        } else {
            currentMessage.append(logEntry)
            if (frame.payload.size < 1000) {
                log(currentMessage.toString())
                currentMessage.clear()
            }
        }
        Unit
    }

    private fun log(logEntry: String) {
        if (logEntry.contains("committed state")) {
            // extract out height=123

            "height=?.+\\[0m(\\d+)".toRegex().find(logEntry)?.let {
                val height = it.groupValues[1].toLong()
                if (height > currentHeight) {
                    Logger.info("New block, height={}", height)
                    currentHeight = height
                }
            }
        }

        if (logEntry.contains("INFO+")) {
            Logger.info(logEntry)
        } else if (logEntry.contains("INF ") || logEntry.contains(" INFO ")) {
            // We map this to debug as there is a LOT of info level logs
            Logger.debug(logEntry)
        } else if (logEntry.contains("ERR") || logEntry.contains(" ERROR ")) {
            Logger.error(logEntry)
        } else if (logEntry.contains("DBG ") || logEntry.contains(" DEBUG ")) {
            Logger.debug(logEntry)
        } else if (logEntry.contains("WRN ") || logEntry.contains(" WARN ")) {
            Logger.warn(logEntry)
        } else {
            Logger.trace(logEntry)
        }
    }
}
