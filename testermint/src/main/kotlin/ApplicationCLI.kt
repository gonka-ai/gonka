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
import java.time.Instant
import java.time.format.DateTimeParseException

// Usage
data class ApplicationCLI(val containerId: String, override val config: ApplicationConfig) : HasConfig {
    private val dockerClient = DockerClientBuilder.getInstance()
        .build()

    fun createContainer() {
        wrapLog("createContainer", false) {
            Logger.info("Creating container with id $containerId")
            dockerClient.createContainerCmd(config.nodeImageName)
                .withName(containerId)
                .withVolumes(Volume(config.mountDir))
                .exec()
            dockerClient.startContainerCmd(containerId).exec()
        }
    }
    fun waitForMinimumBlock(minBlockHeight: Long) {
        wrapLog("waitForMinimumBlock", false) {
            Logger.info("Waiting for block height to reach $minBlockHeight")
            while (true) {
                val currentState = getStatus()
                val currentBlock = currentState.syncInfo.latestBlockHeight
                if (currentBlock >= minBlockHeight) {
                    Logger.info("Block height reached $currentBlock")
                    break
                }
                Logger.debug("Current block height is $currentBlock, waiting...")
                Thread.sleep(1000)
            }
        }
    }

    fun waitForNextBlock(blocksToWait: Int = 1) {
        wrapLog("waitForNextBlock", false) {
            val currentState = getStatus()
            val currentBlock = currentState.syncInfo.latestBlockHeight
            Logger.info("Waiting for block + $blocksToWait after $currentBlock")
            while (true) {
                val newState = getStatus()
                val newBlock = newState.syncInfo.latestBlockHeight
                if (newBlock >= currentBlock + blocksToWait) {
                    Logger.info("Block height reached $newBlock")
                    break
                }
                Logger.debug("Current block height is $newBlock, waiting...")
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
            listOf("keys", "list")
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
        Logger.debug("Executing command: ${argsWithJson.joinToString(" ")}")
        val response = exec(argsWithJson)
        val output = response.joinToString("\n")
        Logger.debug("Output:$output")
        return gsonSnakeCase.fromJson(output, T::class.java)
    }

    // New function that allows using TypeToken for proper deserialization of generic types
    private fun <T> execAndParseWithType(typeToken: TypeToken<T>, args: List<String>): T {
        val argsWithJson = (listOf(config.appName) + args + "--output" + "json")
        Logger.debug("Executing command: ${argsWithJson.joinToString(" ")}")
        val response = exec(argsWithJson)
        val output = response.joinToString("\n")
        Logger.debug("Output:$output")
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
        val execResponse = dockerClient.execStartCmd(execCreateCmdResponse.id).exec(output)
        execResponse.awaitCompletion()
        return output.output
    }

    fun signPayload(payload: String): String = wrapLog("signPayload", true) {
        val response = this.exec(
            listOf(
                config.appName,
                "signature",
                "create",
                // Do we need single quotes here?
                payload
            )
        )

        // so hacky
        response[1].dropWhile { it != ' ' }.drop(1)
    }

    fun transferMoneyTo(destinationNode: ApplicationCLI, amount: Long): TxResponse = wrapLog("transferMoneyTo", true) {
        val sourceAccount = this.getKeys()[0].address
        val destAccount = destinationNode.getKeys()[0].address
        val response = this.execAndParse<TxResponse>(
            listOf(
                "tx",
                "bank",
                "send",
                sourceAccount,
                destAccount,
                "$amount${config.denom}",
                "--keyring-backend",
                "test",
                "--chain-id=${config.chainId}",
                "--keyring-dir=/root/${config.stateDirName}",
                "--yes",
                "--output",
                "json"
            )
        )
        response
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
            "source" to "container"
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
                    Logger.info("Block height:$height")
                    currentHeight = height
                }
            }
        }

        if (logEntry.contains("INFO+")) {
            Logger.info(logEntry)
        } else if (logEntry.contains("INF") || logEntry.contains(" INFO ")) {
            // We map this to debug as there is a LOT of info level logs
            Logger.debug(logEntry)
        } else if (logEntry.contains("ERR") || logEntry.contains(" ERROR ")) {
            Logger.error(logEntry)
        } else if (logEntry.contains("DBG") || logEntry.contains(" DEBUG ")) {
            Logger.debug(logEntry)
        } else if (logEntry.contains("WRN") || logEntry.contains(" WARN ")) {
            Logger.warn(logEntry)
        } else {
            Logger.trace(logEntry)
        }
    }
}
